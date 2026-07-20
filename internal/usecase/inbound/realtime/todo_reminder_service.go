package realtime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"assistant-api/internal/ent"
	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"
	"assistant-api/internal/usecase/ai/reranker"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// RecentMessageStore 提供 todo reply linker 需要的上下文查詢能力。
// 這裡只依賴最小方法，避免 realtime usecase 直接綁死 repository concrete type。
type RecentMessageStore interface {
	ResolveParentMessage(ctx context.Context, message *ent.ChannelMessage) (*ent.ChannelMessage, error)
	FindMessageWindowAround(ctx context.Context, message *ent.ChannelMessage, beforeLimit int, afterLimit int) ([]*ent.ChannelMessage, error)
	FindRecentMessagesBefore(ctx context.Context, message *ent.ChannelMessage, limit int) ([]*ent.ChannelMessage, error)
}

// TodoCandidateInput 是 Todo Reminder usecase 傳給 repository 的落庫資料。
// 欄位對齊 llminteraction.TodoAnalysis，但保留 ent UUID，避免 repository 再解析目前訊息與 linked message。
type TodoCandidateInput struct {
	ChannelID       uuid.UUID
	MessageID       uuid.UUID
	LinkedMessageID uuid.UUID
	Decision        string
	Summary         string
	Assignees       []string
	DueText         string
	DueAt           *time.Time
	DueTimezone     string
	DuePrecision    string
	DueDecision     string
	DueConfidence   float64
	DueReason       string
	MissingFields   []string
	Confidence      float64
	Reason          string
}

// PersistTodoCandidateFunc 由 provider 層注入，負責把 Todo analyzer 結果寫入實際 repository。
// 這裡使用 function 而不是 repository concrete type，避免 realtime usecase 反向依賴 repository package。
type PersistTodoCandidateFunc func(ctx context.Context, input TodoCandidateInput) (*ent.TodoCandidate, error)

// TodoReminderService 是待辦提醒的即時訊息服務。
//
// 目前第一階段只做觀測：當非指令訊息完成 classification 後，
// 先把模型打出的 tag 印到 log，確認「channel 有人啟用待辦提醒 -> 訊息會被分類 -> tag 可被服務收到」這條路徑成立。
// 後續真正建立提醒、解析時間、寫入資料庫時，應該從這個 handler 往下擴充，
// 不要回到 provider webhook 裡做平台專屬邏輯。
type TodoReminderService struct {
	platformLabel        string
	repo                 RecentMessageStore
	persistTodoCandidate PersistTodoCandidateFunc
	llm                  llminteraction.InteractionService
	ranker               reranker.Service
	recentLimit          int
	replyChainMaxDepth   int
	timezone             string
}

// TodoReminderServiceOptions 提供待辦提醒服務的可觀測性設定。
type TodoReminderServiceOptions struct {
	PlatformLabel        string
	Repo                 RecentMessageStore
	PersistTodoCandidate PersistTodoCandidateFunc
	LLM                  llminteraction.InteractionService
	Ranker               reranker.Service
	RecentLimit          int
	ReplyChainMaxDepth   int
	Timezone             string
}

