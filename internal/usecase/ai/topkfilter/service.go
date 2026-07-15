package topkfilter

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"
	"assistant-api/internal/usecase/ai/embedding"
	"assistant-api/internal/usecase/ai/reranker"

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
// 回傳值為經過 retrieval + rerank 後的最終候選清單，方便呼叫端再將
// 結果交給下一階段（例如 semantic decision）做最終決策。
type Service interface {
	FilterMessage(ctx context.Context, message *unifiedmessage.Message) []ScoredCandidate
}

// service 是 top-k pipeline 的編排層。
// 它只負責流程控制，不直接實作 retrieval 或 rerank 細節：
// - retriever: 第一階段候選召回（vector retrieval）
// - reranker: 第二階段候選重排（cross-encoder rerank）
// locale/retrievalTopK/rerankTopK 則用於輸出可觀測資訊。
type service struct {
	pipeline      CandidatePipeline
	locale        string
	retrievalTopK int
	rerankTopK    int
}

// NewService 組裝 top-k 篩選服務。
// 這裡只做非常小的初始化規則：
// - searcher 或 embedder 缺任一個就不建立 service，避免半套依賴進入執行期。
// - locale 未指定時回退到 zh-TW，對齊目前 action_route seed。
// - topK 非正數時回退到預設 5，避免呼叫端忘記設定造成空查詢或過大查詢。
func NewService(searcher Searcher, embedder embedding.Service, locale string, topK int) Service {
	return NewServiceWithReranker(searcher, embedder, nil, locale, topK, topK)
}

// NewServiceWithReranker 組裝具備 cross-encoder 重排能力的 top-k 篩選服務。
// retrievalTopK 控制第一階段向量召回可取回的候選數量（通常設得較大，確保正確候選不會被漏掉）；
// rerankTopK 控制第二階段 cross-encoder 精排後最終保留的候選數量（通常設得較小，作為送進下一步決策的窄範圍）。
func NewServiceWithReranker(searcher Searcher, embedder embedding.Service, rerankerSvc reranker.Service, locale string, retrievalTopK int, rerankTopK int) Service {
	// 組裝第一階段召回器（top-k retrieval）。
	retriever := NewTopKRetriever(searcher, embedder, locale, retrievalTopK)
	// 組裝第二階段重排器（cross-encoder rerank），narrow 到 rerankTopK 筆。
	reorder := NewCandidateReranker(rerankerSvc, rerankTopK)
	// 將兩階段串成可觀測的候選處理管線。
	pipeline := NewCandidatePipelineWithStages(retriever, reorder)
	if pipeline == nil {
		return nil
	}
	if strings.TrimSpace(locale) == "" {
		locale = defaultLocale
	}
	if retrievalTopK <= 0 {
		retrievalTopK = defaultTopK
	}
	if rerankTopK <= 0 {
		rerankTopK = defaultTopK
	}
	return &service{pipeline: pipeline, locale: locale, retrievalTopK: retrievalTopK, rerankTopK: rerankTopK}
}

// FilterMessage 會在訊息一進來時先做正式的 top-k 候選篩選。
// 流程分成四段：
// 1. 快速拒絕非文字或空訊息，避免不必要的 embedding 成本。
// 2. 呼叫 embedding service，把文字轉成向量。
// 3. 用 query vector 對 action_route 做 pgvector top-k 查詢。
// 4. 將候選結果整理成結構化 log，方便觀察每則訊息被篩到哪些操作。
//
// 回傳值為最終（rerank 成功則經過重排，否則回退原 retrieval 順序）的候選清單，
// 讓呼叫端可以把這些候選帶往下一步正式決策（例如 semantic decision）。
func (s *service) FilterMessage(ctx context.Context, message *unifiedmessage.Message) []ScoredCandidate {
	// 第一層保護：service 未完整組裝、訊息不存在、或訊息不是文字時直接跳過。
	// 這能避免在 webhook 高頻入口對圖片/貼圖/空 payload 誤打 embedding API。
	if s == nil || s.pipeline == nil || message == nil || !message.IsText() {
		return nil
	}

	// 第二層保護：即使 message type 是 text，也仍要排除只含空白的內容。
	// 空字串沒有語意價值，送去 embedding 只會增加不必要的延遲與噪音。
	text := strings.TrimSpace(message.Text)
	if text == "" {
		return nil
	}
	// platform 來自 unified message，讓 log 不再綁定特定通訊軟體。
	platform := strings.TrimSpace(message.Platform)

	// 第一階段：向量召回 top-k 候選。
	candidates, err := s.pipeline.RetrieveCandidates(ctx, text)
	if err != nil {
		reason := "vector_retrieval_failed"
		switch {
		case errors.Is(err, errEmbeddingFailed):
			reason = "embedding_failed"
		case errors.Is(err, errMarshalQueryVectorFailed):
			reason = "marshal_query_vector_failed"
		case errors.Is(err, errVectorSearchFailed):
			reason = "vector_search_failed"
		}
		zap.L().Debug("inbound message top-k filter skipped",
			zap.String("platform", platform),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("reason", reason),
			zap.Error(err),
		)
		return nil
	}

	// 先輸出 retrieval 階段結果，方便與 rerank 後結果分開比較。
	topKScoredCandidates := buildScoredCandidates(candidates)
	retrievedLogs := formatCandidateLogs(topKScoredCandidates)
	zap.L().Info("inbound message top-k retrieved candidates",
		zap.String("platform", platform),
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("locale", s.locale),
		zap.Int("retrieval_top_k", s.retrievalTopK),
		zap.String("text", text),
		zap.Strings("candidates", retrievedLogs),
	)

	scoredCandidates := topKScoredCandidates
	rerankApplied := false
	// 第二階段：若啟用 cross-encoder，則在候選集上做精排。
	// 失敗時不阻斷流程，回退到 retrieval 原排序。
	reordered, applied, rerankErr := s.pipeline.RerankCandidates(ctx, text, candidates)
	if rerankErr != nil {
		zap.L().Debug("inbound message top-k rerank skipped",
			zap.String("platform", platform),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("reason", "rerank_failed"),
			zap.Error(rerankErr),
		)
	} else {
		// 僅在重排成功時覆寫候選結果，保留失敗 fallback 能力。
		scoredCandidates = reordered
		rerankApplied = applied
	}

	// 將候選整理成易讀的單行摘要，刻意把 rank / operation / skill /
	// distance / route_text 全部展開，讓 log 本身就足以判斷：
	// - 排名順序是否合理
	// - 哪個 skill 被召回
	// - 對應到哪條 route_text
	// - 距離是否過近或過遠
	// 這對調整 seed route、embedding 模型或未來的 threshold 都很重要。
	vectorLogs := formatCandidateLogs(scoredCandidates)

	// 第二次輸出 rerank 結果，並附上與 top-k 的排名對照，方便直接比較差異。
	rankDiffLogs := formatCandidateRankDiffs(topKScoredCandidates, scoredCandidates)
	zap.L().Info("inbound message top-k reranked candidates",
		zap.String("platform", platform),
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("locale", s.locale),
		zap.Int("rerank_top_k", s.rerankTopK),
		zap.Bool("rerank_applied", rerankApplied),
		zap.String("text", text),
		zap.Strings("candidates", vectorLogs),
		zap.Strings("rank_comparison", rankDiffLogs),
	)

	// 回傳最終候選（reranked 優先，rerank 失敗則為 retrieval 原序），
	// 交由呼叫端決定是否要送進下一步的最終決策（例如 semantic decision）。
	return scoredCandidates
}

