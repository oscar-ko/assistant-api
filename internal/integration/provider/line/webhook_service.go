package line

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	"assistant-api/internal/integration/provider/realtime"
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
	"assistant-api/internal/usecase/inbound/qarouting"

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
	decisionService       commanddecision.Service
	llmInteractionService llminteraction.InteractionService
	persistenceService    messagepersist.Service
	topKFilterService     topkfilter.Service
	actionPostDispatcher  *actionpost.Dispatcher
	followUpSender        PushMessageService
	nonCommandDispatcher  *realtime.Dispatcher
	commandFlow           *conversationflow.Orchestrator
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
	translateClient, translateProfile, err := realtime.BuildTranslatorFromConfig(config.AI, config.LLMProviders)
	if err != nil {
		panic(err)
	}
	autoTranslate := realtime.NewAutoTranslateService(realtime.AutoTranslateServiceOptions{
		Repo:       repo,
		Sender:     lineRealtimeSender{sender: options.FollowUpSender},
		Translator: translateClient,
		ResolveOwnerUserID: func(ctx context.Context, platformUserID string) (uuid.UUID, error) {
			return repo.ResolveUserIDByLineUserID(ctx, platformUserID)
		},
		BotSenderID:   strings.TrimSpace(config.Line.BotUserID),
		PlatformLabel: "line:" + strings.TrimSpace(translateProfile),
	})
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
			// 非指令訊息不進 command flow，但仍允許多服務即時 side-effect。
			// 目前包含：翻譯服務；後續可擴充待辦分析等服務，避免互斥。
			s.handleRealtimeNonCommandServices(
				message,
				savedMessage,
				strings.TrimSpace(event.Source.UserID),
				strings.TrimSpace(event.Message.QuoteToken),
			)
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

		s.ensureCommandFlow()
		if s.commandFlow == nil {
			continue
		}
		s.commandFlow.ProcessCommand(
			message,
			savedMessage,
			strings.TrimSpace(event.Source.UserID),
			strings.TrimSpace(event.ReplyToken),
			strings.TrimSpace(event.Message.QuoteToken),
		)
	}

}

// handleRealtimeNonCommandServices 是非指令訊息的即時服務分派點。
// 設計目標：
// 1) 保持 command 與 non-command 路徑解耦
// 2) 讓同一則訊息可並行觸發多個服務（翻譯、提醒分析等）
// 3) 新服務只需在這裡掛載，不必改 command 判斷流程
func (s *WebhookService) handleRealtimeNonCommandServices(message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, lineUserID string, quoteToken string) {
	if s == nil || message == nil {
		return
	}
	if s.nonCommandDispatcher == nil {
		return
	}
	// 非指令訊息可同時觸發多個即時服務；翻譯/未來服務都由共用 dispatcher 管理。
	s.nonCommandDispatcher.Handle(context.Background(), realtime.MessageContext{
		Message:        message,
		SavedMessage:   savedMessage,
		PlatformUserID: lineUserID,
		QuoteRef:       quoteToken,
	})
}

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

// dispatchActionPostHandlers 依最終決策的 api_operation 分派對應後處理。
// 這層刻意放在主流程之外，讓新 action 只需註冊 handler，不需改 ProcessIncoming 主幹。
func (s *WebhookService) dispatchActionPostHandlers(message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, lineUserID string, replyToken string, quoteToken string, decision *llminteraction.ActionDecision, matchedSkillCode string) {
	if s == nil || decision == nil {
		return
	}
	if s.actionPostDispatcher == nil {
		// 保底 lazy init，確保測試直接組 struct 時仍可共用同一份 dispatcher。
		s.actionPostDispatcher = actionpost.NewDefaultDispatcher(s.repo)
	}
	succeeded := s.actionPostDispatcher.Dispatch(message, lineUserID, decision, matchedSkillCode)
	// 只有 action post handler 明確回報成功，才發送「執行成功」通知。
	// 這可避免在 side-effect 部分失敗時誤導使用者。
	if !succeeded {
		return
	}
	s.sendActionSuccessNotice(message, savedMessage, lineUserID, replyToken, quoteToken)
}