// NewTodoReminderService 建立待辦提醒即時服務。
func NewTodoReminderService(options TodoReminderServiceOptions) *TodoReminderService {
	return &TodoReminderService{
		platformLabel:        strings.TrimSpace(options.PlatformLabel),
		repo:                 options.Repo,
		persistTodoCandidate: options.PersistTodoCandidate,
		llm:                  options.LLM,
		ranker:               options.Ranker,
		timezone:             strings.TrimSpace(options.Timezone),
		// RecentLimit 必須由 config 層解析後注入；預設值集中在 ai.history_context.recent_message_limit，
		// usecase 不再內建 8，避免未來調整召回窗口時出現「設定檔一份、程式碼一份」的漂移。
		recentLimit: options.RecentLimit,
		// ReplyChainMaxDepth 同樣必須由 config 注入；usecase 不內建 4，
		// 避免 prompt 成本與 reply chain 追溯深度在設定檔外悄悄漂移。
		replyChainMaxDepth: options.ReplyChainMaxDepth,
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
		// 顯式平台 reply/quote 是使用者主動指定的強關聯，不是「從最近幾則訊息猜測」的弱關聯。
		// 因此這條路不能受 recent history window 限制：使用者可能引用幾小時或幾天前的待辦訊息，
		// 只要平台給了 reply_to_msg_id，就應該直接回資料庫查那一則被引用訊息。
		//
		// LINE 的 quotedMessageId、Slack 的 thread parent 會在 adapter 層統一寫入 ReplyToMsgID；
		// repository 的 ResolveParentMessage 集中管理 triggered_message_id / reply_to_msg_id 的解析順序。
		explicitReplyTarget, err := s.repo.ResolveParentMessage(ctx, messageCtx.SavedMessage)
		if err != nil {
			zap.L().Warn("todo reminder explicit reply context skipped: parent message query failed",
				zap.String("platform", s.platformLabel),
				zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
				zap.String("reply_to_msg_id", strings.TrimSpace(messageCtx.Message.ReplyToMsgID)),
				zap.Error(err),
			)
			return
		}
		if explicitReplyTarget == nil {
			// 這裡刻意不退回 recent messages 猜測。
			// 使用者既然按了 reply/quote，語意錨點就是那則 parent message；如果本地資料庫查不到，
			// 改用近端窗口很容易把另一個待辦誤認成承接目標，造成錯誤更新或取消候選。
			zap.L().Warn("todo reminder explicit reply context skipped: parent message not found",
				zap.String("platform", s.platformLabel),
				zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
				zap.String("reply_to_msg_id", strings.TrimSpace(messageCtx.Message.ReplyToMsgID)),
			)
			return
		}

		zap.L().Info("todo reminder explicit reply context resolved",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.String("reply_to_msg_id", strings.TrimSpace(messageCtx.Message.ReplyToMsgID)),
			zap.String("parent_message_id", explicitReplyTarget.ID.String()),
			zap.String("parent_text", strings.TrimSpace(explicitReplyTarget.Content)),
		)
		// explicit reply/quote 提供主要上下文錨點；但完整語意常散落在多個 message window：
		// 1. 被 reply/quote 的 parent message 附近，可能有人補了負責人、時間或取消條件。
		// 2. 目前訊息附近，可能有最新補充或同一串待辦的後續對話。
		// 3. 如果 parent message 本身又 reply 了更早的訊息，則沿著 reply chain 再加入上一層 window。
		// 所有 window 會去重並依 CreatedAt 組合，讓 analyzer 看到時間脈絡，而不是只看一串 rerank 候選。
		contextMessages, err := s.buildExplicitReplyContextMessages(ctx, messageCtx, explicitReplyTarget)
		if err != nil {
			return
		}
		prompt := buildImplicitReplyTodoPrompt(explicitReplyTarget, contextMessages, result)
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
			zap.L().Warn("todo reminder explicit reply todo analysis failed",
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

		zap.L().Info("todo reminder explicit reply todo analyzed",
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
		s.persistTodoCandidateAnalysis(ctx, messageCtx, analysis)
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
	prompt := buildImplicitReplyTodoPrompt(nil, recentMessages, result)
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
	s.persistTodoCandidateAnalysis(ctx, messageCtx, analysis)
}

func (s *TodoReminderService) buildExplicitReplyContextMessages(ctx context.Context, messageCtx MessageContext, explicitReplyTarget *ent.ChannelMessage) ([]*ent.ChannelMessage, error) {
	if s == nil || s.repo == nil || messageCtx.Message == nil || messageCtx.SavedMessage == nil {
		return nil, nil
	}
	if s.replyChainMaxDepth <= 0 {
		// reply chain 深度會直接放大「anchor 數量 x 每個 anchor 的 message window」，
		// 因此不能在 usecase 偷補預設值；缺設定時只使用第一層 explicit target，並把設定問題寫進 log。
		zap.L().Warn("todo reminder explicit reply chain depth is not configured; only direct reply target will be used",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.Int("reply_chain_max_depth", s.replyChainMaxDepth),
		)
	}
	anchors := []*ent.ChannelMessage{explicitReplyTarget}
	lastAnchor := explicitReplyTarget
	visited := map[uuid.UUID]struct{}{
		explicitReplyTarget.ID: {},
	}
	for depth := 1; depth < s.replyChainMaxDepth; depth++ {
		if lastAnchor == nil || strings.TrimSpace(lastAnchor.ReplyToMsgID) == "" {
			break
		}
		parent, err := s.repo.ResolveParentMessage(ctx, lastAnchor)
		if err != nil {
			zap.L().Warn("todo reminder explicit reply chain truncated: parent message query failed",
				zap.String("platform", s.platformLabel),
				zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
				zap.String("reply_to_msg_id", strings.TrimSpace(lastAnchor.ReplyToMsgID)),
				zap.Int("depth", depth),
				zap.Error(err),
			)
			return nil, err
		}
		if parent == nil {
			zap.L().Warn("todo reminder explicit reply chain truncated: parent message not found",
				zap.String("platform", s.platformLabel),
				zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
				zap.String("reply_to_msg_id", strings.TrimSpace(lastAnchor.ReplyToMsgID)),
				zap.Int("depth", depth),
			)
			break
		}
		if _, ok := visited[parent.ID]; ok {
			zap.L().Warn("todo reminder explicit reply chain truncated: cycle detected",
				zap.String("platform", s.platformLabel),
				zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
				zap.String("parent_message_id", parent.ID.String()),
				zap.Int("depth", depth),
			)
			break
		}
		visited[parent.ID] = struct{}{}
		anchors = append(anchors, parent)
		lastAnchor = parent
	}

	if s.recentLimit <= 0 {
		zap.L().Warn("todo reminder explicit reply context windows limited to anchors: recent limit is not configured",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.Int("recent_limit", s.recentLimit),
		)
		return mergeChannelMessageWindows(anchors, messageCtx.SavedMessage.ID), nil
	}

	windows := make([]*ent.ChannelMessage, 0, len(anchors)*((s.recentLimit*2)+1)+s.recentLimit)
	for _, anchor := range anchors {
		window, err := s.repo.FindMessageWindowAround(ctx, anchor, s.recentLimit, s.recentLimit)
		if err != nil {
			zap.L().Warn("todo reminder explicit reply context skipped: anchor window query failed",
				zap.String("platform", s.platformLabel),
				zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
				zap.String("anchor_message_id", anchor.ID.String()),
				zap.Error(err),
			)
			return nil, err
		}
		windows = append(windows, window...)
	}
	currentWindow, err := s.repo.FindMessageWindowAround(ctx, messageCtx.SavedMessage, s.recentLimit, 0)
	if err != nil {
		zap.L().Warn("todo reminder explicit reply context skipped: current window query failed",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.Error(err),
		)
		return nil, err
	}
	windows = append(windows, currentWindow...)
	contextMessages := mergeChannelMessageWindows(windows, messageCtx.SavedMessage.ID)
	zap.L().Info("todo reminder explicit reply context windows ready",
		zap.String("platform", s.platformLabel),
		zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
		zap.Int("anchor_count", len(anchors)),
		zap.Int("context_message_count", len(contextMessages)),
		zap.Any("anchors", formatChannelMessageLogEntries(anchors)),
		zap.Any("context_messages", formatChannelMessageLogEntries(contextMessages)),
	)
	return contextMessages, nil
}

func mergeChannelMessageWindows(items []*ent.ChannelMessage, excludeMessageIDs ...uuid.UUID) []*ent.ChannelMessage {
	excluded := make(map[uuid.UUID]struct{}, len(excludeMessageIDs))
	for _, id := range excludeMessageIDs {
		if id != uuid.Nil {
			excluded[id] = struct{}{}
		}
	}
	seen := make(map[uuid.UUID]struct{}, len(items))
	merged := make([]*ent.ChannelMessage, 0, len(items))
	for _, item := range items {
		if item == nil || item.ID == uuid.Nil {
			continue
		}
		if _, ok := excluded[item.ID]; ok {
			continue
		}
		if _, ok := seen[item.ID]; ok {
			continue
		}
		seen[item.ID] = struct{}{}
		merged = append(merged, item)
	}
	sort.SliceStable(merged, func(left int, right int) bool {
		if merged[left].CreatedAt.Equal(merged[right].CreatedAt) {
			return merged[left].ID.String() < merged[right].ID.String()
		}
		return merged[left].CreatedAt.Before(merged[right].CreatedAt)
	})
	return merged
}

func (s *TodoReminderService) persistTodoCandidateAnalysis(ctx context.Context, messageCtx MessageContext, analysis *llminteraction.TodoAnalysis) {
	if s == nil || s.persistTodoCandidate == nil || messageCtx.SavedMessage == nil || analysis == nil {
		// persistence function 沒注入時保留 log-only 模式，方便測試或尚未啟用資料表的環境先觀測 analyzer 結果。
		return
	}
	decision := strings.TrimSpace(analysis.Decision)
	if decision == "no_action" {
		// no_action 是 analyzer 明確判斷不應啟動 Todo Reminder；不落庫才能避免堆積無效候選。
		return
	}
	linkedMessageID, err := parseOptionalUUID(strings.TrimSpace(analysis.LinkedMessageID))
	if err != nil {
		zap.L().Warn("todo reminder candidate skipped: linked message id is invalid",
			zap.String("platform", s.platformLabel),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.String("linked_message_id", strings.TrimSpace(analysis.LinkedMessageID)),
			zap.Error(err),
		)
		return
	}
	dueTime := s.normalizeTodoDueTime(ctx, messageCtx, analysis)
	item, err := s.persistTodoCandidate(ctx, TodoCandidateInput{
		ChannelID:       messageCtx.SavedMessage.ChannelID,
		MessageID:       messageCtx.SavedMessage.ID,
		LinkedMessageID: linkedMessageID,
		Decision:        decision,
		Summary:         strings.TrimSpace(analysis.Summary),
		Assignees:       append([]string(nil), analysis.Assignees...),
		DueText:         strings.TrimSpace(analysis.DueText),
		DueAt:           dueTime.dueAt,
		DueTimezone:     dueTime.timezone,
		DuePrecision:    dueTime.precision,
		DueDecision:     dueTime.decision,
		DueConfidence:   dueTime.confidence,
		DueReason:       dueTime.reason,
		MissingFields:   append([]string(nil), analysis.MissingFields...),
		Confidence:      analysis.Confidence,
		Reason:          strings.TrimSpace(analysis.Reason),
	})
	if err != nil {
		zap.L().Warn("todo reminder candidate persistence failed",
			zap.String("platform", s.platformLabel),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.String("decision", decision),
			zap.Error(err),
		)
		return
	}
	if item == nil {
		return
	}
	zap.L().Info("todo reminder candidate persisted",
		zap.String("platform", s.platformLabel),
		zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
		zap.String("candidate_id", item.ID.String()),
		zap.String("status", string(item.Status)),
		zap.String("decision", string(item.LastDecision)),
	)
}

func parseOptionalUUID(value string) (uuid.UUID, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return uuid.Nil, nil
	}
	parsed, err := uuid.Parse(trimmed)
	if err != nil {
		return uuid.Nil, err
	}
	return parsed, nil
}

type normalizedTodoDueTime struct {
	dueAt      *time.Time
	timezone   string
	precision  string
	decision   string
	confidence float64
	reason     string
}

func (s *TodoReminderService) normalizeTodoDueTime(ctx context.Context, messageCtx MessageContext, analysis *llminteraction.TodoAnalysis) normalizedTodoDueTime {
	dueText := strings.TrimSpace(analysis.DueText)
	if s == nil || s.llm == nil || messageCtx.SavedMessage == nil || dueText == "" {
		return normalizedTodoDueTime{}
	}
	timezone := strings.TrimSpace(s.timezone)
	if timezone == "" {
		// timezone 必須由 config 注入；缺設定時只跳過時間正規化，不阻止 candidate 本身落庫。
		zap.L().Warn("todo reminder due time normalization skipped: timezone is not configured",
			zap.String("platform", s.platformLabel),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
		)
		return normalizedTodoDueTime{}
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		zap.L().Warn("todo reminder due time normalization skipped: timezone is invalid",
			zap.String("platform", s.platformLabel),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.String("timezone", timezone),
			zap.Error(err),
		)
		return normalizedTodoDueTime{}
	}
	if messageCtx.SavedMessage.CreatedAt.IsZero() {
		// reference_time 是相對時間解析的基準；缺失時不猜現在時間，避免重放舊訊息時解析成錯誤日期。
		zap.L().Warn("todo reminder due time normalization skipped: reference time is empty",
			zap.String("platform", s.platformLabel),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
		)
		return normalizedTodoDueTime{}
	}
	prompt := buildTodoDueTimePrompt(messageCtx, analysis, messageCtx.SavedMessage.CreatedAt.In(location), timezone)
	zap.L().Info("todo reminder due time normalizer request prepared",
		zap.String("platform", s.platformLabel),
		zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
		zap.String("due_text", dueText),
		zap.String("timezone", timezone),
		zap.String("prompt", prompt),
	)
	result, err := s.llm.AnalyzeTodoDueTime(ctx, prompt, dueText)
	if err != nil {
		zap.L().Warn("todo reminder due time normalization failed",
			zap.String("platform", s.platformLabel),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.String("due_text", dueText),
			zap.Error(err),
		)
		return normalizedTodoDueTime{}
	}
	if result == nil {
		return normalizedTodoDueTime{}
	}
	zap.L().Info("todo reminder due time normalized",
		zap.String("platform", s.platformLabel),
		zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
		zap.String("decision", strings.TrimSpace(result.Decision)),
		zap.String("due_at", strings.TrimSpace(result.DueAt)),
		zap.String("timezone", strings.TrimSpace(result.Timezone)),
		zap.String("precision", strings.TrimSpace(result.Precision)),
		zap.Float64("confidence", result.Confidence),
		zap.String("reason", strings.TrimSpace(result.Reason)),
	)
	if strings.TrimSpace(result.Decision) != "normalized" {
		return normalizedTodoDueTime{decision: strings.TrimSpace(result.Decision), timezone: strings.TrimSpace(result.Timezone), precision: strings.TrimSpace(result.Precision), confidence: result.Confidence, reason: strings.TrimSpace(result.Reason)}
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(result.DueAt))
	if err != nil {
		zap.L().Warn("todo reminder due time normalization skipped: due_at is invalid",
			zap.String("platform", s.platformLabel),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.String("due_at", strings.TrimSpace(result.DueAt)),
			zap.Error(err),
		)
		return normalizedTodoDueTime{}
	}
	return normalizedTodoDueTime{dueAt: &parsed, decision: strings.TrimSpace(result.Decision), timezone: strings.TrimSpace(result.Timezone), precision: strings.TrimSpace(result.Precision), confidence: result.Confidence, reason: strings.TrimSpace(result.Reason)}
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

func buildImplicitReplyTodoPrompt(explicitReplyTarget *ent.ChannelMessage, recentMessages []*ent.ChannelMessage, result ClassificationResult) string {
	var builder strings.Builder
	// 同一份 todo_analysis contract 同時支援兩種上下文來源：
	// 1. Explicit reply/quote target：平台明確指出的 parent message，不受 recentLimit 影響。
	// 2. Context messages：多個 message window 去重後依時間排序；沒有 reply/quote 時則是單一 recent window。
	// prompt 明確要求 linked_message_id 只能來自這兩個區塊，讓下游 persistence 可以用 message id
	// 回查既有 TodoCandidate，避免模型編造不可追蹤的 linkage。
	builder.WriteString("你是 Todo Reminder 的內部 structured analyzer。請判斷 Current message 是否應形成、更新、承接或取消待辦候選，即使使用者沒有按平台 reply。\n")
	builder.WriteString("只能輸出 todo_analysis JSON contract：schema_version, decision, linked_message_id, summary, assignees, due_text, confidence, missing_fields, reason。\n")
	builder.WriteString("decision 只能是 create_candidate、update_candidate、acknowledge、cancel_candidate、needs_more_info、no_action。\n")
	builder.WriteString("linked_message_id 必須來自 Explicit reply/quote target 或 Context messages 的 id；全新待辦或 no_action 時使用空字串。\n")
	builder.WriteString("若存在 Explicit reply/quote target，這是使用者明確引用的訊息，即使很久以前也優先作為接續上下文。\n")
	builder.WriteString("Context messages 可能由多個 message window 組合而成，已去重並依時間排序；請把它當成補充脈絡，不要忽略較舊的 explicit target。\n")
	builder.WriteString("提醒、請記得、到時提醒我、不要忘記、幫我記一下等日常提醒語氣，只要包含可追蹤事項、承諾、交付、時間或對象，就屬於 Todo candidate；不可只因為語氣日常就判 no_action。\n")
	builder.WriteString("summary 是整理後的待辦內容；due_text 保留使用者原本的時間字面，不要自行正規化日期；assignees 保留訊息中可見的人名或稱呼。\n")
	builder.WriteString("欄位型別必須固定：schema_version/decision/linked_message_id/summary/due_text/reason 是 string，confidence 是 number，assignees 與 missing_fields 永遠是 string array；沒有人名或缺漏欄位時輸出 []，不可輸出字串、物件或 null。\n")
	builder.WriteString("needs_more_info 時 missing_fields 必須指出缺 summary、assignees 或 due_text 等欄位；no_action 時 linked_message_id、summary、assignees、due_text、missing_fields 都必須為空。\n")
	builder.WriteString("JSON shape 範例：{\"schema_version\":\"v1\",\"decision\":\"no_action\",\"linked_message_id\":\"\",\"summary\":\"\",\"assignees\":[],\"due_text\":\"\",\"confidence\":0.0,\"missing_fields\":[],\"reason\":\"...\"}\n")
	builder.WriteString("不可輸出額外欄位，不可用自然語言包住 JSON。\n\n")
	builder.WriteString("Explicit reply/quote target:\n")
	if explicitReplyTarget != nil {
		builder.WriteString(fmt.Sprintf("id=%s sender=%s text=%q\n", explicitReplyTarget.ID.String(), strings.TrimSpace(explicitReplyTarget.SenderName), strings.TrimSpace(explicitReplyTarget.Content)))
	} else {
		builder.WriteString("none\n")
	}
	builder.WriteString("\n")
	builder.WriteString("Context messages:\n")
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

func buildTodoDueTimePrompt(messageCtx MessageContext, analysis *llminteraction.TodoAnalysis, referenceTime time.Time, timezone string) string {
	return strings.TrimSpace(fmt.Sprintf(`你是 Todo Reminder 的時間正規化器。請把 due_text 依 reference_time 與 timezone 轉成 RFC3339 due_at。
只能輸出 todo_due_time JSON contract：schema_version, decision, due_at, timezone, precision, confidence, missing_fields, reason。

規則：
- decision 只能是 normalized、needs_more_info、no_due_time。
- normalized 時 due_at 必須是 RFC3339，例如 2026-07-20T09:00:00+08:00，timezone 必須使用輸入 timezone。
- 若 due_text 只有日期沒有時間，precision 使用 date，due_at 可用該日期 09:00:00 作為候選提醒時間，但 reason 必須說明時間是預設候選。
- 若 due_text 太模糊而無法安全排程，decision 使用 needs_more_info 並填 missing_fields。
- 不可輸出額外欄位，不可用自然語言包住 JSON。

reference_time=%s
timezone=%s
current_text=%q
summary=%q
due_text=%q`,
		referenceTime.Format(time.RFC3339),
		strings.TrimSpace(timezone),
		strings.TrimSpace(messageCtx.Message.Text),
		strings.TrimSpace(analysis.Summary),
		strings.TrimSpace(analysis.DueText),
	))
}
