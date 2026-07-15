package line

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"
	"assistant-api/internal/usecase/actionpost"
	"assistant-api/internal/usecase/ai/semanticdecision"
	"assistant-api/internal/usecase/ai/topkfilter"
	"assistant-api/internal/usecase/inbound/commandchain"
	"assistant-api/internal/usecase/inbound/commanddecision"
	"assistant-api/internal/usecase/inbound/messagepersist"

	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
	"go.uber.org/zap"
)

// WebhookProcessor 定義 LINE webhook 的處理介面，方便注入不同實作。
type WebhookProcessor interface {
	// ProcessIncoming 接收原始 webhook body 與簽章字串，執行後續處理。
	// 目前預設實作只做解析與 console 輸出，未做簽章驗證與持久化。
	ProcessIncoming(body []byte, signature string)
}

// WebhookService 是最小可用的預設實作：
// 僅解析事件並輸出到 console，便於開發階段觀察 webhook 是否正常進站。
type WebhookService struct {
	repo                 *repository.ChannelMessageRepo
	decisionService      commanddecision.Service
	semanticService      semanticdecision.Service
	persistenceService   messagepersist.Service
	topKFilterService    topkfilter.Service
	actionPostDispatcher *actionpost.Dispatcher
}

// WebhookServiceOptions 提供 webhook service 的擴充設定。
// 目前主要預留 member name cache 與 TTL，方便後續替換 Redis。
type WebhookServiceOptions struct {
	MemberNameCache  MemberNameCache
	MemberNameTTL    time.Duration
	SemanticDecision semanticdecision.Service
	TopKFilter       topkfilter.Service
}

// NewWebhookService 建立預設 webhook service
func NewWebhookService(repo *repository.ChannelMessageRepo) WebhookProcessor {
	return NewWebhookServiceWithOptions(repo, WebhookServiceOptions{})
}

// NewWebhookServiceWithOptions 建立可帶擴充選項的 webhook service。
func NewWebhookServiceWithOptions(repo *repository.ChannelMessageRepo, options WebhookServiceOptions) WebhookProcessor {
	var lineClient *messaging_api.MessagingApiAPI
	if token := strings.TrimSpace(config.Line.ChannelToken); token != "" {
		if client, err := messaging_api.NewMessagingApiAPI(token); err == nil {
			lineClient = client
		}
	}
	cache := options.MemberNameCache
	if cache == nil {
		// 目前預設為 Noop：不會真正存取任何快取後端（非 memory/DB/Redis）。
		cache = NoopMemberNameCache{}
	}
	memberNameTTL := options.MemberNameTTL
	if memberNameTTL <= 0 {
		// sender 顯示名稱快取預設 10 分鐘，避免每則訊息都打 LINE profile API。
		memberNameTTL = 10 * time.Minute
	}
	// sender 名稱解析流程：cache -> 綁定資料表 -> LINE API，最後再回寫 cache。
	persistSvc := messagepersist.NewService(repo, lineSenderNameResolver{repo: repo, client: lineClient, cache: cache, memberNameTTL: memberNameTTL, now: time.Now})
	chainSvc := commandchain.NewService(repo)
	decisionSvc := commanddecision.NewService(chainSvc)
	return &WebhookService{
		repo:                 repo,
		decisionService:      decisionSvc,
		semanticService:      options.SemanticDecision,
		persistenceService:   persistSvc,
		topKFilterService:    options.TopKFilter,
		actionPostDispatcher: actionpost.NewDefaultDispatcher(repo),
	}
}

// webhookRequest 對應 LINE webhook 最上層 payload。
type webhookRequest struct {
	// Events 為 LINE 一次 webhook payload 內包含的事件陣列。
	Events []webhookEvent `json:"events"`
}

// webhookEvent 對應 events[] 內單一事件。
type webhookEvent struct {
	// Type 事件類型，例如 message、follow、unfollow 等。
	Type string `json:"type"`
	// Source 訊息來源（個人、群組、聊天室）資訊。
	Source webhookEventSource `json:"source"`
	// Message 僅在 message 事件時有意義；其他事件可能為零值。
	Message webhookMessage `json:"message"`
	// Timestamp 為 LINE 事件時間戳 (unix milliseconds)。
	Timestamp int64 `json:"timestamp"`
}

// webhookEventSource 描述事件來源身分（私聊/群組/聊天室）。
type webhookEventSource struct {
	// Type 來源型別：user/group/room。
	Type string `json:"type"`
	// UserID 為一對一聊天來源的使用者 ID。
	UserID string `json:"userId"`
	// GroupID 為群組聊天來源 ID。
	GroupID string `json:"groupId"`
	// RoomID 為多人聊天室來源 ID。
	RoomID string `json:"roomId"`
}

