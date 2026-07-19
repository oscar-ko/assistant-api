package realtime

import (
	"context"
	"fmt"
	"strings"

	"assistant-api/internal/ent"
	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"
	"assistant-api/internal/usecase/ai/reranker"

	"go.uber.org/zap"
)

// RecentMessageStore 提供 implicit reply linker 需要的近端上下文查詢能力。
// 這裡只依賴最小方法，避免 realtime usecase 直接綁死 repository concrete type。
type RecentMessageStore interface {
	FindRecentMessagesBefore(ctx context.Context, message *ent.ChannelMessage, limit int) ([]*ent.ChannelMessage, error)
}

// TodoReminderService 是待辦提醒的即時訊息服務。
//
// 目前第一階段只做觀測：當非指令訊息完成 classification 後，
// 先把模型打出的 tag 印到 log，確認「channel 有人啟用待辦提醒 -> 訊息會被分類 -> tag 可被服務收到」這條路徑成立。
// 後續真正建立提醒、解析時間、寫入資料庫時，應該從這個 handler 往下擴充，
// 不要回到 provider webhook 裡做平台專屬邏輯。
type TodoReminderService struct {
	platformLabel string
	repo          RecentMessageStore
	llm           llminteraction.InteractionService
	ranker        reranker.Service
	recentLimit   int
}

// TodoReminderServiceOptions 提供待辦提醒服務的可觀測性設定。
type TodoReminderServiceOptions struct {
	PlatformLabel string
	Repo          RecentMessageStore
	LLM           llminteraction.InteractionService
	Ranker        reranker.Service
	RecentLimit   int
}

// NewTodoReminderService 建立待辦提醒即時服務。
func NewTodoReminderService(options TodoReminderServiceOptions) *TodoReminderService {
	return &TodoReminderService{
		platformLabel: strings.TrimSpace(options.PlatformLabel),
		repo:          options.Repo,
		llm:           options.LLM,
		ranker:        options.Ranker,
		// RecentLimit 必須由 config 層解析後注入；預設值集中在 ai.history_context.recent_message_limit，
		// usecase 不再內建 8，避免未來調整召回窗口時出現「設定檔一份、程式碼一份」的漂移。
		recentLimit: options.RecentLimit,
	}
}

// HandleClassification 接收非指令訊息的分類結果。
//
// 目前不根據 tag 做分支，也不寫入待辦資料；只把 tag、labels 與訊息定位資訊印出來。
// 這可先驗證 classifier 對每則進入掃描流程的訊息打了什麼標籤，
// 也避免在模型標籤尚未穩定前過早綁定業務行為。
func (s *TodoReminderService) HandleClassification(ctx context.Context, messageCtx MessageContext, result ClassificationResult) {
	if s == nil || messageCtx.Message == nil {
		return
	}
	message := messageCtx.Message
	zap.L().Info("todo reminder classification observed",
		zap.String("platform", s.platformLabel),
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("tag", strings.TrimSpace(result.Tag)),
		zap.String("signal", strings.TrimSpace(result.Signal)),
		zap.Float64("confidence", result.Confidence),
		zap.Float64("score_margin", result.ScoreMargin),
		zap.Strings("labels", result.Labels),
		zap.String("model_name", strings.TrimSpace(result.ModelName)),
	)

	// 中期 implicit reply 流程：
	// classifier 只判斷這則非指令訊息值得進一步檢查；真正「它是否接續前文待辦」交給 context analyzer。
	// 這能處理使用者沒有按 reply、但隔幾句回「我晚點弄」的情境，不用靠關鍵字規則硬判斷。
	s.analyzeImplicitReplyContext(ctx, messageCtx, result)
}

