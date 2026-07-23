package realtime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
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
	FindTodoCandidatesByMessageIDs(ctx context.Context, channelID uuid.UUID, messageIDs []uuid.UUID) ([]*ent.TodoCandidate, error)
	FindActiveTodoCandidatesWithEvidence(ctx context.Context, channelID uuid.UUID, limit int) ([]*ent.TodoCandidate, error)
	FindRecentTodoCandidateEvidenceMessages(ctx context.Context, candidateID uuid.UUID, limit int) ([]*ent.TodoCandidateEvidenceMessage, error)
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
	platformLabel                   string
	repo                            RecentMessageStore
	persistTodoCandidate            PersistTodoCandidateFunc
	llm                             llminteraction.InteractionService
	ranker                          reranker.Service
	recentLimit                     int
	replyChainMaxDepth              int
	evidenceAnchorLimitPerCandidate int
	evidenceWindowBeforeLimit       int
	evidenceWindowAfterLimit        int
	maxCandidateContexts            int
	maxContextMessages              int
	timezone                        string
	channelLocksMu                  sync.Mutex
	channelLocks                    map[string]*sync.Mutex
}

// TodoReminderServiceOptions 提供待辦提醒服務的可觀測性設定。
type TodoReminderServiceOptions struct {
	PlatformLabel                   string
	Repo                            RecentMessageStore
	PersistTodoCandidate            PersistTodoCandidateFunc
	LLM                             llminteraction.InteractionService
	Ranker                          reranker.Service
	RecentLimit                     int
	ReplyChainMaxDepth              int
	EvidenceAnchorLimitPerCandidate int
	EvidenceWindowBeforeLimit       int
	EvidenceWindowAfterLimit        int
	MaxCandidateContexts            int
	MaxContextMessages              int
	Timezone                        string
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
		channelLocks:         make(map[string]*sync.Mutex),
		// RecentLimit 必須由 config 層解析後注入；預設值集中在 ai.todo_reminder.recent_context_message_limit。
		// usecase 不內建 8，避免 Todo Reminder 的 prompt 成本和召回半徑在設定檔外悄悄漂移。
		recentLimit: options.RecentLimit,
		// ReplyChainMaxDepth 同樣必須由 config 注入；usecase 不內建 4，
		// 避免 prompt 成本與 reply chain 追溯深度在設定檔外悄悄漂移。
		replyChainMaxDepth:              options.ReplyChainMaxDepth,
		evidenceAnchorLimitPerCandidate: options.EvidenceAnchorLimitPerCandidate,
		evidenceWindowBeforeLimit:       options.EvidenceWindowBeforeLimit,
		evidenceWindowAfterLimit:        options.EvidenceWindowAfterLimit,
		maxCandidateContexts:            options.MaxCandidateContexts,
		maxContextMessages:              options.MaxContextMessages,
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

	unlock := s.lockTodoReminderChannel(messageCtx)
	defer unlock()

	// 中期 implicit reply 流程：
	// classifier 只判斷這則非指令訊息值得進一步檢查；真正「它是否接續前文待辦」交給 context analyzer。
	// 這能處理使用者沒有按 reply、但隔幾句回「我晚點弄」的情境，不用靠關鍵字規則硬判斷。
	s.analyzeImplicitReplyContext(ctx, messageCtx, result)
}

