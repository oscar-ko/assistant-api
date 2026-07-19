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
	realtimeclient "assistant-api/internal/integration/provider/realtime"
	webhooklog "assistant-api/internal/integration/provider/webhooklog"
	"assistant-api/internal/integration/runtimecontext"
	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"
	"assistant-api/internal/usecase/actionpost"
	"assistant-api/internal/usecase/inbound/commandchain"
	"assistant-api/internal/usecase/inbound/commanddecision"
	"assistant-api/internal/usecase/inbound/conversationflow"
	"assistant-api/internal/usecase/inbound/messagepersist"
	"assistant-api/internal/usecase/inbound/messagepipeline"
	"assistant-api/internal/usecase/inbound/realtime"

	"github.com/google/uuid"
)

type WebhookProcessor interface {
	ValidateSignature(timestamp string, signature string, body []byte) error
	ProcessIncoming(body []byte) (string, error)
}

type WebhookService struct {
	repo                  *repository.ChannelMessageRepo
	tokenStore            slackBotTokenStore
	decisionService       commanddecision.Service
	llmInteractionService aillminteraction.InteractionService
	persistenceService    messagepersist.Service
	topKFilterService     aitopkfilter.Service
	actionPostDispatcher  *actionpost.Dispatcher
	followUpSender        PushMessageService
	nonCommandDispatcher  *realtime.Dispatcher
	commandFlow           *conversationflow.Orchestrator
	messagePipeline       *messagepipeline.Handler
}

type WebhookServiceOptions struct {
	LLMInteraction aillminteraction.InteractionService
	TopKFilter     aitopkfilter.Service
	FollowUpSender PushMessageService
}

