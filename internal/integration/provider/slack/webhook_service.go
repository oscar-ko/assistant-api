package slack

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	aillminteraction "assistant-api/internal/integration/ai/llm_interaction"
	aitopkfilter "assistant-api/internal/integration/ai/topkfilter"
	"assistant-api/internal/integration/provider/realtime"
	webhooklog "assistant-api/internal/integration/provider/webhooklog"
	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"
	"assistant-api/internal/usecase/actionpost"
	"assistant-api/internal/usecase/inbound/commandchain"
	"assistant-api/internal/usecase/inbound/commanddecision"
	"assistant-api/internal/usecase/inbound/conversationflow"
	"assistant-api/internal/usecase/inbound/messagepersist"

	"github.com/google/uuid"
)

type WebhookProcessor interface {
	ValidateSignature(timestamp string, signature string, body []byte) error
	ProcessIncoming(body []byte) (string, error)
}

type WebhookService struct {
	repo                  *repository.ChannelMessageRepo
	decisionService       commanddecision.Service
	llmInteractionService aillminteraction.InteractionService
	persistenceService    messagepersist.Service
	topKFilterService     aitopkfilter.Service
	actionPostDispatcher  *actionpost.Dispatcher
	followUpSender        PushMessageService
	nonCommandDispatcher  *realtime.Dispatcher
	commandFlow           *conversationflow.Orchestrator
}

type WebhookServiceOptions struct {
	LLMInteraction aillminteraction.InteractionService
	TopKFilter     aitopkfilter.Service
	FollowUpSender PushMessageService
}

type slackWebhookRequest struct {
	Type      string      `json:"type"`
	Token     string      `json:"token"`
	Challenge string      `json:"challenge"`
	Event     *slackEvent `json:"event"`
}

type slackEvent struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	Text        string `json:"text"`
	User        string `json:"user"`
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type"`
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
	ClientMsgID string `json:"client_msg_id"`
	BotID       string `json:"bot_id"`
}

func NewWebhookService(repo *repository.ChannelMessageRepo) WebhookProcessor {
	return NewWebhookServiceWithOptions(repo, WebhookServiceOptions{})
}

func NewWebhookServiceWithOptions(repo *repository.ChannelMessageRepo, options WebhookServiceOptions) WebhookProcessor {
	persistSvc := messagepersist.NewService(repo, NewSenderNameResolver())
	chainSvc := commandchain.NewService(repo)
	decisionSvc := commanddecision.NewService(chainSvc)
	dispatcher := actionpost.NewDefaultDispatcher(repo)
	translateClient, translateProfile, err := realtime.BuildTranslatorFromConfig(config.AI, config.LLMProviders)
	if err != nil {
		panic(err)
	}
	// Slack 也直接掛到共用 realtime 翻譯流程上，避免跟 LINE 各自維護一套非指令處理邏輯。
	// 這裡只提供 Slack 專用的 sender 與 platform user id 來源，核心翻譯行為仍由共用模組執行。
	autoTranslate := realtime.NewAutoTranslateService(realtime.AutoTranslateServiceOptions{
		Repo:       repo,
		Sender:     slackRealtimeSender{sender: options.FollowUpSender},
		Translator: translateClient,
		// 翻譯服務只看平台 user id，不關心它來自哪一種事件格式。
		ResolveOwnerUserID: func(ctx context.Context, platformUserID string) (uuid.UUID, error) {
			return repo.ResolveUserIDByPlatformUserID(ctx, platformUserID)
		},
		BotSenderID:   strings.TrimSpace(config.Slack.BotUserID),
		PlatformLabel: "slack:" + strings.TrimSpace(translateProfile),
	})
	flow := conversationflow.NewFromFactory(conversationflow.FactoryOptions{
		PlatformLabel:               "slack",
		BotSenderID:                 strings.TrimSpace(config.Slack.BotUserID),
		SuccessText:                 "指令已執行成功",
		CommandConfidenceThreshold:  config.AI.LLMInteraction.CommandConfidenceThreshold,
		QuestionConfidenceThreshold: config.AI.LLMInteraction.QuestionConfidenceThreshold,
		DecisionJSONRetryCount:      config.AI.LLMInteraction.DecisionJSONRetryCount,
		Repo:                        repo,
		TopKFilter:                  options.TopKFilter,
		LLM:                         options.LLMInteraction,
		Dispatcher:                  dispatcher,
		Messenger:                   slackOutboundMessenger{sender: options.FollowUpSender},
	})
	return &WebhookService{
		repo:                  repo,
		decisionService:       decisionSvc,
		llmInteractionService: options.LLMInteraction,
		persistenceService:    persistSvc,
		topKFilterService:     options.TopKFilter,
		actionPostDispatcher:  dispatcher,
		followUpSender:        options.FollowUpSender,
		nonCommandDispatcher:  realtime.NewDispatcher(autoTranslate),
		commandFlow:           flow,
	}
}

