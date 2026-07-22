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
	"go.uber.org/zap"
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
	Channel     slackChannelRef `json:"channel"`
	ChannelType string `json:"channel_type"`
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
	ClientMsgID string `json:"client_msg_id"`
	BotID       string `json:"bot_id"`
}

type slackChannelRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// UnmarshalJSON 同時接受 Slack event 裡兩種 channel 表示法：
// - 一般 message/member lifecycle 事件常給字串 channel id，例如 "C123"。
// - channel_joined 事件可能給物件，例如 {"id":"C123","name":"general"}。
// 若只用 string 會讓物件型 payload 解析失敗，bot invite 事件就會在 webhook 入口被吃掉。
func (r *slackChannelRef) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err == nil {
		r.ID = strings.TrimSpace(raw)
		r.Name = ""
		return nil
	}

	var parsed struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	r.ID = strings.TrimSpace(parsed.ID)
	r.Name = strings.TrimSpace(parsed.Name)
	return nil
}

func (r slackChannelRef) String() string {
	return strings.TrimSpace(r.ID)
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
	// RecentLimit 現在讀取 Todo Reminder 自己的 recent_context_message_limit。
	// 這個窗口只服務待辦的 implicit analysis；若未來其他 realtime 服務需要近端上下文，應新增各自語意清楚的設定，避免共用 key 造成調參互相牽動。
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
				DueAt:           input.DueAt,
				DueTimezone:     input.DueTimezone,
				DuePrecision:    input.DuePrecision,
				DueDecision:     input.DueDecision,
				DueConfidence:   input.DueConfidence,
				DueReason:       input.DueReason,
				MissingFields:   input.MissingFields,
				Confidence:      input.Confidence,
				Reason:          input.Reason,
			})
		},
		LLM:         options.LLMInteraction,
		Ranker:      rerankerClient,
		RecentLimit: config.AI.TodoReminder.RecentContextMessageLimit,
		// ReplyChainMaxDepth 屬於顯式 reply/quote 的追溯深度；RecentLimit 屬於 implicit recent window 的原始召回量。
		// 兩者最後都會和 evidence 小窗合併，但各自控制不同來源的上下文，因此仍分開注入。
		ReplyChainMaxDepth:              config.AI.TodoReminder.ReplyChainMaxDepth,
		EvidenceAnchorLimitPerCandidate: config.AI.TodoReminder.EvidenceAnchorLimitPerCandidate,
		EvidenceWindowBeforeLimit:       config.AI.TodoReminder.EvidenceWindowBeforeLimit,
		EvidenceWindowAfterLimit:        config.AI.TodoReminder.EvidenceWindowAfterLimit,
		MaxCandidateContexts:            config.AI.TodoReminder.MaxCandidateContexts,
		MaxContextMessages:              config.AI.TodoReminder.MaxContextMessages,
		Timezone:                        config.AI.TodoReminder.Timezone,
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
		SourceGroupID: req.Event.Channel.String(),
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
			SourceGroupID: req.Event.Channel.String(),
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
	channelID := event.Channel.String()
	if channelID == "" {
		return false, nil
	}

	switch eventType {
	case "message":
		// Slack 的 bot 加入/離開頻道不一定會送獨立 lifecycle event；
		// 有些 workspace 只會送 type=message 並用 subtype=channel_join/group_join 表達。
		// 這裡只看 structured subtype 和 bot user id，不解析「has joined the channel」這類顯示文字。
		lifecycleAction, ok := slackMessageLifecycleAction(event.Subtype)
		if !ok {
			return false, nil
		}
		botUserID, err := s.resolveWorkspaceBotUserID(ctx, teamID)
		if err != nil {
			return true, err
		}
		if strings.TrimSpace(event.User) != botUserID {
			zap.L().Info("slack channel lifecycle skipped: joined user is not bot",
				zap.String("team_id", strings.TrimSpace(teamID)),
				zap.String("event_type", eventType),
				zap.String("event_subtype", strings.TrimSpace(event.Subtype)),
				zap.String("channel_id", channelID),
				zap.String("event_user", strings.TrimSpace(event.User)),
				zap.String("bot_user_id", botUserID),
			)
			return true, nil
		}
		if lifecycleAction == slackLifecycleActionJoin {
			channelName, err := s.resolveSlackLifecycleChannelName(ctx, teamID, event)
			if err != nil {
				return true, err
			}
			if err := s.createOrActivateSlackLifecycleChannel(ctx, teamID, eventType, event.Subtype, channelID, channelName); err != nil {
				return true, err
			}
			return true, nil
		}
		if err := s.repo.SetChannelActiveByPlatformGroupID(ctx, "slack", channelID, false); err != nil {
			return true, err
		}
		zap.L().Info("slack channel lifecycle deactivated channel",
			zap.String("team_id", strings.TrimSpace(teamID)),
			zap.String("event_type", eventType),
			zap.String("event_subtype", strings.TrimSpace(event.Subtype)),
			zap.String("channel_id", channelID),
		)
		return true, nil
	case "channel_joined":
		channelName, err := s.resolveSlackLifecycleChannelName(ctx, teamID, event)
		if err != nil {
			return true, err
		}
		if err := s.createOrActivateSlackLifecycleChannel(ctx, teamID, eventType, event.Subtype, channelID, channelName); err != nil {
			return true, err
		}
		return true, nil
	case "member_joined_channel":
		botUserID, err := s.resolveWorkspaceBotUserID(ctx, teamID)
		if err != nil {
			return true, err
		}
		// member_joined_channel 也會在一般使用者加入頻道時觸發；
		// 我們的系統 channel 代表「bot 已在該 Slack 對話空間提供服務」，所以只有加入者是 bot 本身才建立。
		if strings.TrimSpace(event.User) != botUserID {
			zap.L().Info("slack channel lifecycle skipped: joined user is not bot",
				zap.String("team_id", strings.TrimSpace(teamID)),
				zap.String("event_type", eventType),
				zap.String("event_subtype", strings.TrimSpace(event.Subtype)),
				zap.String("channel_id", channelID),
				zap.String("event_user", strings.TrimSpace(event.User)),
				zap.String("bot_user_id", botUserID),
			)
			return true, nil
		}
		channelName, err := s.resolveSlackLifecycleChannelName(ctx, teamID, event)
		if err != nil {
			return true, err
		}
		if err := s.createOrActivateSlackLifecycleChannel(ctx, teamID, eventType, event.Subtype, channelID, channelName); err != nil {
			return true, err
		}
		return true, nil
	case "member_left_channel":
		botUserID, err := s.resolveWorkspaceBotUserID(ctx, teamID)
		if err != nil {
			return true, err
		}
		if strings.TrimSpace(event.User) != botUserID {
			zap.L().Info("slack channel lifecycle skipped: left user is not bot",
				zap.String("team_id", strings.TrimSpace(teamID)),
				zap.String("event_type", eventType),
				zap.String("event_subtype", strings.TrimSpace(event.Subtype)),
				zap.String("channel_id", channelID),
				zap.String("event_user", strings.TrimSpace(event.User)),
				zap.String("bot_user_id", botUserID),
			)
			return true, nil
		}
		if err := s.repo.SetChannelActiveByPlatformGroupID(ctx, "slack", channelID, false); err != nil {
			return true, err
		}
		zap.L().Info("slack channel lifecycle deactivated channel",
			zap.String("team_id", strings.TrimSpace(teamID)),
			zap.String("event_type", eventType),
			zap.String("event_subtype", strings.TrimSpace(event.Subtype)),
			zap.String("channel_id", channelID),
		)
		return true, nil
	case "channel_left":
		if err := s.repo.SetChannelActiveByPlatformGroupID(ctx, "slack", channelID, false); err != nil {
			return true, err
		}
		zap.L().Info("slack channel lifecycle deactivated channel",
			zap.String("team_id", strings.TrimSpace(teamID)),
			zap.String("event_type", eventType),
			zap.String("event_subtype", strings.TrimSpace(event.Subtype)),
			zap.String("channel_id", channelID),
		)
		return true, nil
	default:
		return false, nil
	}
}