type slackWebhookRequest struct {
	Type      string      `json:"type"`
	TeamID    string      `json:"team_id"`
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

func NewWebhookService(repo *repository.ChannelMessageRepo, tokenStore slackBotTokenStore) WebhookProcessor {
	return NewWebhookServiceWithOptions(repo, tokenStore, WebhookServiceOptions{})
}

func NewWebhookServiceWithOptions(repo *repository.ChannelMessageRepo, tokenStore slackBotTokenStore, options WebhookServiceOptions) WebhookProcessor {
	persistSvc := messagepersist.NewService(repo, NewSenderNameResolver(tokenStore))
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
	// reranker 與 classifier/translator 一樣在 provider 啟動時建好，後續只透過 realtime service 注入使用。
	// Slack 事件格式不應影響 implicit reply 的語意排序；平台差異只保留在 sender 與 channel metadata。
	rerankerClient, _, err := realtimeclient.BuildRerankerFromConfig(config.AI)
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
		BotSenderID:   "",
		PlatformLabel: "slack:" + strings.TrimSpace(translateProfile),
	})
	// RecentLimit 讀取共用 history_context，讓 Slack 與 LINE 對「往前抓幾則訊息」維持一致語意。
	// 若未來不同服務需要不同窗口，應在服務自己的 options 裡覆寫，而不是在 provider webhook 裡硬分支。
	todoReminder := realtime.NewTodoReminderService(realtime.TodoReminderServiceOptions{
		PlatformLabel: "slack",
		Repo:          repo,
		PersistTodoCandidate: func(ctx context.Context, input realtime.TodoCandidateInput) (*ent.TodoCandidate, error) {
			// provider 層只做型別轉接：usecase 決定何時落庫，repository 決定如何寫入 Ent。
			return repo.SaveTodoCandidateFromAnalysis(ctx, repository.SaveTodoCandidateInput{
				ChannelID:       input.ChannelID,
				MessageID:       input.MessageID,
				LinkedMessageID: input.LinkedMessageID,
				Decision:        input.Decision,
				Summary:         input.Summary,
				Assignees:       input.Assignees,
				DueText:         input.DueText,
				MissingFields:   input.MissingFields,
				Confidence:      input.Confidence,
				Reason:          input.Reason,
			})
		},
		LLM:         options.LLMInteraction,
		Ranker:      rerankerClient,
		RecentLimit: config.AI.HistoryContext.RecentMessageLimit,
	})
	messageClassifier := realtime.NewMessageClassificationService(realtime.MessageClassificationServiceOptions{
		TextScanGate:  repo,
		Classifier:    classifierClient,
		Handlers:      []realtime.ClassificationHandler{todoReminder},
		PlatformLabel: "slack:" + strings.TrimSpace(classifierProfile),
	})
	nonCommandDispatcher := realtime.NewDispatcher(autoTranslate, messageClassifier)
	// Slack 和 LINE 共用同一組非指令 realtime services；
	// 平台差異只留在 sender/user resolver，分類 tag 的後續處理由 handler 接手。
	flow := conversationflow.NewFromFactory(conversationflow.FactoryOptions{
		PlatformLabel:               "slack",
		BotSenderID:                 "",
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
	pipeline := &messagepipeline.Handler{
		PlatformLabel:        "slack",
		Persistence:          persistSvc,
		Decision:             decisionSvc,
		NonCommandDispatcher: nonCommandDispatcher,
		CommandFlow:          flow,
	}
	return &WebhookService{
		repo:                  repo,
		tokenStore:            tokenStore,
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
	if handled, err := s.handleChannelLifecycleEvent(context.Background(), strings.TrimSpace(req.TeamID), req.Event); handled {
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
	message.PlatformTenantID = strings.TrimSpace(req.TeamID)
	botUserID, botUserIDErr := s.resolveWorkspaceBotUserID(context.Background(), strings.TrimSpace(req.TeamID))
	if botUserIDErr != nil {
		return "", botUserIDErr
	}
	if resolvedName, nameErr := resolveSlackChannelDisplayName(context.Background(), s.tokenStore, message); nameErr == nil {
		message.ChannelName = resolvedName
	}

	if s.messagePipeline == nil {
		return "", nil
	}
	s.messagePipeline.Process(messagepipeline.Input{
		Context:        runtimecontext.WithBotSenderID(WithWorkspaceTeamID(context.Background(), strings.TrimSpace(message.PlatformTenantID)), botUserID),
		Message:        message,
		BotUserID:      botUserID,
		PlatformUserID: strings.TrimSpace(req.Event.User),
		ReplyRef:       resolveSlackReplyRef(*req.Event),
	})
	return "", nil
}

// handleChannelLifecycleEvent handles bot join/leave events and updates channel lifecycle.
//
// 規則：
// - bot 被邀請進群組/頻道時：建立（或啟用）group channel
// - bot 離開時：將對應 channel 標記 is_active=false
func (s *WebhookService) handleChannelLifecycleEvent(ctx context.Context, teamID string, event *slackEvent) (bool, error) {
	if s == nil || s.repo == nil || event == nil {
		return false, nil
	}
	eventType := strings.ToLower(strings.TrimSpace(event.Type))
	channelID := strings.TrimSpace(event.Channel)
	if channelID == "" {
		return false, nil
	}

	botUserID, err := s.resolveWorkspaceBotUserID(ctx, teamID)
	if err != nil {
		return true, err
	}
	switch eventType {
	case "member_joined_channel":
		// 只有「加入者是 bot 本身」才視為頻道生命週期事件。
		if strings.TrimSpace(event.User) != botUserID {
			return true, nil
		}
		channelName, err := GetChannelNameByID(ctx, s.tokenStore, teamID, channelID)
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

func resolveSlackChannelDisplayName(ctx context.Context, tokenStore slackBotTokenStore, message *unifiedmessage.Message) (string, error) {
	if message == nil {
		return "", fmt.Errorf("message is nil")
	}
	teamID := strings.TrimSpace(message.PlatformTenantID)
	if strings.EqualFold(strings.TrimSpace(message.ChannelType), "private") {
		return GetUserDisplayNameByID(ctx, tokenStore, teamID, strings.TrimSpace(message.SenderID))
	}
	return GetChannelNameByID(ctx, tokenStore, teamID, strings.TrimSpace(message.ChannelID))
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

func (s *WebhookService) resolveWorkspaceBotUserID(ctx context.Context, teamID string) (string, error) {
	if s == nil || s.tokenStore == nil {
		return "", fmt.Errorf("slack bot token store is not initialized")
	}
	return s.tokenStore.ResolveWorkspaceBotUserID(ctx, strings.TrimSpace(teamID))
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