func (s *WebhookService) ValidateSignature(timestamp string, signature string, body []byte) error {
	signingSecret := strings.TrimSpace(config.Slack.SigningSecret)
	if signingSecret == "" {
		return fmt.Errorf("slack signing secret is empty")
	}
	timestamp = strings.TrimSpace(timestamp)
	signature = strings.TrimSpace(signature)
	if timestamp == "" || signature == "" {
		return fmt.Errorf("missing slack signature headers")
	}

	tsInt, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid slack request timestamp")
	}
	now := time.Now().Unix()
	if tsInt < now-300 || tsInt > now+300 {
		return fmt.Errorf("stale slack request timestamp")
	}

	mac := hmac.New(sha256.New, []byte(signingSecret))
	base := "v0:" + timestamp + ":" + string(body)
	if _, err := mac.Write([]byte(base)); err != nil {
		return err
	}
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return fmt.Errorf("invalid slack request signature")
	}
	return nil
}

func (s *WebhookService) ProcessIncoming(body []byte) (string, error) {
	var req slackWebhookRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return "", err
	}

	if strings.EqualFold(strings.TrimSpace(req.Type), "url_verification") {
		configuredToken := strings.TrimSpace(config.Slack.VerificationToken)
		requestToken := strings.TrimSpace(req.Token)
		if configuredToken != "" && requestToken != "" && configuredToken != requestToken {
			return "", fmt.Errorf("invalid slack verification token")
		}
		return strings.TrimSpace(req.Challenge), nil
	}

	if !strings.EqualFold(strings.TrimSpace(req.Type), "event_callback") {
		return "", nil
	}
	if req.Event == nil {
		return "", nil
	}
	if handled, err := s.handleChannelLifecycleEvent(context.Background(), req.Event); handled {
		return "", err
	}
	webhooklog.LogIncomingMessage(webhooklog.IncomingMessage{
		Provider:      "slack",
		EventType:     req.Event.Type,
		SourceType:    req.Event.ChannelType,
		SourceUserID:  req.Event.User,
		SourceGroupID: req.Event.Channel,
		SourceRoomID:  "",
		MessageID:     req.Event.TS,
		Text:          req.Event.Text,
	})
	if !strings.EqualFold(strings.TrimSpace(req.Event.Type), "message") {
		return "", nil
	}
	if strings.TrimSpace(req.Event.Subtype) != "" || strings.TrimSpace(req.Event.BotID) != "" {
		return "", nil
	}

	message, ok, reason := adaptSlackEventToUnified(*req.Event)
	if !ok {
		webhooklog.LogUnifiedConversionSkipped(webhooklog.UnifiedConversionSkipped{
			Provider:      "slack",
			EventType:     req.Event.Type,
			SourceType:    req.Event.ChannelType,
			SourceUserID:  req.Event.User,
			SourceGroupID: req.Event.Channel,
			SourceRoomID:  "",
			MessageID:     req.Event.TS,
			Reason:        reason,
		})
		return "", nil
	}
	if resolvedName, nameErr := resolveSlackChannelDisplayName(context.Background(), message); nameErr == nil {
		message.ChannelName = resolvedName
	}

	savedMessage := s.persistenceService.PersistUnifiedMessage(context.Background(), message)
	decision := &commanddecision.Decision{IsMentionedBot: message.MentionsUser(config.Slack.BotUserID)}
	if strings.EqualFold(strings.TrimSpace(message.ChannelType), "private") {
		decision.IsPrivateChannel = true
	}
	decision.IsEffectiveMentionedBot = decision.IsMentionedBot
	if s.decisionService != nil {
		decision = s.decisionService.DecideMessage(context.Background(), message, savedMessage, config.Slack.BotUserID)
		if decision != nil && strings.EqualFold(strings.TrimSpace(message.ChannelType), "private") {
			decision.IsPrivateChannel = true
		}
	}
	if decision == nil || !decision.IsCommand() {
		s.handleRealtimeNonCommandServices(
			message,
			savedMessage,
			strings.TrimSpace(req.Event.User),
			resolveSlackReplyRef(*req.Event),
		)
		return "", nil
	}

	if s.commandFlow == nil {
		return "", nil
	}
	s.commandFlow.ProcessCommand(
		message,
		savedMessage,
		strings.TrimSpace(message.SenderID),
		resolveSlackReplyRef(*req.Event),
		"",
	)
	return "", nil
}