// webhookMessage 為 message 事件的訊息主體。
type webhookMessage struct {
	// ID 為 LINE message ID，可用於後續追蹤或回覆流程。
	ID string `json:"id"`
	// Type 訊息型別，例如 text、image、audio。
	Type string `json:"type"`
	// Text 僅在 text 訊息時有內容。
	Text string `json:"text"`
	// QuotedMessageID 為被引用訊息的 ID。
	QuotedMessageID string `json:"quotedMessageId"`
	// Mention 為 LINE mention 資訊。
	Mention *webhookMessageMention `json:"mention,omitempty"`
}

type webhookMessageMention struct {
	// Mentionees 為被 mention 的使用者清單。
	Mentionees []webhookMentionee `json:"mentionees"`
}

type webhookMentionee struct {
	// Index 為 mention 在文字中的位置。
	Index int `json:"index"`
	// Length 為 mention 片段長度。
	Length int `json:"length"`
	// UserID 為被 mention 的使用者 ID。
	UserID string `json:"userId"`
}

// ProcessIncoming 負責 LINE webhook 的整體流程編排。
// 它會先解析原始 payload，再對每則 message 事件做三件事：
// 1. 先把訊息本體印到 console，方便除錯與觀察。
// 2. 先將訊息寫入資料庫，確保訊息可即時查詢與追蹤。
// 3. 最後再交給 AI client 做延伸判讀，避免阻塞落庫。
func (s *WebhookService) ProcessIncoming(body []byte, signature string) {
	var req webhookRequest
	// 第一段：先將 webhook body 轉成結構化資料。
	// 如果解析失敗，只記錄錯誤並直接返回，避免 webhook ACK 被卡住。
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			zap.L().Error("line webhook parse failed",
				zap.Bool("signature_present", signature != ""),
				zap.Int("body_bytes", len(body)),
				zap.Error(err),
			)
			return
		}
	}

	// 第二段：逐筆掃描事件陣列，只處理 message 事件。
	for _, event := range req.Events {
		// 先把收到的原始事件資訊印出來，避免後續轉換或 command gate 把訊息吃掉。
		zap.L().Info("line message received",
			zap.String("event_type", strings.TrimSpace(event.Type)),
			zap.String("source_type", strings.TrimSpace(event.Source.Type)),
			zap.String("source_user_id", strings.TrimSpace(event.Source.UserID)),
			zap.String("source_group_id", strings.TrimSpace(event.Source.GroupID)),
			zap.String("source_room_id", strings.TrimSpace(event.Source.RoomID)),
			zap.String("message_id", strings.TrimSpace(event.Message.ID)),
			zap.String("text", strings.TrimSpace(event.Message.Text)),
		)

		// 非 message 事件直接略過；只有文字/圖片等訊息才需要進一步處理。
		if strings.TrimSpace(event.Type) != "message" {
			continue
		}

		message, ok, reason := adaptLineEventToUnified(event)
		if !ok {
			zap.L().Debug("line message unified conversion skipped",
				zap.String("event_type", strings.TrimSpace(event.Type)),
				zap.String("source_type", strings.TrimSpace(event.Source.Type)),
				zap.String("source_user_id", strings.TrimSpace(event.Source.UserID)),
				zap.String("source_group_id", strings.TrimSpace(event.Source.GroupID)),
				zap.String("source_room_id", strings.TrimSpace(event.Source.RoomID)),
				zap.String("message_id", strings.TrimSpace(event.Message.ID)),
				zap.String("reason", reason),
			)
			continue
		}

		// 先落庫，確保訊息資料優先可用，不受後續 AI 延遲影響。
		savedMessage := s.persistUnifiedMessage(message)
		decision := &commanddecision.Decision{IsMentionedBot: message.MentionsUser(config.Line.BotUserID)}
		if strings.EqualFold(strings.TrimSpace(message.ChannelType), "private") {
			decision.IsPrivateChannel = true
		}
		decision.IsEffectiveMentionedBot = decision.IsMentionedBot
		if s.decisionService != nil {
			decision = s.decisionService.DecideMessage(context.Background(), message, savedMessage, config.Line.BotUserID)
			if decision != nil && strings.EqualFold(strings.TrimSpace(message.ChannelType), "private") {
				decision.IsPrivateChannel = true
			}
		}

		if !decision.IsCommand() {
			// 嚴格 command gate：非 command 訊息到此結束。
			// 目的是把 AI 成本集中在可執行意圖，避免一般閒聊流量進入 rerank/semantic 決策。
			continue
		}

		if decision.CommandChainError != nil {
			zap.L().Debug("command chain check skipped",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.Error(decision.CommandChainError),
			)
		} else if decision.IsOnCommandChain {
			zap.L().Info("command chain message",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.Bool("mentioned_bot", decision.IsMentionedBot),
				zap.Bool("effective_mentioned_bot", decision.IsEffectiveMentionedBot),
				zap.String("reply_to_msg_id", strings.TrimSpace(message.ReplyToMsgID)),
			)
		}

		if s.topKFilterService == nil {
			// 未注入 top-k/rerank 服務時，保留可觀測與落庫，但不做 AI 決策。
			continue
		}

		// 第三階段：把 reranker 精排後的候選交給語意決策模型，選出最終唯一一個 action。
		candidates := s.topKFilterService.FilterMessage(context.Background(), message)
		if len(candidates) == 0 || s.semanticService == nil {
			// 若沒有可用候選，或未注入 final semantic 服務，
			// 就停在 rerank 結果，不強行產生最終 action。
			continue
		}

		actionCandidates := toActionCandidates(candidates)
		finalDecision, err := s.semanticService.DecideFinalAction(context.Background(), message.Text, actionCandidates)
		if err != nil {
			zap.L().Warn("line message final action decision failed",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("text", strings.TrimSpace(message.Text)),
				zap.String("error_message", err.Error()),
				zap.Error(err),
			)
			continue
		}
		if finalDecision == nil {
			// 模型端允許回 nil（例如策略性略過），呼叫端視為本次不下最終 action。
			// 這裡保持靜默跳過，避免把「未決策」誤記錄成錯誤。
			continue
		}

		// 無論後續是否因低信心改走問答，先把 action decision 原始判讀結果印出來，
		// 方便觀察模型在候選 action 之間的實際選擇與信心值。
		zap.L().Info("line message action decision evaluated",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("text", strings.TrimSpace(message.Text)),
			zap.String("api_operation", finalDecision.APIOperation),
			zap.Float64("confidence", finalDecision.Confidence),
			zap.String("reason", strings.TrimSpace(finalDecision.Reason)),
		)

		// 低信心門檻：若 final semantic decision 信心值不足，
		// 判定為「使用者在問 LLM」而非可執行指令，直接停止 action 路徑。
		confidenceThreshold := config.AI.SemanticDecision.CommandConfidenceThreshold
		if confidenceThreshold > 0 && finalDecision.Confidence < confidenceThreshold {
			// 雖然挑到了某個 action，但信心值不足時仍改走問答，
			// 避免低把握度 action 造成誤觸發。
			s.routeMessageToQuestionAnswer(message, finalDecision.Confidence, confidenceThreshold, "low_action_confidence")
			continue
		}

		// 把模型選出的 api_operation 對應回候選清單，確認它真的落在候選範圍內，
		// 同時取得對應的 skill_code/route_text，讓最終結果一眼就能看出對應到哪個 action。
		matchedSkillCode := ""
		matchedRouteText := ""
		validSelection := false
		for _, candidate := range actionCandidates {
			if candidate.Operation == finalDecision.APIOperation {
				matchedSkillCode = candidate.SkillCode
				matchedRouteText = candidate.RouteText
				validSelection = true
				break
			}
		}
		if !validSelection {
			// 若模型選出不在候選清單內的 operation，代表可能發生 hallucination 或 prompt 偏移；
			// 保留告警，但仍輸出 final log 供後續排查與離線評估。
			zap.L().Warn("line message final action not in candidates",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("api_operation", finalDecision.APIOperation),
			)
		}

		zap.L().Info("line message final action decided",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("text", strings.TrimSpace(message.Text)),
			zap.String("api_operation", finalDecision.APIOperation),
			zap.String("skill_code", matchedSkillCode),
			zap.String("matched_route_text", matchedRouteText),
			zap.Bool("valid_selection", validSelection),
			zap.Float64("confidence", finalDecision.Confidence),
			zap.String("reason", finalDecision.Reason),
		)

		s.dispatchActionPostHandlers(message, strings.TrimSpace(event.Source.UserID), finalDecision, matchedSkillCode)
	}

}

