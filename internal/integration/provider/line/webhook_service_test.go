package line

import (
	"context"
	"testing"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"
	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"
	"assistant-api/internal/usecase/ai/topkfilter"
	"assistant-api/internal/usecase/inbound/commanddecision"
	"assistant-api/internal/usecase/inbound/conversationflow"
	"assistant-api/internal/usecase/inbound/messagepersist"
	"assistant-api/internal/usecase/inbound/messagepipeline"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type stubTopKFilter struct {
	called     bool
	candidates []topkfilter.ScoredCandidate
}

func (s *stubTopKFilter) FilterMessage(ctx context.Context, message *unifiedmessage.Message) []topkfilter.ScoredCandidate {
	_ = ctx
	_ = message
	s.called = true
	return s.candidates
}

type stubLLMInteraction struct {
	called                bool
	decision              *llminteraction.ActionDecision
	err                   error
	candidates            []llminteraction.ActionCandidate
	answerCalled          bool
	answer                *llminteraction.QuestionAnswer
	answerErr             error
	clarifyCalled         bool
	clarifyReason         string
	clarifyingQuestion    *llminteraction.QuestionAnswer
	clarifyingQuestionErr error
}

type stubPushMessageService struct {
	sendCalled            bool
	chatID                string
	lineUserID            string
	replyToken            string
	quoteToken            string
	text                  string
	sentPlatformMessageID string
	err                   error
}

type stubLineMessageStore struct{}

func (stubLineMessageStore) GetChannelByPlatformGroupID(ctx context.Context, platform string, groupID string) (*ent.Channel, error) {
	_ = ctx
	_ = platform
	return &ent.Channel{ID: uuid.New(), GroupID: groupID}, nil
}

func (stubLineMessageStore) UpdateChannelDisplayNameByID(ctx context.Context, channelID uuid.UUID, channelName string) error {
	_ = ctx
	_ = channelID
	_ = channelName
	return nil
}

func (stubLineMessageStore) SaveReceivedMessage(ctx context.Context, channelID uuid.UUID, platform string, platformTenantID string, senderID string, senderName string, platformMessageID string, replyToMsgID string, content string, messageType string, platformTimestamp int64) (*ent.ChannelMessage, error) {
	_ = ctx
	_ = platform
	_ = platformTenantID
	_ = senderName
	_ = platformTimestamp
	userID := uuid.New()
	return &ent.ChannelMessage{
		ID:                uuid.New(),
		ChannelID:         channelID,
		SenderID:          senderID,
		SenderUserID:      &userID,
		PlatformMessageID: platformMessageID,
		ReplyToMsgID:      replyToMsgID,
		Content:           content,
		MessageType:       messageType,
	}, nil
}

func newTestPipelineWebhookService(filter topkfilter.Service, llm llminteraction.InteractionService, push PushMessageService) *WebhookService {
	persistSvc := messagepersist.NewService(stubLineMessageStore{}, messagepersist.NoopSenderNameResolver{})
	decisionSvc := commanddecision.NewService(nil)
	flow := conversationflow.NewFromFactory(conversationflow.FactoryOptions{
		PlatformLabel:               "line",
		BotSenderID:                 config.Line.BotUserID,
		SuccessText:                 "指令已執行成功",
		CommandConfidenceThreshold:  config.AI.LLMInteraction.CommandConfidenceThreshold,
		QuestionConfidenceThreshold: config.AI.LLMInteraction.QuestionConfidenceThreshold,
		DecisionJSONRetryCount:      config.AI.LLMInteraction.DecisionJSONRetryCount,
		TopKFilter:                  filter,
		LLM:                         llm,
		Messenger:                   lineOutboundMessenger{sender: push},
	})
	return &WebhookService{
		topKFilterService:     filter,
		llmInteractionService: llm,
		persistenceService:    persistSvc,
		decisionService:       decisionSvc,
		followUpSender:        push,
		commandFlow:           flow,
		messagePipeline: &messagepipeline.Handler{
			PlatformLabel: "line",
			Persistence:   persistSvc,
			Decision:      decisionSvc,
			CommandFlow:   flow,
		},
	}
}

func (s *stubPushMessageService) SendTextToChat(ctx context.Context, chatID string, lineUserID string, text string, replyToken string, quoteToken string) (string, error) {
	_ = ctx
	s.sendCalled = true
	s.chatID = chatID
	s.lineUserID = lineUserID
	s.text = text
	s.replyToken = replyToken
	s.quoteToken = quoteToken
	return s.sentPlatformMessageID, s.err
}

func (s *stubLLMInteraction) DecideFinalAction(ctx context.Context, text string, candidates []llminteraction.ActionCandidate) (*llminteraction.ActionDecision, error) {
	_ = ctx
	_ = text
	s.called = true
	s.candidates = candidates
	return s.decision, s.err
}

func (s *stubLLMInteraction) AnswerQuestion(ctx context.Context, text string) (*llminteraction.QuestionAnswer, error) {
	_ = ctx
	_ = text
	s.answerCalled = true
	return s.answer, s.answerErr
}

func (s *stubLLMInteraction) AskClarifyingQuestion(ctx context.Context, text string, reason string) (*llminteraction.QuestionAnswer, error) {
	_ = ctx
	_ = text
	s.clarifyCalled = true
	s.clarifyReason = reason
	return s.clarifyingQuestion, s.clarifyingQuestionErr
}

func TestResolveSender(t *testing.T) {
	tests := []struct {
		name   string
		source webhookEventSource
		want   string
	}{
		{name: "prefer user id", source: webhookEventSource{UserID: "U123", GroupID: "G1", RoomID: "R1"}, want: "U123"},
		{name: "fallback group id", source: webhookEventSource{GroupID: "G1", RoomID: "R1"}, want: "G1"},
		{name: "fallback room id", source: webhookEventSource{RoomID: "R1"}, want: "R1"},
		{name: "unknown when empty", source: webhookEventSource{}, want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveSender(tt.source); got != tt.want {
				t.Fatalf("resolveSender() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWebhookService_ProcessIncoming_InvalidJSON(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	oldLogger := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	defer zap.ReplaceGlobals(oldLogger)

	(&WebhookService{}).ProcessIncoming([]byte("{invalid"), "sig")

	if observed.FilterMessage("line webhook parse failed").Len() == 0 {
		t.Fatalf("expected parse failed zap log")
	}
}

func TestWebhookService_ProcessIncoming_TextMessage(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	oldLogger := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	defer zap.ReplaceGlobals(oldLogger)

	body := []byte(`{"events":[{"type":"message","source":{"type":"group","groupId":"G123","userId":"U123"},"message":{"type":"text","text":"hello"}}]}`)
	filterStub := &stubTopKFilter{}
	(&WebhookService{topKFilterService: filterStub}).ProcessIncoming(body, "sig")

	// 「group + 一般文字」在目前規則下不是 command，應直接被 command gate 擋掉，
	// 因此不應進入 rerank/top-k pipeline。
	if filterStub.called {
		t.Fatalf("expected non-command message to skip rerank")
	}
	if observed.FilterMessage("line message received").Len() == 0 {
		t.Fatalf("expected incoming log")
	}
}

func TestWebhookService_ProcessIncoming_NonMessageEventStillLogsReceived(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	oldLogger := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	defer zap.ReplaceGlobals(oldLogger)

	body := []byte(`{"events":[{"type":"follow","source":{"type":"user","userId":"U123"}}]}`)
	(&WebhookService{}).ProcessIncoming(body, "sig")

	if observed.FilterMessage("line message received").Len() == 0 {
		t.Fatalf("expected raw received log for non-message event")
	}
	if observed.FilterMessage("line message unified conversion skipped").Len() != 0 {
		t.Fatalf("expected non-message event to skip unified conversion logging because it is handled by type gate")
	}
}

func TestWebhookService_ProcessIncoming_CommandMessage(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	oldLogger := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	defer zap.ReplaceGlobals(oldLogger)
	body := []byte(`{"events":[{"type":"message","source":{"type":"private","userId":"U123"},"message":{"type":"text","text":"help"}}]}`)
	filterStub := &stubTopKFilter{}
	newTestPipelineWebhookService(filterStub, &stubLLMInteraction{}, nil).ProcessIncoming(body, "sig")

	// private channel 會被視為 command 模式，應觸發 rerank 階段。
	if !filterStub.called {
		t.Fatalf("expected command message to run rerank")
	}
	if observed.FilterMessage("line message received").Len() == 0 {
		t.Fatalf("expected incoming log")
	}
}

func TestWebhookService_ProcessIncoming_FinalActionDecision(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	oldLogger := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	defer zap.ReplaceGlobals(oldLogger)

	body := []byte(`{"events":[{"type":"message","source":{"type":"private","userId":"U123"},"message":{"type":"text","text":"\u6211\u8981\u7ffb\u8b6f"}}]}`)
	filterStub := &stubTopKFilter{
		candidates: []topkfilter.ScoredCandidate{
			{Candidate: repository.ActionRouteVectorCandidate{APIOperation: "start_translation_locale", SkillCode: "channel.translation", RouteText: "\u958b\u555f\u7ffb\u8b6f"}},
		},
	}
	decisionStub := &stubLLMInteraction{decision: &llminteraction.ActionDecision{NextStep: llminteraction.NextStepExecuteAction, APIOperation: "start_translation_locale", Confidence: 0.92, Reason: "stub reason"}}

	newTestPipelineWebhookService(filterStub, decisionStub, nil).ProcessIncoming(body, "sig")

	if !decisionStub.called {
		t.Fatalf("expected final action decision to be called")
	}
	if len(decisionStub.candidates) != 1 || decisionStub.candidates[0].Operation != "start_translation_locale" {
		t.Fatalf("expected converted action candidates, got %+v", decisionStub.candidates)
	}
	entries := observed.FilterMessage("line message final action decided").All()
	if len(entries) == 0 {
		t.Fatalf("expected final action decision log")
	}
	// 驗證 log 內的候選映射資訊完整，確保日後追查可直接看到 operation 對到哪個 skill/route。
	logContext := entries[0].ContextMap()
	if logContext["skill_code"] != "channel.translation" {
		t.Fatalf("expected matched skill_code, got %v", logContext["skill_code"])
	}
	if logContext["valid_selection"] != true {
		t.Fatalf("expected valid_selection=true, got %v", logContext["valid_selection"])
	}
	if observed.FilterMessage("line message final action not in candidates").Len() != 0 {
		t.Fatalf("expected no hallucination warning when operation matches a candidate")
	}
}

func TestWebhookService_ProcessIncoming_FinalActionNotInCandidates(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	oldLogger := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	defer zap.ReplaceGlobals(oldLogger)

	body := []byte(`{"events":[{"type":"message","source":{"type":"private","userId":"U123"},"message":{"type":"text","text":"\u6211\u8981\u7ffb\u8b6f"}}]}`)
	filterStub := &stubTopKFilter{
		candidates: []topkfilter.ScoredCandidate{
			{Candidate: repository.ActionRouteVectorCandidate{APIOperation: "start_translation_locale", SkillCode: "channel.translation", RouteText: "\u958b\u555f\u7ffb\u8b6f"}},
		},
	}
	// 模擬模型回傳一個不在候選清單裡的 api_operation，驗證會被捕捉並告警。
	decisionStub := &stubLLMInteraction{decision: &llminteraction.ActionDecision{NextStep: llminteraction.NextStepExecuteAction, APIOperation: "unknown_operation", Confidence: 0.5, Reason: "stub reason"}}

	newTestPipelineWebhookService(filterStub, decisionStub, nil).ProcessIncoming(body, "sig")

	if observed.FilterMessage("line message final action not in candidates").Len() == 0 {
		t.Fatalf("expected hallucination warning when operation is not among candidates")
	}
	entries := observed.FilterMessage("line message final action decided").All()
	if len(entries) == 0 {
		t.Fatalf("expected final action decision log")
	}
	if entries[0].ContextMap()["valid_selection"] != false {
		t.Fatalf("expected valid_selection=false, got %v", entries[0].ContextMap()["valid_selection"])
	}
}

func TestWebhookService_ProcessIncoming_LowConfidenceTreatedAsChat(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	oldLogger := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	defer zap.ReplaceGlobals(oldLogger)

	oldThreshold := config.AI.LLMInteraction.CommandConfidenceThreshold
	oldQuestionThreshold := config.AI.LLMInteraction.QuestionConfidenceThreshold
	config.AI.LLMInteraction.CommandConfidenceThreshold = 0.7
	config.AI.LLMInteraction.QuestionConfidenceThreshold = 0.6
	defer func() {
		config.AI.LLMInteraction.CommandConfidenceThreshold = oldThreshold
		config.AI.LLMInteraction.QuestionConfidenceThreshold = oldQuestionThreshold
	}()

	body := []byte(`{"events":[{"type":"message","source":{"type":"private","userId":"U123"},"message":{"type":"text","text":"這個問題幫我解釋一下"}}]}`)
	filterStub := &stubTopKFilter{
		candidates: []topkfilter.ScoredCandidate{
			{Candidate: repository.ActionRouteVectorCandidate{APIOperation: "start_translation_locale", SkillCode: "channel.translation", RouteText: "開啟翻譯"}},
		},
	}
	decisionStub := &stubLLMInteraction{
		decision: &llminteraction.ActionDecision{NextStep: llminteraction.NextStepAskClarifyingQuestion, APIOperation: "start_translation_locale", Confidence: 0.42, Reason: "stub reason"},
		answer:   &llminteraction.QuestionAnswer{SchemaVersion: "v1", Answer: "我推薦《星際效應》。", Confidence: 0.35},
	}
	pushStub := &stubPushMessageService{sentPlatformMessageID: "sent-123"}

	newTestPipelineWebhookService(filterStub, decisionStub, pushStub).ProcessIncoming(body, "sig")

	// ask_clarifying_question 但無缺參數時，應走一般問答，避免追問迴圈。
	if decisionStub.clarifyCalled {
		t.Fatalf("expected AskClarifyingQuestion not to be called when no parameters are missing")
	}
	if !decisionStub.answerCalled {
		t.Fatalf("expected generic AnswerQuestion to be called on low action confidence without missing parameters")
	}
	if !pushStub.sendCalled {
		t.Fatalf("expected shared question-answer flow to send reply")
	}
	if pushStub.text != "我推薦《星際效應》。" {
		t.Fatalf("unexpected outbound answer text: %q", pushStub.text)
	}
	answerEntries := observed.FilterMessage("line message semantic question answered").All()
	if len(answerEntries) == 0 {
		t.Fatalf("expected semantic question answered log")
	}
	if answerEntries[0].ContextMap()["mode"] != "question_answer" {
		t.Fatalf("expected mode=question_answer, got %v", answerEntries[0].ContextMap()["mode"])
	}
	if answerEntries[0].ContextMap()["recommend_cloud_llm"] != true {
		t.Fatalf("expected recommend_cloud_llm=true, got %v", answerEntries[0].ContextMap()["recommend_cloud_llm"])
	}
	if observed.FilterMessage("line message semantic answer suggests cloud llm fallback").Len() == 0 {
		t.Fatalf("expected cloud llm fallback warning log")
	}
	// 既然已分流到問答，final action log 不應出現。
	if observed.FilterMessage("line message final action decided").Len() != 0 {
		t.Fatalf("expected no final action log when confidence is below threshold")
	}
}

func TestWebhookService_ProcessIncoming_ZeroConfidenceNoMatchRoutesToQuestionAnswer(t *testing.T) {
	// 共用流程已將 no-match 的 ask_clarifying_question 降為一般問答，
	// 這裡確認 LINE 也會跟著 shared behavior 發送回覆。
	core, observed := observer.New(zapcore.DebugLevel)
	oldLogger := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	defer zap.ReplaceGlobals(oldLogger)

	oldCommandThreshold := config.AI.LLMInteraction.CommandConfidenceThreshold
	oldQuestionThreshold := config.AI.LLMInteraction.QuestionConfidenceThreshold
	config.AI.LLMInteraction.CommandConfidenceThreshold = 0.7
	config.AI.LLMInteraction.QuestionConfidenceThreshold = 0.6
	defer func() {
		config.AI.LLMInteraction.CommandConfidenceThreshold = oldCommandThreshold
		config.AI.LLMInteraction.QuestionConfidenceThreshold = oldQuestionThreshold
	}()

	body := []byte(`{"events":[{"type":"message","source":{"type":"private","userId":"U123"},"message":{"type":"text","text":"推薦一部電影"}}]}`)
	filterStub := &stubTopKFilter{
		candidates: []topkfilter.ScoredCandidate{
			{Candidate: repository.ActionRouteVectorCandidate{APIOperation: "start_translation_locale", SkillCode: "channel.translation", RouteText: "開啟翻譯"}},
		},
	}
	decisionStub := &stubLLMInteraction{
		decision: &llminteraction.ActionDecision{NextStep: llminteraction.NextStepAskClarifyingQuestion, APIOperation: "", Confidence: 0.0, Reason: "no candidate matched"},
		answer:   &llminteraction.QuestionAnswer{SchemaVersion: "v1", Answer: "你可以看《星際效應》。", Confidence: 0.88},
	}
	pushStub := &stubPushMessageService{sentPlatformMessageID: "sent-123"}

	newTestPipelineWebhookService(filterStub, decisionStub, pushStub).ProcessIncoming(body, "sig")

	// no_match 且無缺參數時，應直接走一般問答，避免追問迴圈。
	if decisionStub.clarifyCalled {
		t.Fatalf("expected AskClarifyingQuestion not to be called when no parameters are missing")
	}
	if !decisionStub.answerCalled {
		t.Fatalf("expected generic AnswerQuestion to be called when action decision confidence is 0")
	}
	if !pushStub.sendCalled {
		t.Fatalf("expected shared question-answer flow to send reply")
	}
	entries := observed.FilterMessage("line message semantic question answered").All()
	if len(entries) == 0 {
		t.Fatalf("expected semantic question answered log")
	}
	if entries[0].ContextMap()["cause"] != "answer_question" {
		t.Fatalf("expected cause=answer_question after shared downgrade, got %v", entries[0].ContextMap()["cause"])
	}
	if entries[0].ContextMap()["mode"] != "question_answer" {
		t.Fatalf("expected mode=question_answer, got %v", entries[0].ContextMap()["mode"])
	}
	if observed.FilterMessage("line message final action decided").Len() != 0 {
		t.Fatalf("expected no final action log when action decision is zero-confidence no-match")
	}
}

func TestWebhookService_ProcessIncoming_AnswerQuestionNextStepUsesGenericQA(t *testing.T) {
	// next_step=answer_question 時，不應再走追問分支，而是直接送出一般回答。
	core, observed := observer.New(zapcore.DebugLevel)
	oldLogger := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	defer zap.ReplaceGlobals(oldLogger)

	body := []byte(`{"events":[{"type":"message","source":{"type":"private","userId":"U123"},"message":{"type":"text","text":"推薦一部電影"}}]}`)
	filterStub := &stubTopKFilter{
		candidates: []topkfilter.ScoredCandidate{
			{Candidate: repository.ActionRouteVectorCandidate{APIOperation: "start_translation_locale", SkillCode: "channel.translation", RouteText: "開啟翻譯"}},
		},
	}
	decisionStub := &stubLLMInteraction{
		decision: &llminteraction.ActionDecision{NextStep: llminteraction.NextStepAnswerQuestion, APIOperation: "", Confidence: 0.88, Reason: "user is asking a general question"},
		answer:   &llminteraction.QuestionAnswer{SchemaVersion: "v1", Answer: "你可以看《星際效應》。", Confidence: 0.88},
	}
	pushStub := &stubPushMessageService{sentPlatformMessageID: "sent-123"}

	newTestPipelineWebhookService(filterStub, decisionStub, pushStub).ProcessIncoming(body, "sig")

	if !decisionStub.answerCalled {
		t.Fatalf("expected generic AnswerQuestion to be called when next_step=answer_question")
	}
	if decisionStub.clarifyCalled {
		t.Fatalf("expected AskClarifyingQuestion not to be called when next_step=answer_question")
	}
	if !pushStub.sendCalled {
		t.Fatalf("expected shared question-answer flow to send reply")
	}
	entries := observed.FilterMessage("line message semantic question answered").All()
	if len(entries) == 0 {
		t.Fatalf("expected semantic question answered log")
	}
	if entries[0].ContextMap()["mode"] != "question_answer" {
		t.Fatalf("expected mode=question_answer, got %v", entries[0].ContextMap()["mode"])
	}
	if entries[0].ContextMap()["cause"] != "answer_question" {
		t.Fatalf("expected cause=answer_question, got %v", entries[0].ContextMap()["cause"])
	}
	if observed.FilterMessage("line message final action decided").Len() != 0 {
		t.Fatalf("expected no final action log when next_step=answer_question")
	}
}