// handleChannelLifecycleEvent handles bot join/leave events and updates channel lifecycle.
//
// 規則：
// - bot 被邀請進群組/頻道時：建立（或啟用）group channel
// - bot 離開時：將對應 channel 標記 is_active=false
func (s *WebhookService) handleChannelLifecycleEvent(ctx context.Context, event *slackEvent) (bool, error) {
	if s == nil || s.repo == nil || event == nil {
		return false, nil
	}
	eventType := strings.ToLower(strings.TrimSpace(event.Type))
	channelID := strings.TrimSpace(event.Channel)
	if channelID == "" {
		return false, nil
	}

	botUserID := strings.TrimSpace(config.Slack.BotUserID)
	switch eventType {
	case "member_joined_channel":
		// 只有「加入者是 bot 本身」才視為頻道生命週期事件。
		if strings.TrimSpace(event.User) != botUserID {
			return true, nil
		}
		channelName, err := GetChannelNameByID(ctx, channelID)
		if err != nil {
			return true, err
		}
		if _, err := s.repo.GetOrCreateChannel(ctx, "slack", channelID, "group", channelName); err != nil {
			return true, err
		}
		if err := s.repo.SetChannelActiveByPlatformGroupID(ctx, "slack", channelID, true); err != nil {
			return true, err
		}
		return true, nil
	case "member_left_channel":
		if strings.TrimSpace(event.User) != botUserID {
			return true, nil
		}
		if err := s.repo.SetChannelActiveByPlatformGroupID(ctx, "slack", channelID, false); err != nil {
			return true, err
		}
		return true, nil
	case "channel_left":
		if err := s.repo.SetChannelActiveByPlatformGroupID(ctx, "slack", channelID, false); err != nil {
			return true, err
		}
		return true, nil
	default:
		return false, nil
	}
}

func resolveSlackChannelDisplayName(ctx context.Context, message *unifiedmessage.Message) (string, error) {
	if message == nil {
		return "", fmt.Errorf("message is nil")
	}
	if strings.EqualFold(strings.TrimSpace(message.ChannelType), "private") {
		return GetUserDisplayNameByID(ctx, strings.TrimSpace(message.SenderID))
	}
	return GetChannelNameByID(ctx, strings.TrimSpace(message.ChannelID))
}

type slackOutboundMessenger struct {
	sender PushMessageService
}

func (m slackOutboundMessenger) SendText(ctx context.Context, chatID string, userID string, text string, replyRef string, quoteRef string) (string, error) {
	if m.sender == nil {
		return "", nil
	}
	return m.sender.SendTextToChat(ctx, chatID, userID, text, replyRef, quoteRef)
}

func (s *WebhookService) handleRealtimeNonCommandServices(message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, slackUserID string, quoteRef string) {
	if s == nil || message == nil {
		return
	}
	if s.nonCommandDispatcher == nil {
		return
	}
	// 這裡只負責把 Slack 事件轉成共用 realtime context，避免 webhook 入口塞進太多 side-effect。
	// 如果未來再加其他非指令服務，也應該掛在 dispatcher 內，而不是在這裡分支擴張。
	s.nonCommandDispatcher.Handle(context.Background(), realtime.MessageContext{
		Message:        message,
		SavedMessage:   savedMessage,
		PlatformUserID: slackUserID,
		QuoteRef:       quoteRef,
	})
}

// slackRealtimeSender 將 Slack 的送訊息能力包成共用 realtime sender 介面。
//
// 這樣 auto-translate 只需要一個統一的發送契約，不必直接依賴 Slack 的 webhook service。
type slackRealtimeSender struct {
	sender PushMessageService
}

func (m slackRealtimeSender) SendText(ctx context.Context, chatID string, userID string, text string, replyRef string, quoteRef string) (string, error) {
	if m.sender == nil {
		return "", nil
	}
	return m.sender.SendTextToChat(ctx, chatID, userID, text, replyRef, quoteRef)
}