// dispatchActionPostHandlers 依最終決策的 api_operation 分派對應後處理。
// 這層刻意放在主流程之外，讓新 action 只需註冊 handler，不需改 ProcessIncoming 主幹。
func (s *WebhookService) dispatchActionPostHandlers(message *unifiedmessage.Message, lineUserID string, decision *semanticdecision.ActionDecision, matchedSkillCode string) {
	if s == nil || decision == nil {
		return
	}
	if s.actionPostDispatcher == nil {
		// 保底 lazy init，確保測試直接組 struct 時仍可共用同一份 dispatcher。
		s.actionPostDispatcher = actionpost.NewDefaultDispatcher(s.repo)
	}
	s.actionPostDispatcher.Dispatch(message, lineUserID, decision, matchedSkillCode)
}

// translationTargetLocalesFromDecision 從通用 action_params 擷取翻譯語系參數。
// 兼容 target_locale（單值）與 target_locales（多值）兩種格式，
// 輸出會經過清理與去重，避免同語系重覆寫入。
func translationTargetLocalesFromDecision(decision *semanticdecision.ActionDecision) []string {
	return actionpost.ExtractTranslationTargetLocales(decision)
}

func (s *WebhookService) routeMessageToQuestionAnswer(message *unifiedmessage.Message, actionConfidence float64, actionThreshold float64, cause string) {
	if s == nil || s.semanticService == nil || message == nil {
		return
	}

	// 進入此函式代表已判定「不應直接執行 action」，
	// 改由問答模型生成自然語言回覆與 answer confidence。
	qa, qaErr := s.semanticService.AnswerQuestion(context.Background(), message.Text)
	if qaErr != nil {
		zap.L().Warn("line message semantic question answer failed",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("cause", cause),
			zap.Float64("action_confidence", actionConfidence),
			zap.Float64("action_threshold", actionThreshold),
			zap.Error(qaErr),
		)
		return
	}
	if qa == nil {
		// 上游可能在特殊策略下回 nil；這裡僅記錄，避免當成系統錯誤。
		zap.L().Info("line message semantic question answer skipped",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("cause", cause),
			zap.Float64("action_confidence", actionConfidence),
			zap.Float64("action_threshold", actionThreshold),
		)
		return
	}

	questionThreshold := config.AI.SemanticDecision.QuestionConfidenceThreshold
	// 第二道門檻：問答答案信心不足時，標記建議改送 cloud LLM。
	// 目前先記錄 recommend_cloud_llm，不在此函式直接呼叫外部 cloud provider。
	recommendCloudLLM := questionThreshold > 0 && qa.Confidence < questionThreshold
	zap.L().Info("line message semantic question answered",
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("text", strings.TrimSpace(message.Text)),
		zap.String("cause", cause),
		zap.Float64("action_confidence", actionConfidence),
		zap.Float64("action_threshold", actionThreshold),
		zap.String("answer", strings.TrimSpace(qa.Answer)),
		zap.Float64("answer_confidence", qa.Confidence),
		zap.Float64("answer_threshold", questionThreshold),
		zap.Bool("recommend_cloud_llm", recommendCloudLLM),
	)
	if recommendCloudLLM {
		zap.L().Warn("line message semantic answer suggests cloud llm fallback",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.Float64("answer_confidence", qa.Confidence),
			zap.Float64("answer_threshold", questionThreshold),
		)
	}
}