// sendActionSuccessNotice 在 action 成功後通知使用者。
// 流程策略：
// 1) 若有 replyToken，先嘗試 Reply API，確保訊息掛在同一則指令下。
// 2) Reply 失敗或無 token 時 fallback 到 Push API，避免成功訊息遺失。
// 3) 無論 reply/push，都會將送出的 bot 訊息落庫並關聯到原指令訊息。
func (s *WebhookService) sendActionSuccessNotice(message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, lineUserID string, replyToken string, quoteToken string) {
	if s == nil || s.followUpSender == nil || message == nil {
		return
	}

	lineUserID = strings.TrimSpace(lineUserID)
	replyToken = strings.TrimSpace(replyToken)
	quoteToken = strings.TrimSpace(quoteToken)
	if lineUserID == "" {
		return
	}
	//TODO: 未來抽出成多國語系
	text := "指令已執行成功"

	chatID := strings.TrimSpace(message.ChannelID)
	sentPlatformMessageID := ""
	usedReply := false
	// Reply token 具時效且一次性，成功時可保證是「回覆該次使用者指令」。
	if replyToken != "" {
		replySentID, err := s.followUpSender.SendTextToChat(
			context.Background(),
			chatID,
			lineUserID,
			text,
			replyToken,
			quoteToken,
		)
		if err != nil {
			zap.L().Warn("line action success notification reply failed, fallback to push",
				zap.String("channel_id", chatID),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("line_user_id", lineUserID),
				zap.Error(err),
			)
		} else {
			sentPlatformMessageID = replySentID
			usedReply = true
		}
	}

	if sentPlatformMessageID == "" {
		// Reply 不可用時改用 push，確保成功通知至少送達。
		pushSentID, err := s.followUpSender.SendTextToChat(
			context.Background(),
			chatID,
			lineUserID,
			text,
			"",
			quoteToken,
		)
		if err != nil {
			zap.L().Warn("line action success notification push failed",
				zap.String("channel_id", chatID),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("line_user_id", lineUserID),
				zap.Error(err),
			)
			return
		}
		sentPlatformMessageID = pushSentID
	}

	zap.L().Info("line action success notification sent",
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("line_user_id", lineUserID),
		zap.Bool("used_reply", usedReply),
		zap.String("sent_platform_message_id", sentPlatformMessageID),
	)

	if s.repo == nil || savedMessage == nil {
		return
	}
	// 將成功通知寫入 channel_message，並把 related_message_id 指回觸發指令。
	// 這讓前端/後端都能沿對話鍊追溯「哪個成功通知對應哪個指令」。
	if _, err := s.repo.SaveSentMessage(
		context.Background(),
		savedMessage.ChannelID,
		strings.TrimSpace(config.Line.BotUserID),
		"",
		sentPlatformMessageID,
		text,
		"text",
		time.Now().UnixMilli(),
		savedMessage.ID,
	); err != nil {
		zap.L().Warn("persist action success notification failed",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("line_user_id", lineUserID),
			zap.String("sent_platform_message_id", sentPlatformMessageID),
			zap.Error(err),
		)
	}
}

// translationTargetLocalesFromDecision 從通用 action_params 擷取翻譯語系參數。
// 契約固定使用 target_locales（字串陣列），
// 輸出會經過清理與去重，避免同語系重覆寫入。
func translationTargetLocalesFromDecision(decision *llminteraction.ActionDecision) []string {
	return actionpost.ExtractTranslationTargetLocales(decision)
}