func (s *TodoReminderService) analyzeImplicitReplyContext(ctx context.Context, messageCtx MessageContext, result ClassificationResult) {
	if s == nil || s.repo == nil || s.llm == nil || messageCtx.Message == nil || messageCtx.SavedMessage == nil {
		// implicit reply linker 需要「已落庫訊息」作為查詢邊界，也需要 LLM context analyzer 做最終判斷；
		// 任一依賴缺失時直接略過，避免在非指令 realtime 流程中產生半套 side-effect。
		return
	}
	if strings.TrimSpace(messageCtx.Message.ReplyToMsgID) != "" {
		// 顯式平台 reply 會走既有 reply chain；這裡只處理「沒有按 reply」的隱式接續。
		return
	}
	if s.recentLimit <= 0 {
		// recentLimit 是 config-driven；如果設定解析後仍無效，寧可略過 implicit linker，
		// 不用 usecase 自行補預設值，避免隱性覆蓋部署環境的設定錯誤。
		zap.L().Warn("todo reminder implicit reply context skipped: recent limit is not configured",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.Int("recent_limit", s.recentLimit),
		)
		return
	}

	zap.L().Info("todo reminder implicit reply context analysis started",
		zap.String("platform", s.platformLabel),
		zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
		zap.String("saved_message_id", messageCtx.SavedMessage.ID.String()),
		zap.String("current_text", strings.TrimSpace(messageCtx.Message.Text)),
		zap.Int("recent_limit", s.recentLimit),
		zap.String("classifier_tag", strings.TrimSpace(result.Tag)),
		zap.String("classifier_signal", strings.TrimSpace(result.Signal)),
		zap.Float64("classifier_confidence", result.Confidence),
		zap.Float64("classifier_score_margin", result.ScoreMargin),
	)

	// 先做便宜的同 channel 近端召回，只限制訊息筆數，不在 repository 層判斷語意。
	// 語意排序與是否真的相關，分別交給 reranker 與 context analyzer，避免資料層夾帶業務規則。
	recentMessages, err := s.repo.FindRecentMessagesBefore(ctx, messageCtx.SavedMessage, s.recentLimit)
	if err != nil {
		zap.L().Warn("todo reminder implicit reply context skipped: recent message query failed",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.Error(err),
		)
		return
	}
	zap.L().Info("todo reminder implicit reply recent messages recalled",
		zap.String("platform", s.platformLabel),
		zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
		zap.Int("recent_limit", s.recentLimit),
		zap.Int("candidate_count", len(recentMessages)),
		zap.Any("candidates", formatChannelMessageLogEntries(recentMessages)),
	)
	if len(recentMessages) == 0 {
		return
	}
	// 如果有注入 reranker，先把「目前短句」和「近端歷史訊息」做文字對精排。
	// 這一步只調整候選順序，不直接宣告 linked/no_link，避免把模型分數當成最終語意結論。
	recentMessages, err = s.rerankImplicitReplyCandidates(ctx, strings.TrimSpace(messageCtx.Message.Text), recentMessages)
	if err != nil {
		zap.L().Warn("todo reminder implicit reply context skipped: rerank failed",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.Error(err),
		)
		return
	}
	zap.L().Info("todo reminder implicit reply candidates ready for context analyzer",
		zap.String("platform", s.platformLabel),
		zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
		zap.Int("candidate_count", len(recentMessages)),
		zap.Any("ordered_candidates", formatChannelMessageLogEntries(recentMessages)),
	)

	// todo analyzer 是最後一道結構化判斷：它會在 bounded context 內輸出 todo 專用 decision，
	// 下游只讀 schema，不需要靠自然語言或關鍵字猜測使用者是不是在接前文。
	prompt := buildImplicitReplyTodoPrompt(recentMessages, result)
	zap.L().Info("todo reminder todo analyzer request prepared",
		zap.String("platform", s.platformLabel),
		zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
		zap.String("current_text", strings.TrimSpace(messageCtx.Message.Text)),
		zap.String("prompt", prompt),
		zap.Int("prompt_length", len([]rune(prompt))),
	)
	analysis, err := s.llm.AnalyzeTodo(ctx, prompt, strings.TrimSpace(messageCtx.Message.Text))
	if err != nil {
		zap.L().Warn("todo reminder implicit reply todo analysis failed",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.Error(err),
		)
		return
	}
	if analysis == nil {
		return
	}

	zap.L().Info("todo reminder implicit reply todo analyzed",
		zap.String("platform", s.platformLabel),
		zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
		zap.String("decision", strings.TrimSpace(analysis.Decision)),
		zap.String("linked_message_id", strings.TrimSpace(analysis.LinkedMessageID)),
		zap.String("summary", strings.TrimSpace(analysis.Summary)),
		zap.Strings("assignees", append([]string(nil), analysis.Assignees...)),
		zap.String("due_text", strings.TrimSpace(analysis.DueText)),
		zap.Float64("confidence", analysis.Confidence),
		zap.Strings("missing_fields", append([]string(nil), analysis.MissingFields...)),
		zap.String("reason", strings.TrimSpace(analysis.Reason)),
	)
}