// persistUnifiedMessage 負責把統一訊息格式寫入 channel 與 channel_message。
// 這個函式只做資料持久化，不應再混入 AI 判讀或其他額外業務邏輯。
func (s *WebhookService) persistUnifiedMessage(message *unifiedmessage.Message) *ent.ChannelMessage {
	return s.persistenceService.PersistUnifiedMessage(context.Background(), message)
}

// toActionCandidates 把 topkfilter 的 reranked 候選轉成 semanticdecision 可用的文字描述，
// 刻意在這裡做轉換，避免 semanticdecision 直接依賴 topkfilter/ranking 內部型別。
func toActionCandidates(candidates []topkfilter.ScoredCandidate) []semanticdecision.ActionCandidate {
	out := make([]semanticdecision.ActionCandidate, 0, len(candidates))
	for _, item := range candidates {
		// 只抽出最終語意判斷需要的欄位，避免把底層資料結構耦合到 semanticdecision。
		out = append(out, semanticdecision.ActionCandidate{
			Operation: item.Candidate.APIOperation,
			SkillCode: item.Candidate.SkillCode,
			RouteText: item.Candidate.RouteText,
			Score:     item.Score,
		})
	}
	return out
}

type lineSenderNameResolver struct {
	repo   *repository.ChannelMessageRepo
	client *messaging_api.MessagingApiAPI
	cache  MemberNameCache
	// memberNameTTL 控制 sender 名稱快取有效期限。
	memberNameTTL time.Duration
	now           func() time.Time
}

