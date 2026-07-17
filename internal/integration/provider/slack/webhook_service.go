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
	aillminteraction "assistant-api/internal/integration/ai/llm_interaction"
	aitopkfilter "assistant-api/internal/integration/ai/topkfilter"
	webhooklog "assistant-api/internal/integration/provider/webhooklog"
	"assistant-api/internal/repository"
	"assistant-api/internal/usecase/actionpost"
	"assistant-api/internal/usecase/inbound/commandchain"
	"assistant-api/internal/usecase/inbound/commanddecision"
	"assistant-api/internal/usecase/inbound/conversationflow"
	"assistant-api/internal/usecase/inbound/messagepersist"
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

type slackOutboundMessenger struct {
	sender PushMessageService
}

func (m slackOutboundMessenger) SendText(ctx context.Context, chatID string, userID string, text string, replyRef string, quoteRef string) (string, error) {
	if m.sender == nil {
		return "", nil
	}
	return m.sender.SendTextToChat(ctx, chatID, userID, text, replyRef, quoteRef)
}