func (s *TodoReminderService) rerankImplicitReplyCandidates(ctx context.Context, query string, recentMessages []*ent.ChannelMessage) ([]*ent.ChannelMessage, error) {
	platformLabel := ""
	if s != nil {
		platformLabel = s.platformLabel
	}
	if s == nil || s.ranker == nil || len(recentMessages) <= 1 {
		// 沒有 reranker 或候選不足時，保留 repository 回傳的時間序；
		// 這不是語意 fallback，而是表示目前沒有可執行的精排依賴或排序空間。
		zap.L().Info("todo reminder implicit reply rerank skipped",
			zap.String("platform", platformLabel),
			zap.Bool("ranker_configured", s != nil && s.ranker != nil),
			zap.Int("candidate_count", len(recentMessages)),
		)
		return recentMessages, nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		zap.L().Info("todo reminder implicit reply rerank skipped: empty query",
			zap.String("platform", s.platformLabel),
			zap.Int("candidate_count", len(recentMessages)),
		)
		return recentMessages, nil
	}

	documents := make([]string, 0, len(recentMessages))
	messageByDocumentIndex := make(map[int]*ent.ChannelMessage, len(recentMessages))
	for _, item := range recentMessages {
		if item == nil {
			continue
		}
		document := strings.TrimSpace(item.Content)
		if document == "" {
			continue
		}
		// reranker API 只知道 documents 的陣列 index；這裡保存 index -> message 的對照，
		// 才能在精排回傳後安全還原成原本的 ChannelMessage，避免平行陣列錯位。
		messageByDocumentIndex[len(documents)] = item
		documents = append(documents, document)
	}
	if len(documents) <= 1 {
		zap.L().Info("todo reminder implicit reply rerank skipped: insufficient non-empty documents",
			zap.String("platform", s.platformLabel),
			zap.String("query", query),
			zap.Int("candidate_count", len(recentMessages)),
			zap.Int("document_count", len(documents)),
		)
		return recentMessages, nil
	}
	zap.L().Info("todo reminder implicit reply rerank request prepared",
		zap.String("platform", s.platformLabel),
		zap.String("query", query),
		zap.Int("document_count", len(documents)),
		zap.Strings("documents", append([]string(nil), documents...)),
	)

	ranked, err := s.ranker.Rerank(ctx, query, documents, len(documents))
	if err != nil {
		return nil, err
	}
	zap.L().Info("todo reminder implicit reply rerank result received",
		zap.String("platform", s.platformLabel),
		zap.String("query", query),
		zap.Int("ranked_count", len(ranked)),
		zap.Any("ranked_documents", formatRankedDocumentLogEntries(ranked)),
	)
	if len(ranked) == 0 {
		return recentMessages, nil
	}

	reordered := make([]*ent.ChannelMessage, 0, len(ranked))
	used := make(map[int]struct{}, len(ranked))
	for _, item := range ranked {
		message, ok := messageByDocumentIndex[item.Index]
		if !ok || message == nil {
			continue
		}
		reordered = append(reordered, message)
		used[item.Index] = struct{}{}
	}
	// 有些 reranker 實作可能只回 top-k 子集合；未回傳的候選保留在尾端，
	// 讓 context analyzer 仍可看見完整召回窗口，而不是因精排截斷而漏掉必要上下文。
	for index, message := range messageByDocumentIndex {
		if _, ok := used[index]; ok || message == nil {
			continue
		}
		reordered = append(reordered, message)
	}
	if len(reordered) == 0 {
		return recentMessages, nil
	}
	zap.L().Info("todo reminder implicit reply rerank reordered candidates",
		zap.String("platform", s.platformLabel),
		zap.Int("candidate_count", len(reordered)),
		zap.Any("ordered_candidates", formatChannelMessageLogEntries(reordered)),
	)
	return reordered, nil
}