// formatCandidateLogs 將候選轉成易讀字串，供結構化日誌輸出。
// 若某筆包含 Score，會額外帶上 rerank_score，方便直接比較前後排序。
func formatCandidateLogs(candidates []ScoredCandidate) []string {
	vectorLogs := make([]string, 0, len(candidates))
	for idx, item := range candidates {
		candidate := item.Candidate
		rerankScore := ""
		if item.Score != nil {
			// 分數存在即輸出，代表該候選有被某個 stage 評分。
			rerankScore = " rerank_score=" + fmt.Sprintf("%.6f", *item.Score)
		}
		vectorLogs = append(vectorLogs,
			"rank="+strconv.Itoa(idx+1)+
				" operation="+strings.TrimSpace(candidate.APIOperation)+
				" skill="+strings.TrimSpace(candidate.SkillCode)+
				" distance="+fmt.Sprintf("%.6f", candidate.Distance)+
				rerankScore+
				" route_text="+strconv.Quote(strings.TrimSpace(candidate.RouteText)),
		)
	}
	return vectorLogs
}

// buildScoredCandidates 將純候選包成顯式結構，表示目前沒有額外分數。
func buildScoredCandidates(candidates []repository.ActionRouteVectorCandidate) []ScoredCandidate {
	out := make([]ScoredCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, ScoredCandidate{Candidate: candidate})
	}
	return out
}

// formatCandidateRankDiffs 產生 top-k 與 rerank 的排名對照，便於快速觀察名次變化。
func formatCandidateRankDiffs(before []ScoredCandidate, after []ScoredCandidate) []string {
	// 使用 queue 保留同一 key 的多筆排名，避免重複 operation 被後者覆蓋。
	beforeRankQueue := make(map[string][]int, len(before))
	for idx, item := range before {
		key := comparisonKey(item.Candidate)
		if key == "" {
			continue
		}
		beforeRankQueue[key] = append(beforeRankQueue[key], idx+1)
	}

	diffs := make([]string, 0, len(after))
	for idx, item := range after {
		key := comparisonKey(item.Candidate)
		if key == "" {
			continue
		}
		afterPos := idx + 1
		beforeRanks := beforeRankQueue[key]
		if len(beforeRanks) == 0 {
			diffs = append(diffs,
				"op="+strings.TrimSpace(item.Candidate.APIOperation)+
					" "+
					"NA->"+strconv.Itoa(afterPos)+
					" (NA)",
			)
			continue
		}

		// 同 key 多筆時，按出現順序逐筆配對，確保比較結果穩定。
		beforePos := beforeRanks[0]
		beforeRankQueue[key] = beforeRanks[1:]
		shift := beforePos - afterPos
		diffs = append(diffs,
			"op="+strings.TrimSpace(item.Candidate.APIOperation)+
				" "+
				strconv.Itoa(beforePos)+"->"+strconv.Itoa(afterPos)+
				" ("+formatRankShift(shift)+")",
		)
	}

	return diffs
}

// comparisonKey 產生候選穩定識別鍵，用於跨階段配對同一筆候選。
func comparisonKey(candidate repository.ActionRouteVectorCandidate) string {
	operation := strings.TrimSpace(candidate.APIOperation)
	skill := strings.TrimSpace(candidate.SkillCode)
	route := strings.TrimSpace(candidate.RouteText)
	if operation == "" && route == "" {
		return ""
	}
	return operation + "|" + skill + "|" + route
}

// formatRankShift 將名次變化轉成簡短人類可讀格式。
func formatRankShift(shift int) string {
	if shift > 0 {
		return "+" + strconv.Itoa(shift)
	}
	return strconv.Itoa(shift)
}
