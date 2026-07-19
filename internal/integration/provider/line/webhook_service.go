package line

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"assistant-api/internal/config"
	realtimeclient "assistant-api/internal/integration/provider/realtime"
	webhooklog "assistant-api/internal/integration/provider/webhooklog"
	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"
	"assistant-api/internal/usecase/actionpost"
	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"
	"assistant-api/internal/usecase/ai/topkfilter"
	"assistant-api/internal/usecase/inbound/commandchain"
	"assistant-api/internal/usecase/inbound/commanddecision"
	"assistant-api/internal/usecase/inbound/conversationflow"
	"assistant-api/internal/usecase/inbound/messagepersist"
	"assistant-api/internal/usecase/inbound/messagepipeline"
	"assistant-api/internal/usecase/inbound/realtime"

	"github.com/google/uuid"
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
	repo                  *repository.ChannelMessageRepo
	lineClient            *messaging_api.MessagingApiAPI
	decisionService       commanddecision.Service
	llmInteractionService llminteraction.InteractionService
	persistenceService    messagepersist.Service
	topKFilterService     topkfilter.Service
	actionPostDispatcher  *actionpost.Dispatcher
	followUpSender        PushMessageService
	nonCommandDispatcher  *realtime.Dispatcher
	commandFlow           *conversationflow.Orchestrator
	messagePipeline       *messagepipeline.Handler
}

// WebhookServiceOptions 提供 webhook service 的擴充設定。
// 目前主要預留 member name cache 與 TTL，方便後續替換 Redis。
type WebhookServiceOptions struct {
	MemberNameCache MemberNameCache
	MemberNameTTL   time.Duration
	LLMInteraction  llminteraction.InteractionService
	TopKFilter      topkfilter.Service
	FollowUpSender  PushMessageService
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
	dispatcher := actionpost.NewDefaultDispatcher(repo)
	translateClient, translateProfile, err := realtimeclient.BuildTranslatorFromConfig(config.AI, config.LLMProviders)
	if err != nil {
		panic(err)
	}
	classifierClient, classifierProfile, err := realtimeclient.BuildClassifierFromConfig(config.AI)
	if err != nil {
		panic(err)
	}
	// 這裡把翻譯能力包成共用 realtime service，而不是直接寫在 LINE webhook 裡。
	// 這樣 Slack 也可以用同一套「非指令訊息 -> 翻譯 side-effect」流程，
	// 差別只剩底層 sender 與平台使用者識別來源。
	autoTranslate := realtime.NewAutoTranslateService(realtime.AutoTranslateServiceOptions{
		Repo:       repo,
		Sender:     lineRealtimeSender{sender: options.FollowUpSender},
		Translator: translateClient,
		// 這裡用平台中立的 user id 來反查內部 owner，
		// 讓翻譯流程不需要知道來源是 LINE 還是 Slack。
		ResolveOwnerUserID: func(ctx context.Context, platformUserID string) (uuid.UUID, error) {
			return repo.ResolveUserIDByPlatformUserID(ctx, platformUserID)
		},
		BotSenderID:   strings.TrimSpace(config.Line.BotUserID),
		PlatformLabel: "line:" + strings.TrimSpace(translateProfile),
	})
	todoReminder := realtime.NewTodoReminderService(realtime.TodoReminderServiceOptions{PlatformLabel: "line"})
	messageClassifier := realtime.NewMessageClassificationService(realtime.MessageClassificationServiceOptions{
		TextScanGate:  repo,
		Classifier:    classifierClient,
		Handlers:      []realtime.ClassificationHandler{todoReminder},
		PlatformLabel: "line:" + strings.TrimSpace(classifierProfile),
	})
	nonCommandDispatcher := realtime.NewDispatcher(autoTranslate, messageClassifier)
	// 非指令訊息允許多個 realtime service 同時處理。
	// 翻譯會主動送出譯文；分類只產生 tag 並交給後續 handler，不阻斷彼此。
	flow := conversationflow.NewFromFactory(conversationflow.FactoryOptions{
		PlatformLabel:               "line",
		BotSenderID:                 strings.TrimSpace(config.Line.BotUserID),
		SuccessText:                 "指令已執行成功",
		CommandConfidenceThreshold:  config.AI.LLMInteraction.CommandConfidenceThreshold,
		QuestionConfidenceThreshold: config.AI.LLMInteraction.QuestionConfidenceThreshold,
		DecisionJSONRetryCount:      config.AI.LLMInteraction.DecisionJSONRetryCount,
		Repo:                        repo,
		TopKFilter:                  options.TopKFilter,
		LLM:                         options.LLMInteraction,
		Dispatcher:                  dispatcher,
		Messenger:                   lineOutboundMessenger{sender: options.FollowUpSender},
	})
	pipeline := &messagepipeline.Handler{
		PlatformLabel:        "line",
		Persistence:          persistSvc,
		Decision:             decisionSvc,
		NonCommandDispatcher: nonCommandDispatcher,
		CommandFlow:          flow,
	}
	return &WebhookService{
		repo:                  repo,
		lineClient:            lineClient,
		decisionService:       decisionSvc,
		llmInteractionService: options.LLMInteraction,
		persistenceService:    persistSvc,
		topKFilterService:     options.TopKFilter,
		actionPostDispatcher:  dispatcher,
		followUpSender:        options.FollowUpSender,
		nonCommandDispatcher:  nonCommandDispatcher,
		commandFlow:           flow,
		messagePipeline:       pipeline,
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
	// ReplyToken 為可用於回覆該事件訊息的 token。
	ReplyToken string `json:"replyToken"`
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
	// QuoteToken 為可引用此訊息的 token。
	QuoteToken string `json:"quoteToken"`
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

const lineUnnamedChannelLabel = "(No Name)"

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
		if handled, err := s.handleChannelLifecycleEvent(context.Background(), event); handled {
			if err != nil {
				zap.L().Error("line channel lifecycle event failed",
					zap.String("event_type", strings.TrimSpace(event.Type)),
					zap.String("source_type", strings.TrimSpace(event.Source.Type)),
					zap.String("source_group_id", strings.TrimSpace(event.Source.GroupID)),
					zap.String("source_room_id", strings.TrimSpace(event.Source.RoomID)),
					zap.Error(err),
				)
			}
			continue
		}

		// 先把收到的原始事件資訊印出來，避免後續轉換或 command gate 把訊息吃掉。
		webhooklog.LogIncomingMessage(webhooklog.IncomingMessage{
			Provider:      "line",
			EventType:     event.Type,
			SourceType:    event.Source.Type,
			SourceUserID:  event.Source.UserID,
			SourceGroupID: event.Source.GroupID,
			SourceRoomID:  event.Source.RoomID,
			MessageID:     event.Message.ID,
			Text:          event.Message.Text,
		})

		// 非 message 事件直接略過；只有文字/圖片等訊息才需要進一步處理。
		if strings.TrimSpace(event.Type) != "message" {
			continue
		}

		message, ok, reason := adaptLineEventToUnified(event)
		if !ok {
			webhooklog.LogUnifiedConversionSkipped(webhooklog.UnifiedConversionSkipped{
				Provider:      "line",
				EventType:     event.Type,
				SourceType:    event.Source.Type,
				SourceUserID:  event.Source.UserID,
				SourceGroupID: event.Source.GroupID,
				SourceRoomID:  event.Source.RoomID,
				MessageID:     event.Message.ID,
				Reason:        reason,
			})
			continue
		}

		if resolvedChannelName, err := s.resolveLineChannelDisplayName(context.Background(), event, message); err == nil {
			message.ChannelName = resolvedChannelName
		}

		if s.messagePipeline == nil {
			continue
		}
		s.messagePipeline.Process(messagepipeline.Input{
			Context:        context.Background(),
			Message:        message,
			BotUserID:      config.Line.BotUserID,
			PlatformUserID: strings.TrimSpace(event.Source.UserID),
			ReplyRef:       strings.TrimSpace(event.ReplyToken),
			QuoteRef:       strings.TrimSpace(event.Message.QuoteToken),
		})
	}

}