func (s *TodoReminderService) lockTodoReminderChannel(messageCtx MessageContext) func() {
	if s == nil {
		return func() {}
	}
	key := ""
	if messageCtx.SavedMessage != nil && messageCtx.SavedMessage.ChannelID != uuid.Nil {
		key = messageCtx.SavedMessage.ChannelID.String()
	} else if messageCtx.Message != nil {
		key = strings.TrimSpace(messageCtx.Message.ChannelID)
	}
	if key == "" {
		return func() {}
	}
	s.channelLocksMu.Lock()
	if s.channelLocks == nil {
		s.channelLocks = make(map[string]*sync.Mutex)
	}
	lock := s.channelLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		s.channelLocks[key] = lock
	}
	s.channelLocksMu.Unlock()
	lock.Lock()
	return lock.Unlock
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
		candidateContexts, err := s.findTodoCandidateContexts(ctx, messageCtx, contextMessages)
		if err != nil {
			return
		}
		prompt := buildImplicitReplyTodoPrompt(explicitReplyTarget, contextMessages, contextMessages, candidateContexts, result)
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
	recentCandidateContexts, err := s.findTodoCandidateContexts(ctx, messageCtx, recentMessages)
	if err != nil {
		return
	}
	evidenceMessages, evidenceCandidateContexts, err := s.buildTodoCandidateEvidenceContextMessages(ctx, messageCtx)
	if err != nil {
		return
	}
	conversationMessages := limitChannelMessages(mergeChannelMessageWindows(append(append([]*ent.ChannelMessage(nil), recentMessages...), evidenceMessages...)), s.maxContextMessages)
	candidateContexts := limitTodoCandidates(mergeTodoCandidateContexts(recentCandidateContexts, evidenceCandidateContexts), s.maxCandidateContexts)
	if !shouldAnalyzeImplicitTodoResult(result) && len(candidateContexts) == 0 {
		zap.L().Info("todo reminder implicit reply context skipped: classifier did not identify todo and no candidate context exists",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.String("classifier_tag", strings.TrimSpace(result.Tag)),
			zap.String("classifier_signal", strings.TrimSpace(result.Signal)),
			zap.Float64("classifier_confidence", result.Confidence),
			zap.Float64("classifier_score_margin", result.ScoreMargin),
		)
		return
	}
	// 如果有注入 reranker，先把「目前短句」和「近端歷史訊息」做文字對精排。
	// 這一步只調整候選順序，不直接宣告 linked/no_link，避免把模型分數當成最終語意結論。
	contextMessages, err := s.rerankImplicitReplyCandidates(ctx, strings.TrimSpace(messageCtx.Message.Text), conversationMessages)
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
		zap.Int("candidate_count", len(contextMessages)),
		zap.Int("candidate_context_count", len(candidateContexts)),
		zap.Any("ordered_candidates", formatChannelMessageLogEntries(contextMessages)),
	)

	// todo analyzer 是最後一道結構化判斷：它會在 bounded context 內輸出 todo 專用 decision，
	// 下游只讀 schema，不需要靠自然語言或關鍵字猜測使用者是不是在接前文。
	prompt := buildImplicitReplyTodoPrompt(nil, contextMessages, conversationMessages, candidateContexts, result)
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

func (s *TodoReminderService) findTodoCandidateContexts(ctx context.Context, messageCtx MessageContext, messages []*ent.ChannelMessage) ([]*ent.TodoCandidate, error) {
	if s == nil || s.repo == nil || messageCtx.SavedMessage == nil || messageCtx.SavedMessage.ChannelID == uuid.Nil || len(messages) == 0 {
		return nil, nil
	}
	messageIDs := make([]uuid.UUID, 0, len(messages))
	seen := make(map[uuid.UUID]struct{}, len(messages))
	for _, message := range messages {
		if message == nil || message.ID == uuid.Nil {
			continue
		}
		if _, ok := seen[message.ID]; ok {
			continue
		}
		seen[message.ID] = struct{}{}
		messageIDs = append(messageIDs, message.ID)
	}
	if len(messageIDs) == 0 {
		return nil, nil
	}
	candidates, err := s.repo.FindTodoCandidatesByMessageIDs(ctx, messageCtx.SavedMessage.ChannelID, messageIDs)
	if err != nil {
		zap.L().Warn("todo reminder candidate context skipped: candidate query failed",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.Error(err),
		)
		return nil, err
	}
	return candidates, nil
}