func formatChannelMessageLogEntries(messages []*ent.ChannelMessage) []map[string]any {
	entries := make([]map[string]any, 0, len(messages))
	for index, message := range messages {
		if message == nil {
			continue
		}
		entries = append(entries, map[string]any{
			"order":               index + 1,
			"id":                  message.ID.String(),
			"platform_message_id": strings.TrimSpace(message.PlatformMessageID),
			"sender_id":           strings.TrimSpace(message.SenderID),
			"sender_name":         strings.TrimSpace(message.SenderName),
			"message_type":        strings.TrimSpace(message.MessageType),
			"content":             strings.TrimSpace(message.Content),
			"created_at":          message.CreatedAt,
		})
	}
	return entries
}

func formatRankedDocumentLogEntries(documents []reranker.RankedDocument) []map[string]any {
	entries := make([]map[string]any, 0, len(documents))
	for index, document := range documents {
		entries = append(entries, map[string]any{
			"rank":           index + 1,
			"document_index": document.Index,
			"document":       strings.TrimSpace(document.Document),
			"score":          document.Score,
		})
	}
	return entries
}

func buildImplicitReplyTodoPrompt(recentMessages []*ent.ChannelMessage, result ClassificationResult) string {
	var builder strings.Builder
	builder.WriteString("你是 Todo Reminder 的內部 structured analyzer。請判斷 Current message 是否應形成、更新、承接或取消待辦候選，即使使用者沒有按平台 reply。\n")
	builder.WriteString("只能輸出 todo_analysis JSON contract：schema_version, decision, linked_message_id, summary, assignees, due_text, confidence, missing_fields, reason。\n")
	builder.WriteString("decision 只能是 create_candidate、update_candidate、acknowledge、cancel_candidate、needs_more_info、no_action。\n")
	builder.WriteString("linked_message_id 必須來自 Recent messages 的 id；全新待辦或 no_action 時使用空字串。\n")
	builder.WriteString("summary 是整理後的待辦內容；due_text 保留使用者原本的時間字面，不要自行正規化日期；assignees 保留訊息中可見的人名或稱呼。\n")
	builder.WriteString("needs_more_info 時 missing_fields 必須指出缺 summary、assignees 或 due_text 等欄位；no_action 時 linked_message_id、summary、assignees、due_text、missing_fields 都必須為空。\n")
	builder.WriteString("不可輸出額外欄位，不可用自然語言包住 JSON。\n\n")
	builder.WriteString("Recent messages:\n")
	for index, item := range recentMessages {
		if item == nil {
			continue
		}
		builder.WriteString(fmt.Sprintf("%d. id=%s sender=%s text=%q\n", index+1, item.ID.String(), strings.TrimSpace(item.SenderName), strings.TrimSpace(item.Content)))
	}
	builder.WriteString("\nClassifier observation:\n")
	builder.WriteString(fmt.Sprintf("tag=%s signal=%s confidence=%.4f score_margin=%.4f\n",
		strings.TrimSpace(result.Tag),
		strings.TrimSpace(result.Signal),
		result.Confidence,
		result.ScoreMargin,
	))
	return builder.String()
}