// handleChannelLifecycleEvent handles LINE bot join/leave lifecycle events.
//
// 規則：
// - join（bot 被邀請）時建立/啟用 group channel
// - leave（bot 離開）時停用 channel（is_active=false）
func (s *WebhookService) handleChannelLifecycleEvent(ctx context.Context, event webhookEvent) (bool, error) {
	if s == nil || s.repo == nil {
		return false, nil
	}
	eventType := strings.ToLower(strings.TrimSpace(event.Type))
	sourceType := strings.ToLower(strings.TrimSpace(event.Source.Type))

	// LINE 的 channel 生命周期只處理 group/room；user 私聊不走邀請/離開語意。
	if sourceType != "group" && sourceType != "room" {
		return false, nil
	}

	channelID, channelType := resolveChannelIdentity(event.Source)
	if channelID == "" || !strings.EqualFold(channelType, "group") {
		return true, nil
	}

	switch eventType {
	case "join":
		channelName, err := s.resolveLineJoinChannelName(ctx, event)
		if err != nil {
			return true, err
		}
		if _, err := s.repo.GetOrCreateChannel(ctx, "line", channelID, "group", channelName); err != nil {
			return true, err
		}
		if err := s.repo.SetChannelActiveByPlatformGroupID(ctx, "line", channelID, true); err != nil {
			return true, err
		}
		return true, nil
	case "leave":
		if err := s.repo.SetChannelActiveByPlatformGroupID(ctx, "line", channelID, false); err != nil {
			return true, err
		}
		return true, nil
	default:
		return false, nil
	}
}