func (s *WebhookService) createOrActivateSlackLifecycleChannel(ctx context.Context, teamID string, eventType string, eventSubtype string, channelID string, channelName string) error {
	// GetOrCreateChannel 負責建立或同步名稱/type；接著再明確 SetChannelActive，
	// 讓「bot 重新被邀請進既有頻道」和 LINE join 流程一樣會把 channel 從 inactive 重新啟用。
	channelItem, err := s.repo.GetOrCreateChannel(ctx, "slack", channelID, "group", channelName)
	if err != nil {
		return err
	}
	if err := s.repo.SetChannelActiveByPlatformGroupID(ctx, "slack", channelID, true); err != nil {
		return err
	}
	zap.L().Info("slack channel lifecycle created or activated channel",
		zap.String("team_id", strings.TrimSpace(teamID)),
		zap.String("event_type", strings.TrimSpace(eventType)),
		zap.String("event_subtype", strings.TrimSpace(eventSubtype)),
		zap.String("channel_id", channelID),
		zap.String("channel_name", strings.TrimSpace(channelName)),
		zap.String("system_channel_id", channelItem.ID.String()),
	)
	return nil
}

const (
	slackLifecycleActionJoin  = "join"
	slackLifecycleActionLeave = "leave"
)

func slackMessageLifecycleAction(subtype string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(subtype)) {
	case "channel_join", "group_join":
		return slackLifecycleActionJoin, true
	case "channel_leave", "group_leave":
		return slackLifecycleActionLeave, true
	default:
		return "", false
	}
}

func (s *WebhookService) resolveSlackLifecycleChannelName(ctx context.Context, teamID string, event *slackEvent) (string, error) {
	if event == nil {
		return "", fmt.Errorf("slack event is nil")
	}
	if name := strings.TrimSpace(event.Channel.Name); name != "" {
		return name, nil
	}
	return GetChannelNameByID(ctx, s.tokenStore, teamID, event.Channel.String())
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
