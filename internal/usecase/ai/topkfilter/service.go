package topkfilter

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"
	"assistant-api/internal/usecase/ai/embedding"

	"go.uber.org/zap"
)

const (
	// defaultLocale 對應目前 action_route seed 寫入的主要語系。
	// 若呼叫端沒有明確指定 locale，top-k 篩選就會落到這個預設值，
	// 避免 webhook 進來時因 locale 空值而完全查不到候選路由。
	defaultLocale = "zh-TW"
	// defaultTopK 控制每次向量檢索最多取回多少個候選。
	// 這裡的用途不是最終決策，而是先把最相關的一小批 action_route
	// 候選整理出來，提供後續觀察或正式決策流程使用。
	defaultTopK = 5
)

// Searcher 抽象出 action_route 的向量查詢能力。
// 目前由 repository.ActionRouteRepo 實作，方便在 usecase 層專注於：
// 1. 文字轉 embedding
// 2. embedding 轉 query vector
// 3. 呼叫資料層取得 top-k 候選
// 4. 將結果輸出成可追蹤的結構化 log
type Searcher interface {
	SearchTopByVectorAndLocale(ctx context.Context, locale string, queryVector string, topK int, skillCodes []string) ([]repository.ActionRouteVectorCandidate, error)
}

// Service 定義「收到訊息後做 top-k 候選篩選」的 usecase 介面。
// 這個介面刻意只暴露單一入口，讓 webhook 或其他 inbound adapter
// 不需要知道 embedding、pgvector 或 route repository 的細節。
type Service interface {
	FilterMessage(ctx context.Context, message *unifiedmessage.Message)
}

// service 是 top-k 篩選流程的實際實作。
// searcher 負責向量檢索；embedder 負責把文字轉成 embedding；
// locale/topK 則定義這個 service 的查詢預設行為。
type service struct {
	searcher Searcher
	embedder embedding.Service
	locale   string
	topK     int
}

// NewService 組裝 top-k 篩選服務。
// 這裡只做非常小的初始化規則：
// - searcher 或 embedder 缺任一個就不建立 service，避免半套依賴進入執行期。
// - locale 未指定時回退到 zh-TW，對齊目前 action_route seed。
// - topK 非正數時回退到預設 5，避免呼叫端忘記設定造成空查詢或過大查詢。
func NewService(searcher Searcher, embedder embedding.Service, locale string, topK int) Service {
	if searcher == nil || embedder == nil {
		return nil
	}
	locale = strings.TrimSpace(locale)
	if locale == "" {
		locale = defaultLocale
	}
	if topK <= 0 {
		topK = defaultTopK
	}
	return &service{searcher: searcher, embedder: embedder, locale: locale, topK: topK}
}

// FilterMessage 會在訊息一進來時先做正式的 top-k 候選篩選。
// 流程分成四段：
// 1. 快速拒絕非文字或空訊息，避免不必要的 embedding 成本。
// 2. 呼叫 embedding service，把文字轉成向量。
// 3. 用 query vector 對 action_route 做 pgvector top-k 查詢。
// 4. 將候選結果整理成結構化 log，方便觀察每則訊息被篩到哪些操作。
//
// 這裡刻意不回傳候選值，而是先專注在 webhook 入口的「先篩選、先記錄」。
// 若未來需要把候選帶往下一段正式決策，可再把回傳值或 context carrier 補上。
func (s *service) FilterMessage(ctx context.Context, message *unifiedmessage.Message) {
	// 第一層保護：service 未完整組裝、訊息不存在、或訊息不是文字時直接跳過。
	// 這能避免在 webhook 高頻入口對圖片/貼圖/空 payload 誤打 embedding API。
	if s == nil || s.searcher == nil || s.embedder == nil || message == nil || !message.IsText() {
		return
	}

	// 第二層保護：即使 message type 是 text，也仍要排除只含空白的內容。
	// 空字串沒有語意價值，送去 embedding 只會增加不必要的延遲與噪音。
	text := strings.TrimSpace(message.Text)
	if text == "" {
		return
	}

	// 先把使用者文字轉成 embedding。這是整個 top-k 篩選的前置條件；
	// 若 embedding 失敗，就無法進一步做向量比對。
	// 失敗策略採 debug log + return：
	// - 不阻塞 webhook 主流程
	// - 仍保留 channel/message 維度，方便回頭追查是哪則訊息失敗
	vector, err := s.embedder.GetEmbedding(ctx, text)
	if err != nil {
		zap.L().Debug("line message top-k filter skipped",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("reason", "embedding_failed"),
			zap.Error(err),
		)
		return
	}

	// repository 目前接受的是 JSON 字串型態的 query vector，
	// 因此這裡把 embedding 結果編碼成 JSON array，例如 [0.1,0.2,0.3]。
	// 這讓 usecase 層不需要知道底層 pgvector literal 的細節。
	queryVector, err := json.Marshal(vector)
	if err != nil {
		zap.L().Debug("line message top-k filter skipped",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("reason", "marshal_query_vector_failed"),
			zap.Error(err),
		)
		return
	}

	// 正式執行 top-k 向量篩選。
	// skillCodes 目前先傳 nil，表示不先做人為技能白名單限制，
	// 讓資料庫依距離自然排序出整體最相關候選。
	// 若未來有上游 intent / domain constraint，再把 skillCodes 接進來即可。
	candidates, err := s.searcher.SearchTopByVectorAndLocale(ctx, s.locale, string(queryVector), s.topK, nil)
	if err != nil {
		zap.L().Debug("line message top-k filter skipped",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("reason", "vector_search_failed"),
			zap.Error(err),
		)
		return
	}

	// 將候選整理成易讀的單行摘要，刻意把 rank / operation / skill /
	// distance / route_text 全部展開，讓 log 本身就足以判斷：
	// - 排名順序是否合理
	// - 哪個 skill 被召回
	// - 對應到哪條 route_text
	// - 距離是否過近或過遠
	// 這對調整 seed route、embedding 模型或未來的 threshold 都很重要。
	vectorLogs := make([]string, 0, len(candidates))
	for idx, candidate := range candidates {
		vectorLogs = append(vectorLogs,
			"rank="+strconv.Itoa(idx+1)+
				" operation="+strings.TrimSpace(candidate.APIOperation)+
				" skill="+strings.TrimSpace(candidate.SkillCode)+
				" distance="+fmt.Sprintf("%.6f", candidate.Distance)+
				" route_text="+strconv.Quote(strings.TrimSpace(candidate.RouteText)),
		)
	}

	// 最後輸出正式的 top-k 篩選結果。
	// 這筆 log 的語意是「該訊息已完成候選篩選」，不是單純 debug preview，
	// 因此使用 Info level，讓營運與開發在正常日誌中就能直接觀察召回結果。
	zap.L().Info("line message top-k filtered candidates",
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("locale", s.locale),
		zap.Int("top_k", s.topK),
		zap.String("text", text),
		zap.Strings("candidates", vectorLogs),
	)
}