func (s *WebhookService) routeMessageToQuestionAnswer(message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, lineUserID string, actionConfidence float64, actionThreshold float64, cause string, decisionReason string, missingParameters []string) {
	if s == nil || s.llmInteractionService == nil || message == nil {
		return
	}

	// 進入此函式代表已判定「不應直接執行 action」。
	// 只有在真的需要補參數時才走追問；
	// 對一般問題（即使 decision 給了 ask_clarifying_question 但無缺參數）改走一般問答，避免追問迴圈。
	var (
		qa     *llminteraction.QuestionAnswer
		qaErr  error
		qaMode = "question_answer"
	)
	if qarouting.ShouldUseClarifyingQuestionMode(cause, missingParameters) {
		qaMode = "clarifying_question"
		// 若已能從 missing parameters 推導出可用模板，優先用固定文案。
		// 這可避免模型在「明確缺參數」時產生不穩定追問。
		// 例如 target_locales 缺失時，一律問「要翻譯成哪些語言」。
		if template := buildMissingParameterTemplateQuestion(missingParameters); strings.TrimSpace(template) != "" {
			qa = &llminteraction.QuestionAnswer{
				SchemaVersion: "v1",
				Answer:        template,
				Confidence:    1,
			}
		} else {
			// 沒有模板時才退回 LLM 生成追問，避免固定模板覆蓋複雜情境。
			qa, qaErr = s.llmInteractionService.AskClarifyingQuestion(context.Background(), message.Text, decisionReason)
		}
	} else {
		// 非追問類型（例如 answer_question）走一般問答。
		qa, qaErr = s.llmInteractionService.AnswerQuestion(context.Background(), message.Text)
	}
	if qaErr != nil {
		zap.L().Warn("line message semantic question answer failed",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("cause", cause),
			zap.String("decision_reason", strings.TrimSpace(decisionReason)),
			zap.String("mode", qaMode),
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
			zap.String("decision_reason", strings.TrimSpace(decisionReason)),
			zap.String("mode", qaMode),
			zap.Float64("action_confidence", actionConfidence),
			zap.Float64("action_threshold", actionThreshold),
		)
		return
	}

	questionThreshold := config.AI.LLMInteraction.QuestionConfidenceThreshold
	// 第二道門檻：問答答案信心不足時，標記建議改送 cloud LLM。
	// 目前先記錄 recommend_cloud_llm，不在此函式直接呼叫外部 cloud provider。
	recommendCloudLLM := questionThreshold > 0 && qa.Confidence < questionThreshold
	zap.L().Info("line message semantic question answered",
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("text", strings.TrimSpace(message.Text)),
		zap.String("cause", cause),
		zap.String("decision_reason", strings.TrimSpace(decisionReason)),
		zap.String("mode", qaMode),
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

	if qaMode == "clarifying_question" {
		s.sendClarifyingQuestion(message, savedMessage, lineUserID, strings.TrimSpace(qa.Answer))
	}
}

func buildMissingParameterTemplateQuestion(missingParameters []string) string {
	// 這個 helper 專門把缺參數訊號轉成「可直接發送」的固定追問文案。
	// 規則：
	// 1) 先正規化與去重
	// 2) 有特例模板（目前 target_locales）時優先使用
	// 3) 其餘情況回退為通用模板
	if len(missingParameters) == 0 {
		return ""
	}
	normalized := make([]string, 0, len(missingParameters))
	seen := make(map[string]struct{}, len(missingParameters))
	for _, parameter := range missingParameters {
		key := strings.ToLower(strings.TrimSpace(parameter))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	if len(normalized) == 0 {
		return ""
	}
	if len(normalized) == 1 && normalized[0] == llminteraction.ActionParamTargetLocales {
		return "請告訴我要翻譯成哪些語言？例如英文、日文。"
	}
	return "請補充以下必要資訊後我才能執行指令：" + strings.Join(normalized, ", ")
}

func (s *WebhookService) persistMissingParameterResult(savedMessage *ent.ChannelMessage, apiOperation string, source string, missingParameters []string, reason string) {
	// 這裡是 missing_parameter 狀態統一落庫入口。
	// 任何「需要追問才能繼續」的情境都應收斂到這裡：
	// - 模型主動 ask_clarifying_question
	// - execute_action 但被契約拒絕
	// 好處是 action_results 可維持單一查詢語意，不需要分散解讀多種狀態。
	if s == nil || s.repo == nil || savedMessage == nil {
		return
	}
	apiOperation = strings.TrimSpace(apiOperation)
	if apiOperation == "" {
		return
	}
	seen := make(map[string]struct{}, len(missingParameters))
	normalized := make([]string, 0, len(missingParameters))
	for _, parameter := range missingParameters {
		key := strings.ToLower(strings.TrimSpace(parameter))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	if len(normalized) == 0 {
		// 若上游沒給 missing_parameters，嘗試從 reason 文字中抽取 action_params.xxx。
		// 這讓 contract_reject 路徑仍可儘量留下結構化資訊。
		normalized = llminteraction.InferMissingParametersFromReason(reason)
	}
	resultMessage := "source=" + strings.TrimSpace(source)
	if len(normalized) > 0 {
		resultMessage += ";missing_parameters=" + strings.Join(normalized, ",")
	}
	if trimmedReason := strings.TrimSpace(reason); trimmedReason != "" {
		resultMessage += ";reason=" + trimmedReason
	}
	if err := s.repo.UpsertActionResult(context.Background(), apiOperation, savedMessage.ID, "missing_parameter", resultMessage); err != nil {
		zap.L().Warn("persist missing-parameter action result failed",
			zap.String("channel_id", savedMessage.ChannelID.String()),
			zap.String("message_id", savedMessage.ID.String()),
			zap.String("api_operation", apiOperation),
			zap.Error(err),
		)
	}
}

func (s *WebhookService) sendClarifyingQuestion(message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, lineUserID string, question string) {
	if s == nil || s.followUpSender == nil || message == nil {
		return
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return
	}

	// 追問不需要綁定到當次 webhook reply token，
	// 採 push 以支援延遲生成或後續補發場景。
	sentPlatformMessageID, err := s.followUpSender.SendTextToChat(
		context.Background(),
		strings.TrimSpace(message.ChannelID),
		strings.TrimSpace(lineUserID),
		question,
		"",
		"",
	)
	if err != nil {
		zap.L().Warn("line clarifying question push failed",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("line_user_id", strings.TrimSpace(lineUserID)),
			zap.Error(err),
		)
		return
	}

	zap.L().Info("line clarifying question pushed",
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("line_user_id", strings.TrimSpace(lineUserID)),
		zap.String("sent_platform_message_id", sentPlatformMessageID),
	)

	if s.repo == nil || savedMessage == nil {
		return
	}
	if _, err := s.repo.SaveSentMessage(
		context.Background(),
		savedMessage.ChannelID,
		strings.TrimSpace(config.Line.BotUserID),
		"",
		sentPlatformMessageID,
		question,
		"text",
		time.Now().UnixMilli(),
		savedMessage.ID,
	); err != nil {
		zap.L().Warn("persist clarifying question failed",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("sent_platform_message_id", sentPlatformMessageID),
			zap.Error(err),
		)
	}
}

// persistUnifiedMessage 負責把統一訊息格式寫入 channel 與 channel_message。
// 這個函式只做資料持久化，不應再混入 AI 判讀或其他額外業務邏輯。
func (s *WebhookService) persistUnifiedMessage(message *unifiedmessage.Message) *ent.ChannelMessage {
	return s.persistenceService.PersistUnifiedMessage(context.Background(), message)
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