// buildTodoCandidateEvidenceContextMessages 以 active candidate 的 evidence anchors 重建長討論串上下文。
//
// recent window 只能看目前訊息附近的相鄰對話；當待辦跨很多則閒聊後才被更新時，相關訊息可能早就滑出 recent window。
// 這個 helper 會先取仍活躍的 candidate，再用每個 candidate 最近的 evidence anchor 往前後展開小訊息窗，
// 最後交給 merge/limit 流程和 recent window 合併。它只負責召回候選上下文，不在這裡判斷語意是否真的承接。
func (s *TodoReminderService) buildTodoCandidateEvidenceContextMessages(ctx context.Context, messageCtx MessageContext) ([]*ent.ChannelMessage, []*ent.TodoCandidate, error) {
	if s == nil || s.repo == nil || messageCtx.Message == nil || messageCtx.SavedMessage == nil || messageCtx.SavedMessage.ChannelID == uuid.Nil {
		return nil, nil, nil
	}
	if s.maxCandidateContexts <= 0 || s.evidenceAnchorLimitPerCandidate <= 0 {
		// evidence recall 是可調的長討論串記憶層；缺少候選數或 anchor 數設定時不啟用，
		// 讓系統保留原本 recent window 行為，而不是在 usecase 補隱性預設值。
		return nil, nil, nil
	}
	candidates, err := s.repo.FindActiveTodoCandidatesWithEvidence(ctx, messageCtx.SavedMessage.ChannelID, s.maxCandidateContexts)
	if err != nil {
		zap.L().Warn("todo reminder evidence context skipped: active candidate query failed",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.Error(err),
		)
		return nil, nil, err
	}
	if len(candidates) == 0 {
		return nil, nil, nil
	}
	contextMessages := make([]*ent.ChannelMessage, 0)
	for _, candidate := range candidates {
		if candidate == nil || candidate.ID == uuid.Nil {
			continue
		}
		evidenceItems, err := s.repo.FindRecentTodoCandidateEvidenceMessages(ctx, candidate.ID, s.evidenceAnchorLimitPerCandidate)
		if err != nil {
			zap.L().Warn("todo reminder evidence context skipped: evidence anchor query failed",
				zap.String("platform", s.platformLabel),
				zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
				zap.String("candidate_id", candidate.ID.String()),
				zap.Error(err),
			)
			return nil, nil, err
		}
		for _, evidence := range evidenceItems {
			if evidence == nil || evidence.Edges.Message == nil {
				continue
			}
			window, err := s.repo.FindMessageWindowAround(ctx, evidence.Edges.Message, s.evidenceWindowBeforeLimit, s.evidenceWindowAfterLimit)
			if err != nil {
				zap.L().Warn("todo reminder evidence context skipped: evidence window query failed",
					zap.String("platform", s.platformLabel),
					zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
					zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
					zap.String("candidate_id", candidate.ID.String()),
					zap.String("evidence_message_id", evidence.MessageID.String()),
					zap.Error(err),
				)
				return nil, nil, err
			}
			contextMessages = append(contextMessages, window...)
		}
	}
	mergedMessages := limitChannelMessages(mergeChannelMessageWindows(contextMessages, messageCtx.SavedMessage.ID), s.maxContextMessages)
	zap.L().Info("todo reminder evidence context recalled",
		zap.String("platform", s.platformLabel),
		zap.String("channel_id", strings.TrimSpace(messageCtx.Message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
		zap.Int("candidate_count", len(candidates)),
		zap.Int("context_message_count", len(mergedMessages)),
		zap.Any("context_messages", formatChannelMessageLogEntries(mergedMessages)),
	)
	return mergedMessages, candidates, nil
}

// mergeTodoCandidateContexts 合併 recent window 與 evidence window 找到的 candidate contexts。
//
// 同一個 candidate 可能同時被近端訊息與 evidence anchor 命中；這裡只去重並保留先出現的順序，
// 不做語意重排，避免資料組裝層偷偷取代 analyzer 的最終判斷。
func mergeTodoCandidateContexts(groups ...[]*ent.TodoCandidate) []*ent.TodoCandidate {
	seen := make(map[uuid.UUID]struct{})
	merged := make([]*ent.TodoCandidate, 0)
	for _, group := range groups {
		for _, candidate := range group {
			if candidate == nil || candidate.ID == uuid.Nil {
				continue
			}
			if _, ok := seen[candidate.ID]; ok {
				continue
			}
			seen[candidate.ID] = struct{}{}
			merged = append(merged, candidate)
		}
	}
	return merged
}

// limitTodoCandidates 是 prompt 成本的最後候選數保護欄。
// limit <= 0 代表不在這層截斷，方便測試或明確關閉 candidate context 上限。
func limitTodoCandidates(items []*ent.TodoCandidate, limit int) []*ent.TodoCandidate {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return append([]*ent.TodoCandidate(nil), items[:limit]...)
}

// limitChannelMessages 是送進 analyzer 前的總訊息數保護欄。
// 合併後的訊息已按時間排序，因此超過上限時保留最新一段，讓 current message 附近與最近 evidence 更不容易被截掉。
func limitChannelMessages(items []*ent.ChannelMessage, limit int) []*ent.ChannelMessage {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return append([]*ent.ChannelMessage(nil), items[len(items)-limit:]...)
}

func (s *TodoReminderService) persistTodoCandidateAnalysis(ctx context.Context, messageCtx MessageContext, analysis *llminteraction.TodoAnalysis) {
	if s == nil || s.persistTodoCandidate == nil || messageCtx.SavedMessage == nil || analysis == nil {
		// persistence function 沒注入時保留 log-only 模式，方便測試或尚未啟用資料表的環境先觀測 analyzer 結果。
		return
	}
	// 這裡是 structured analyzer 輸出進入資料庫前的最後整理點：
	// linked_message_id 轉成 UUID、due_text 交給獨立 normalizer、missing_fields 合併兩個模型契約的缺口。
	// repository 之後只看這份結構化輸入，不再重新解析自然語言，避免語意判斷分散在多層。
	decision := strings.TrimSpace(analysis.Decision)
	if decision == "no_action" {
		// no_action 是 analyzer 明確判斷不應啟動 Todo Reminder；不落庫才能避免堆積無效候選。
		zap.L().Info("todo reminder candidate persistence skipped: no_action",
			zap.String("platform", s.platformLabel),
			zap.String("message_id", strings.TrimSpace(messageCtx.Message.PlatformMessageID)),
			zap.String("reason", strings.TrimSpace(analysis.Reason)),
		)
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
	missingFields := mergeTodoMissingFields(analysis.MissingFields, dueTime.missingFields)
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
		MissingFields:   missingFields,
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
	dueAt         *time.Time
	timezone      string
	precision     string
	decision      string
	confidence    float64
	missingFields []string
	reason        string
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
		return normalizedTodoDueTime{decision: strings.TrimSpace(result.Decision), timezone: strings.TrimSpace(result.Timezone), precision: strings.TrimSpace(result.Precision), confidence: result.Confidence, missingFields: append([]string(nil), result.MissingFields...), reason: strings.TrimSpace(result.Reason)}
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

func mergeTodoMissingFields(groups ...[]string) []string {
	seen := make(map[string]struct{})
	merged := make([]string, 0)
	for _, group := range groups {
		for _, field := range group {
			trimmed := strings.TrimSpace(field)
			if trimmed == "" {
				continue
			}
			if _, ok := seen[trimmed]; ok {
				continue
			}
			seen[trimmed] = struct{}{}
			merged = append(merged, trimmed)
		}
	}
	return merged
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

func latestNonNilMessage(messages []*ent.ChannelMessage) *ent.ChannelMessage {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index] != nil {
			return messages[index]
		}
	}
	return nil
}

func shouldAnalyzeImplicitTodoResult(result ClassificationResult) bool {
	signal := strings.TrimSpace(result.Signal)
	tag := strings.TrimSpace(result.Tag)
	return signal == ClassificationSignalCandidate || tag == "todo"
}

func buildImplicitReplyTodoPrompt(explicitReplyTarget *ent.ChannelMessage, recentMessages []*ent.ChannelMessage, conversationMessages []*ent.ChannelMessage, candidateContexts []*ent.TodoCandidate, result ClassificationResult) string {
	var builder strings.Builder
	// 同一份 todo_analysis contract 同時支援兩種上下文來源：
	// 1. Explicit reply/quote target：平台明確指出的 parent message，不受 recentLimit 影響。
	// 2. Context messages：多個 message window 去重後依時間排序；沒有 reply/quote 時則是單一 recent window。
	// prompt 明確要求 linked_message_id 只能來自這兩個訊息區塊，讓下游 persistence 可以用 message id
	// 回查既有 TodoCandidate；candidate context 只提供 message-id linkage hint，不提供 row id。
	builder.WriteString("你是 Todo Reminder 的內部 structured analyzer。請判斷 Current message 是否應形成、更新、承接或取消待辦候選，即使使用者沒有按平台 reply。\n")
	builder.WriteString("只能輸出 todo_analysis JSON contract：schema_version, decision, linked_message_id, summary, assignees, due_text, confidence, missing_fields, reason。\n")
	builder.WriteString("decision 只能是 create_candidate、update_candidate、acknowledge、cancel_candidate、needs_more_info、no_action。\n")
	builder.WriteString("linked_message_id 必須只來自 Explicit reply/quote target 或 Context messages 的訊息 id；不可輸出 Todo candidate row id。Todo candidate contexts 的 source_message_id、last_message_id 是 linkage hint：若其中某個 message id 也出現在 Explicit reply/quote target 或 Context messages，就可以輸出該 message id；全新待辦或 no_action 時使用空字串。\n")
	builder.WriteString("若存在 Explicit reply/quote target，這是使用者明確引用的訊息，即使很久以前也優先作為接續上下文。\n")
	builder.WriteString("Context messages 可能由多個 message window 組合而成，已去重並依時間排序；請把它當成補充脈絡，不要忽略較舊的 explicit target。\n")
	builder.WriteString("Context messages 是語意精排後的候選；Conversation messages in chronological order 是實際對話輪次。判斷短確認、否定、可行性回覆或改期時，請用 chronological order 決定最近被回覆的提案/詢問，再用 Context messages 與 Todo candidate contexts 補充狀態，不可只看 rerank 排名。\n")
	builder.WriteString("判斷 update_candidate/acknowledge/cancel_candidate 時，Current message 必須和 linked message 指向同一件可追蹤事項；請比較行動主體、待辦目標、時間/條件變更、語用功能與對話輪次是否共同支持同一任務，不可只因為兩則訊息都有時間、人物或相似詞就連結。\n")
	builder.WriteString("選擇 linked_message_id 時必須先比較 distinctive task phrase：Current message 中最能識別任務的交付物、主題名詞、文件/清單/報告名稱、客戶/專案範圍或限制條件，必須和被連結 candidate context 的 known_summary 指向同一任務；不可只因為兩個 candidate 都有整理、清單、回報、Oscar、期限等泛用元素，或 rerank 排名較前，就連到不同任務。\n")
	builder.WriteString("若 Current message 表示原本時間、可用性或條件不成立，並提出替代時間、條件或執行安排，且上下文顯示它仍在處理同一件可追蹤待辦，decision 應為 update_candidate，linked_message_id 指向被修改的原任務或上一則改期提案；不可因為訊息表面像個人狀態或詢問就判 no_action。\n")
	builder.WriteString("若 Current message 的主要語用功能是在對既有任務提出替代時間、替代條件或可行性確認，且它沒有引入新的獨立任務目標，decision 不可使用 create_candidate；應輸出 update_candidate 並連到被修改的既有任務訊息。\n")
	builder.WriteString("個人可用性、請假、行程或狀態描述只有在 standalone 且沒有承接既有任務時才可判 no_action；若它是在回覆前文交辦、提案或待確認任務，並改變原時間/條件或詢問替代安排是否可行，必須依同一任務輸出 update_candidate，而不是 no_action。\n")
	builder.WriteString("Mandatory decision procedure：先判 Current message 的語用類型，再判是否承接既有 Todo candidate context，最後才判欄位完整性。A. 若 Current message 的 distinctive task phrase、任務目標、交付物、限制條件、負責人或期限可和任一 Todo candidate context 歸為同一件事，必須輸出 update_candidate 或 acknowledge，linked_message_id 指向該 candidate 的 source_message_id 或 last_message_id，不可輸出 create_candidate；若只能和另一個 candidate 共用泛用元素但 distinctive task phrase 不一致，必須視為不同任務，不可連結。B. 若 Current message 本身包含可追蹤行動、交付內容、時間安排、承諾或請求他人完成事項，且無法對應任何既有 Todo candidate context，這才是新任務內容訊息，輸出 create_candidate 或 needs_more_info，不可因 Latest prior conversation message 是問題/狀態回覆而輸出 acknowledge。C. 若 Current message 主要是在說明不可用、請假、行程衝突、條件不成立，並提出替代時間/替代條件/詢問替代安排是否可行，且 Latest prior conversation message 或 Context messages 顯示前文有交辦或待確認任務，輸出 update_candidate，linked_message_id 指向被修改的任務提案或交辦訊息。D. 只有 Current message 是短確認、短否定或短可行性答覆，且沒有新的任務內容、替代時間或替代條件時，才使用 acknowledge；若 Current message 是具體的狀態通知、閒聊、環境回報或不改變任務內容的資訊，且沒有明確承接最近提案或待確認事項，必須輸出 no_action，不可把它當成 acknowledge。E. 只有 A/B/C/D 都不成立時，才考慮 no_action。\n")
	builder.WriteString("在判斷欄位是否完整之前，必須先執行 dialogue target selection：若沒有 Explicit reply/quote target，Latest prior conversation message 是 Current message 的相鄰回覆目標候選；若 Current message 是短確認、短否定或短可行性答覆，且 Latest prior conversation message 是提案、詢問、改期請求或待確認事項，decision 應為 acknowledge，linked_message_id 指向 Latest prior conversation message 的 id，summary/assignees/due_text 從該訊息與同任務上下文承接，missing_fields=[]。\n")
	builder.WriteString("若 Latest prior conversation message 本身是在修改更早的待辦時間/條件，Current message 的短確認是在確認該修改提案；此時仍以 acknowledge 連到 Latest prior conversation message，而不是連到更舊的 Todo candidate context。Todo candidate contexts 只能用來補充同任務狀態，不能覆蓋相鄰對話目標。\n")
	builder.WriteString("只有在 target selection 判定 Current message 不是任何提案、詢問、待確認事項或既有待辦的語用回覆時，才可以因 Current message 本身缺少任務目標或欄位而輸出 no_action。\n")
	builder.WriteString("輸出前必須依序套用 decision precedence：1. Explicit reply/quote target；2. Todo candidate contexts 中同一任務的補充、限制、負責人、期限或交付格式；3. Current message 本身與既有 context 明顯不同的新任務內容或替代安排；4. Latest prior conversation message 的短確認/短否定/可行性答覆；5. no_action。若前面任一層成立，不可再因 Current message 本身很短或缺欄位而改成 no_action。\n")
	builder.WriteString("acknowledge 是 linked decision：只有在能從 Explicit reply/quote target 或 Context messages 中選出被承接的訊息 id 時才能使用；若無法選出有效 linked_message_id，必須改用 no_action 或 needs_more_info。\n")
	builder.WriteString("若 Current message 本身資訊很短，請先判斷它在對話中是否是對最近提案、詢問或待確認事項的語用回覆；若是，輸出 acknowledge 並把 linked_message_id 指向被回覆的提案訊息；不可只因為短訊息本身缺 summary/due_text 就判 no_action。\n")
	builder.WriteString("acknowledge 不等於任何無關訊息都能承接既有 Todo。Current message 必須是語用上正在回答、確認、同意、否定或接受某個明確提案/待確認更新；若它只是另一個話題的狀態通知或閒聊，即使 Context messages 裡有 active candidate，也必須 no_action。\n")
	builder.WriteString("對極短確認或否定回覆，請優先依時間相鄰性、發話者輪替、上一則提案/詢問、以及 Todo candidate contexts 的待確認狀態判斷語用功能；不要讓語意上無待確認功能的短狀態訊息只因 rerank 排名較前就成為 linked target，也不可因 Current message 字數很少就忽略最近提案。\n")
	builder.WriteString("若極短確認/否定的上一則相鄰訊息本身是提案、詢問或改期請求，即使該上一則尚未出現在 Todo candidate contexts，也應優先考慮把 linked_message_id 指向該上一則 Context message；不要為了使用既有 candidate context 而連到更舊、語用功能不同的訊息。\n")
	builder.WriteString("提醒、請記得、到時提醒我、不要忘記、幫我記一下等日常提醒語氣，只要包含可追蹤事項、承諾、交付、時間或對象，就屬於 Todo candidate；不可只因為語氣日常就判 no_action。\n")
	builder.WriteString("summary 是整理後的待辦內容；due_text 保留使用者原本的時間字面，不要自行正規化日期；assignees 保留訊息中可見的人名或稱呼。\n")
	builder.WriteString("欄位型別必須固定：schema_version/decision/linked_message_id/summary/due_text/reason 是 string，confidence 是 number，assignees 與 missing_fields 永遠是 string array；沒有人名或缺漏欄位時輸出 []，不可輸出字串、物件或 null。\n")
	builder.WriteString("JSON key 必須只使用 schema_version、decision、linked_message_id、summary、assignees、due_text、confidence、missing_fields、reason 這 9 個欄位名稱；不可把 JSON 片段、跳脫字元或 key/value 片段放進欄位名稱，例如不可輸出 due_text\\\":\\\"\\\" 這類壞 key。\n")
	builder.WriteString("needs_more_info 只適用於 Current message 已可判定為可追蹤待辦或對既有待辦的承接，但缺少必要欄位而需要使用者補充；若 Current message 本身不是可追蹤待辦、只是聊天、狀態描述、個人行程通知、無需系統追蹤的答覆或無法形成待辦意圖，decision 必須是 no_action，而不是 needs_more_info。\n")
	builder.WriteString("decision 欄位規則：create_candidate 需要 linked_message_id=\"\"、summary 非空、missing_fields=[]；update_candidate/acknowledge/cancel_candidate 需要 linked_message_id 來自訊息 id 清單、summary 非空、missing_fields=[]；needs_more_info 需要 summary 非空且 missing_fields 非空；no_action 需要 linked_message_id=\"\"、summary=\"\"、assignees=[]、due_text=\"\"、missing_fields=[]。所有 decision 的 reason 都必須是非空字串。\n")
	builder.WriteString("對 update_candidate/acknowledge/cancel_candidate，summary、assignees、due_text 可承接 linked message 與 Todo candidate contexts 中已知且未被 Current message 改變的資訊；若同一任務已可由上下文辨識，不可把這些已知欄位列入 missing_fields，missing_fields 必須是 []。\n")
	builder.WriteString("Todo candidate contexts 代表同 channel 仍活躍的既有候選待辦狀態。若 Current message 的任務目標、交付物、負責人、期限或限制條件與某個 candidate context 的 known_summary/known_assignees/known_due_text 指向同一件事，即使中間隔了許多非待辦聊天，也應輸出 update_candidate 或 acknowledge，linked_message_id 指向該 candidate 的 source_message_id 或 last_message_id；不可重新輸出 create_candidate。\n")
	builder.WriteString("只有 Current message 引入與所有 Todo candidate contexts 明顯不同的獨立任務目標時，才可以使用 create_candidate。若只是補充既有任務的摘要、追蹤項目、優先順序、負責人、期限、範圍或交付格式，必須視為既有 candidate 的 update_candidate。\n")
	builder.WriteString("若 Current message 使用指回既有事項的語氣，例如這件事、那件事、那個待辦、這個期限、上述任務、前面那件、剛剛說的，並補充負責人、期限、範圍、交付格式或確認內容，必須先在 Todo candidate contexts 與 Context messages 中尋找同一任務；找到時輸出 update_candidate 或 acknowledge，不可因 Current message 包含可追蹤欄位就重新 create_candidate。\n")
	builder.WriteString("decision=no_action 的欄位表必須固定為 linked_message_id=\"\"、summary=\"\"、assignees=[]、due_text=\"\"、missing_fields=[]；即使原因是缺少任務目標、時間、對象、交付內容或 action intent，也只能把原因寫進 reason，missing_fields 仍必須是 []。\n")
	builder.WriteString("missing_fields 只有 decision=needs_more_info 時可以非空；若 decision=no_action，請把不可形成待辦的原因寫在 reason，不可把缺少的欄位放進 missing_fields，不可輸出 [\"reason\"]、[\"time\"]、[\"task_goal\"] 或任何其他 missing_fields。\n")
	builder.WriteString("no_action 合法 JSON 範例只適用於 target selection 已判定 Current message 完全沒有承接任何提案、詢問、待確認事項或既有待辦時：{\"schema_version\":\"v1\",\"decision\":\"no_action\",\"linked_message_id\":\"\",\"summary\":\"\",\"assignees\":[],\"due_text\":\"\",\"confidence\":0.0,\"missing_fields\":[],\"reason\":\"訊息不構成可追蹤待辦\"}\n")
	builder.WriteString("Current message 本身包含任務內容/交付/時間安排時，若無法對應任何 Todo candidate context，create_candidate 合法 JSON 範例優先於 adjacency acknowledge：{\"schema_version\":\"v1\",\"decision\":\"create_candidate\",\"linked_message_id\":\"\",\"summary\":\"整理並回報前文事項的原因\",\"assignees\":[],\"due_text\":\"明天\",\"confidence\":0.8,\"missing_fields\":[],\"reason\":\"Current message 本身提出新的可追蹤交付內容與時間安排，且不同於既有 candidate context\"}\n")
	builder.WriteString("個人可用性或行程衝突承接既有任務並提出替代安排時的合法 JSON 範例優先於 no_action：{\"schema_version\":\"v1\",\"decision\":\"update_candidate\",\"linked_message_id\":\"<message_id_from_context>\",\"summary\":\"承接既有任務並改為替代時間或條件執行\",\"assignees\":[],\"due_text\":\"替代時間字面\",\"confidence\":0.7,\"missing_fields\":[],\"reason\":\"Current message 是對既有任務的可用性/替代安排回覆\"}\n")
	builder.WriteString("短確認承接 Latest prior conversation message 的合法 JSON 範例優先於 no_action 範例；linked_message_id 必須替換為 Latest prior conversation message 的 id，summary/due_text 可從該訊息與同任務上下文承接：{\"schema_version\":\"v1\",\"decision\":\"acknowledge\",\"linked_message_id\":\"<latest_prior_message_id>\",\"summary\":\"承接最近提案或待確認事項\",\"assignees\":[],\"due_text\":\"\",\"confidence\":0.7,\"missing_fields\":[],\"reason\":\"Current message 是對 Latest prior conversation message 的短確認或可行性答覆\"}\n")
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
	builder.WriteString("\nConversation messages in chronological order:\n")
	if len(conversationMessages) == 0 {
		builder.WriteString("none\n")
	} else {
		for index, item := range conversationMessages {
			if item == nil {
				continue
			}
			builder.WriteString(fmt.Sprintf("%d. id=%s sender=%s text=%q\n", index+1, item.ID.String(), strings.TrimSpace(item.SenderName), strings.TrimSpace(item.Content)))
		}
	}
	builder.WriteString("\nLatest prior conversation message:\n")
	latestPriorMessage := latestNonNilMessage(conversationMessages)
	if latestPriorMessage == nil {
		builder.WriteString("none\n")
	} else {
		builder.WriteString(fmt.Sprintf("id=%s sender=%s text=%q\n", latestPriorMessage.ID.String(), strings.TrimSpace(latestPriorMessage.SenderName), strings.TrimSpace(latestPriorMessage.Content)))
		builder.WriteString("Use this as the first adjacency target candidate for short confirmations, denials, feasibility answers, and acknowledgements.\n")
	}
	builder.WriteString("\nTodo candidate contexts:\n")
	if len(candidateContexts) == 0 {
		builder.WriteString("none\n")
	} else {
		for index, candidate := range candidateContexts {
			if candidate == nil {
				continue
			}
			builder.WriteString(fmt.Sprintf("%d. source_message_id=%s last_message_id=%s status=%s last_decision=%s missing_fields=%v known_summary=%q known_assignees=%v known_due_text=%q\n",
				index+1,
				candidate.SourceMessageID.String(),
				candidate.LastMessageID.String(),
				candidate.Status,
				candidate.LastDecision,
				candidate.MissingFields,
				strings.TrimSpace(candidate.Summary),
				normalizeTodoPromptStringSlice(candidate.Assignees),
				strings.TrimSpace(candidate.DueText),
			))
		}
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

func normalizeTodoPromptStringSlice(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	items := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		items = append(items, value)
	}
	return items
}

func buildTodoDueTimePrompt(messageCtx MessageContext, analysis *llminteraction.TodoAnalysis, referenceTime time.Time, timezone string) string {
	return strings.TrimSpace(fmt.Sprintf(`你是 Todo Reminder 的時間正規化器。請把 due_text 依 reference_time 與 timezone 轉成 RFC3339 due_at。
只能輸出 todo_due_time JSON contract：schema_version, decision, due_at, timezone, precision, confidence, missing_fields, reason。
輸出必須是一個完整 JSON object，所有欄位名稱必須用雙引號包住，欄位之間必須用逗號分隔。

規則：
- decision 只能是 normalized、needs_more_info、no_due_time。
- precision 只能是 datetime、date、relative_window、unknown；不可輸出 time、hour、minute 或其他值。
- normalized 時 due_at 必須是 RFC3339，例如 2026-07-20T09:00:00+08:00，timezone 必須使用輸入 timezone。
- 若 due_text 同時包含日期與明確時間，precision 必須使用 datetime。
- 相對日期必須以 reference_time 在輸入 timezone 的本地日曆日換算；例如 due_text=明天 代表 reference_time 日期加一天，不可使用模型執行當下日期或伺服器現在時間。
- 若 due_text 只有日期沒有時間，precision 使用 date，due_at 可用該日期 09:00:00 作為候選提醒時間，但 reason 必須說明時間是預設候選。
- 若 due_text 太模糊而無法安全排程，decision 使用 needs_more_info，due_at 使用空字串，timezone 使用輸入 timezone，precision 使用 unknown，confidence 使用 0 到 0.5，並填 missing_fields。
- no_due_time 時 due_at 使用空字串，timezone 使用輸入 timezone，precision 使用 unknown，confidence 使用 0 到 0.5。
- reason 是必填非空字串；normalized 時說明如何依 reference_time/timezone 換算，needs_more_info/no_due_time 時說明無法正規化的原因。
- 不可輸出 null；沒有值時使用空字串、空陣列或 precision=unknown。
- 不可輸出額外欄位，不可用自然語言包住 JSON。

輸出範例：{"schema_version":"v1","decision":"needs_more_info","due_at":"","timezone":"Asia/Taipei","precision":"unknown","confidence":0.4,"missing_fields":["time"],"reason":"缺少明確時間"}

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