func (s *WebhookService) resolveLineJoinChannelName(ctx context.Context, event webhookEvent) (string, error) {
	if s == nil || s.lineClient == nil {
		return lineUnnamedChannelLabel, nil
	}
	sourceType := strings.ToLower(strings.TrimSpace(event.Source.Type))
	api := s.lineClient.WithContext(ctx)

	if sourceType == "group" {
		groupID := strings.TrimSpace(event.Source.GroupID)
		if groupID == "" {
			return lineUnnamedChannelLabel, nil
		}
		summary, err := api.GetGroupSummary(groupID)
		if err != nil || summary == nil {
			return lineUnnamedChannelLabel, nil
		}
		name := strings.TrimSpace(summary.GroupName)
		if name == "" {
			return lineUnnamedChannelLabel, nil
		}
		return name, nil
	}

	// LINE room 事件沒有可用的 room 名稱查詢 API，依需求使用固定名稱。
	if sourceType == "room" {
		return lineUnnamedChannelLabel, nil
	}
	return lineUnnamedChannelLabel, nil
}

func (s *WebhookService) resolveLineChannelDisplayName(ctx context.Context, event webhookEvent, message *unifiedmessage.Message) (string, error) {
	if s == nil || s.lineClient == nil || message == nil {
		return "", nil
	}
	api := s.lineClient.WithContext(ctx)
	channelType := strings.ToLower(strings.TrimSpace(message.ChannelType))
	channelID := strings.TrimSpace(message.ChannelID)
	if channelID == "" {
		return "", nil
	}

	switch channelType {
	case "private":
		userID := strings.TrimSpace(event.Source.UserID)
		if userID == "" {
			userID = strings.TrimSpace(message.SenderID)
		}
		if userID == "" {
			return "", nil
		}
		profile, err := api.GetProfile(userID)
		if err != nil || profile == nil {
			return "", err
		}
		return strings.TrimSpace(profile.DisplayName), nil
	case "group":
		if strings.TrimSpace(event.Source.GroupID) == "" {
			return "", nil
		}
		summary, err := api.GetGroupSummary(strings.TrimSpace(event.Source.GroupID))
		if err != nil || summary == nil {
			return "", err
		}
		return strings.TrimSpace(summary.GroupName), nil
	default:
		return "", nil
	}
}

// lineRealtimeSender 將 LINE 的送訊息能力包成共用 realtime sender 介面。
//
// 讓 auto-translate 只依賴 SendText 介面，不需要關心實際是 LINE SDK、Slack SDK，
// 或其他未來平台的發送實作。
type lineRealtimeSender struct {
	sender PushMessageService
}

func (m lineRealtimeSender) SendText(ctx context.Context, chatID string, userID string, text string, replyRef string, quoteRef string) (string, error) {
	if m.sender == nil {
		return "", nil
	}
	return m.sender.SendTextToChat(ctx, chatID, userID, text, replyRef, quoteRef)
}

type lineOutboundMessenger struct {
	sender PushMessageService
}

func (m lineOutboundMessenger) SendText(ctx context.Context, chatID string, userID string, text string, replyRef string, quoteRef string) (string, error) {
	if m.sender == nil {
		return "", nil
	}
	return m.sender.SendTextToChat(ctx, chatID, userID, text, replyRef, quoteRef)
}

func (s *WebhookService) ensureCommandFlow() {
	if s == nil || s.commandFlow != nil {
		return
	}
	dispatcher := s.actionPostDispatcher
	if dispatcher == nil {
		dispatcher = actionpost.NewDefaultDispatcher(s.repo)
		s.actionPostDispatcher = dispatcher
	}
	s.commandFlow = conversationflow.NewFromFactory(conversationflow.FactoryOptions{
		PlatformLabel:               "line",
		BotSenderID:                 strings.TrimSpace(config.Line.BotUserID),
		SuccessText:                 "指令已執行成功",
		CommandConfidenceThreshold:  config.AI.LLMInteraction.CommandConfidenceThreshold,
		QuestionConfidenceThreshold: config.AI.LLMInteraction.QuestionConfidenceThreshold,
		DecisionJSONRetryCount:      config.AI.LLMInteraction.DecisionJSONRetryCount,
		Repo:                        s.repo,
		TopKFilter:                  s.topKFilterService,
		LLM:                         s.llmInteractionService,
		Dispatcher:                  dispatcher,
		Messenger:                   lineOutboundMessenger{sender: s.followUpSender},
	})
}

// toActionCandidates 把 topkfilter 的 reranked 候選轉成 llm_interaction 可用的文字描述，
// 刻意在這裡做轉換，避免 llm_interaction 直接依賴 topkfilter/ranking 內部型別。
func toActionCandidates(candidates []topkfilter.ScoredCandidate) []llminteraction.ActionCandidate {
	out := make([]llminteraction.ActionCandidate, 0, len(candidates))
	for _, item := range candidates {
		// 只抽出最終互動判斷需要的欄位，避免把底層資料結構耦合到 llm_interaction。
		out = append(out, llminteraction.ActionCandidate{
			Operation: item.Candidate.APIOperation,
			SkillCode: item.Candidate.SkillCode,
			RouteText: item.Candidate.RouteText,
			Prompt:    item.Candidate.Prompt,
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

func (r lineSenderNameResolver) ResolveSenderName(ctx context.Context, platform string, platformTenantID string, channelID string, channelType string, senderID string) (string, error) {
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