func (r lineSenderNameResolver) ResolveSenderName(ctx context.Context, platform string, channelID string, channelType string, senderID string) (string, error) {
	if r.repo == nil {
		return "", nil
	}
	if !strings.EqualFold(strings.TrimSpace(platform), "line") {
		return "", nil
	}
	cache := r.cache
	if cache == nil {
		// 保底回退到 Noop，確保未注入快取時流程可正常運作。
		cache = NoopMemberNameCache{}
	}
	now := r.now
	if now == nil {
		now = time.Now
	}
	// TTL 判斷只在 cache 實作有實際儲存能力時才有意義；Noop 永遠 miss。
	if cachedName, expiresAt, found, err := cache.Get(ctx, platform, channelID, channelType, senderID); err == nil && found {
		if strings.TrimSpace(cachedName) != "" && now().Before(expiresAt) {
			return strings.TrimSpace(cachedName), nil
		}
	}

	name, err := r.repo.ResolveLineDisplayNameByLineUserID(ctx, senderID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(name) != "" {
		trimmed := strings.TrimSpace(name)
		_ = cache.Set(ctx, platform, channelID, channelType, senderID, trimmed, now().Add(r.memberNameTTL))
		return trimmed, nil
	}

	if r.client == nil {
		return "", nil
	}

	resolved := strings.TrimSpace(r.resolveByLineAPI(ctx, channelID, channelType, senderID))
	if resolved != "" {
		_ = cache.Set(ctx, platform, channelID, channelType, senderID, resolved, now().Add(r.memberNameTTL))
		return resolved, nil
	}
	return "", nil
}

func (r lineSenderNameResolver) resolveByLineAPI(ctx context.Context, channelID string, channelType string, senderID string) string {
	userID := strings.TrimSpace(senderID)
	if userID == "" {
		return ""
	}
	api := r.client.WithContext(ctx)
	channelKey := strings.TrimSpace(channelID)

	switch strings.ToLower(strings.TrimSpace(channelType)) {
	case "group":
		if channelKey != "" {
			if profile, err := api.GetGroupMemberProfile(channelKey, userID); err == nil && profile != nil {
				if value := strings.TrimSpace(profile.DisplayName); value != "" {
					return value
				}
			}
		}
	case "room":
		if channelKey != "" {
			if profile, err := api.GetRoomMemberProfile(channelKey, userID); err == nil && profile != nil {
				if value := strings.TrimSpace(profile.DisplayName); value != "" {
					return value
				}
			}
		}
	}

	if profile, err := api.GetProfile(userID); err == nil && profile != nil {
		return strings.TrimSpace(profile.DisplayName)
	}
	return ""
}

// resolveSender 依優先序挑選可識別的來源 ID。
// 優先 userId，再 fallback 到 groupId、roomId，最後才回傳 unknown。
func resolveSender(source webhookEventSource) string {
	// 先嘗試最精準的一對一使用者 ID。
	sender := strings.TrimSpace(source.UserID)
	if sender == "" {
		// 沒有 userId 時，退回 groupId。
		sender = strings.TrimSpace(source.GroupID)
	}
	if sender == "" {
		// 再不行就退回 roomId。
		sender = strings.TrimSpace(source.RoomID)
	}
	if sender == "" {
		// 三種來源都沒有時，統一標成 unknown。
		return "unknown"
	}
	return sender
}

// resolveChannelIdentity 根據 LINE event source 類型推導 channel key 與 channel type。
// 這裡的回傳值會拿去建立或查找對應的 channel。
func resolveChannelIdentity(source webhookEventSource) (groupID string, channelType string) {
	// 先把 source type 正規化，避免大小寫或空白影響判斷。
	sourceType := strings.TrimSpace(strings.ToLower(source.Type))
	switch sourceType {
	case "user":
		// 私聊情境直接用 userId 當 channel key。
		if userID := strings.TrimSpace(source.UserID); userID != "" {
			return userID, "private"
		}
	case "group":
		// 群組情境直接用 groupId 當 channel key。
		if groupID := strings.TrimSpace(source.GroupID); groupID != "" {
			return groupID, "group"
		}
	case "room":
		// 房間情境也視為 group 類型來處理。
		if roomID := strings.TrimSpace(source.RoomID); roomID != "" {
			return roomID, "group"
		}
	}

	// 如果 source type 不完整，就退回使用可辨識的 sender 當 channel key。
	if sender := resolveSender(source); sender != "unknown" {
		return sender, "private"
	}
	// 真的都找不到時，回傳空值讓呼叫端決定略過。
	return "", ""
}
