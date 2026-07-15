package line

import (
	"context"
	"testing"

	"assistant-api/internal/config"
	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"
	"assistant-api/internal/usecase/ai/semanticdecision"
	"assistant-api/internal/usecase/ai/topkfilter"

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

type stubSemanticDecision struct {
	called       bool
	decision     *semanticdecision.ActionDecision
	err          error
	candidates   []semanticdecision.ActionCandidate
	answerCalled bool
	answer       *semanticdecision.QuestionAnswer
	answerErr    error
}

func (s *stubSemanticDecision) DecideFinalAction(ctx context.Context, text string, candidates []semanticdecision.ActionCandidate) (*semanticdecision.ActionDecision, error) {
	_ = ctx
	_ = text
	s.called = true
	s.candidates = candidates
	return s.decision, s.err
}

func (s *stubSemanticDecision) AnswerQuestion(ctx context.Context, text string) (*semanticdecision.QuestionAnswer, error) {
	_ = ctx
	_ = text
	s.answerCalled = true
	return s.answer, s.answerErr
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
	(&WebhookService{topKFilterService: filterStub}).ProcessIncoming(body, "sig")

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
	decisionStub := &stubSemanticDecision{decision: &semanticdecision.ActionDecision{APIOperation: "start_translation_locale", Confidence: 0.92, Reason: "stub reason"}}

	(&WebhookService{topKFilterService: filterStub, semanticService: decisionStub}).ProcessIncoming(body, "sig")

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
	decisionStub := &stubSemanticDecision{decision: &semanticdecision.ActionDecision{APIOperation: "unknown_operation", Confidence: 0.5, Reason: "stub reason"}}

	(&WebhookService{topKFilterService: filterStub, semanticService: decisionStub}).ProcessIncoming(body, "sig")

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

	oldThreshold := config.AI.SemanticDecision.CommandConfidenceThreshold
	oldQuestionThreshold := config.AI.SemanticDecision.QuestionConfidenceThreshold
	config.AI.SemanticDecision.CommandConfidenceThreshold = 0.7
	config.AI.SemanticDecision.QuestionConfidenceThreshold = 0.6
	defer func() {
		config.AI.SemanticDecision.CommandConfidenceThreshold = oldThreshold
		config.AI.SemanticDecision.QuestionConfidenceThreshold = oldQuestionThreshold
	}()

	body := []byte(`{"events":[{"type":"message","source":{"type":"private","userId":"U123"},"message":{"type":"text","text":"這個問題幫我解釋一下"}}]}`)
	filterStub := &stubTopKFilter{
		candidates: []topkfilter.ScoredCandidate{
			{Candidate: repository.ActionRouteVectorCandidate{APIOperation: "start_translation_locale", SkillCode: "channel.translation", RouteText: "開啟翻譯"}},
		},
	}
	decisionStub := &stubSemanticDecision{
		decision: &semanticdecision.ActionDecision{APIOperation: "start_translation_locale", Confidence: 0.42, Reason: "stub reason"},
		answer:   &semanticdecision.QuestionAnswer{SchemaVersion: "v1", Answer: "這是一個問題的回覆", Confidence: 0.35},
	}

	(&WebhookService{topKFilterService: filterStub, semanticService: decisionStub}).ProcessIncoming(body, "sig")

	// 低 action confidence 應走問答分支，而不是產生最終 action。
	if !decisionStub.answerCalled {
		t.Fatalf("expected AnswerQuestion to be called on low action confidence")
	}
	answerEntries := observed.FilterMessage("line message semantic question answered").All()
	if len(answerEntries) == 0 {
		t.Fatalf("expected semantic question answered log")
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
	core, observed := observer.New(zapcore.DebugLevel)
	oldLogger := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	defer zap.ReplaceGlobals(oldLogger)

	oldCommandThreshold := config.AI.SemanticDecision.CommandConfidenceThreshold
	oldQuestionThreshold := config.AI.SemanticDecision.QuestionConfidenceThreshold
	config.AI.SemanticDecision.CommandConfidenceThreshold = 0.7
	config.AI.SemanticDecision.QuestionConfidenceThreshold = 0.6
	defer func() {
		config.AI.SemanticDecision.CommandConfidenceThreshold = oldCommandThreshold
		config.AI.SemanticDecision.QuestionConfidenceThreshold = oldQuestionThreshold
	}()

	body := []byte(`{"events":[{"type":"message","source":{"type":"private","userId":"U123"},"message":{"type":"text","text":"推薦一部電影"}}]}`)
	filterStub := &stubTopKFilter{
		candidates: []topkfilter.ScoredCandidate{
			{Candidate: repository.ActionRouteVectorCandidate{APIOperation: "start_translation_locale", SkillCode: "channel.translation", RouteText: "開啟翻譯"}},
		},
	}
	decisionStub := &stubSemanticDecision{
		decision: &semanticdecision.ActionDecision{APIOperation: "", Confidence: 0.0, Reason: "no candidate matched"},
		answer:   &semanticdecision.QuestionAnswer{SchemaVersion: "v1", Answer: "你可以看《星際效應》。", Confidence: 0.88},
	}

	(&WebhookService{topKFilterService: filterStub, semanticService: decisionStub}).ProcessIncoming(body, "sig")

	// no_match 被視為正常語意結果，必須改走問答分支。
	if !decisionStub.answerCalled {
		t.Fatalf("expected AnswerQuestion to be called when action decision confidence is 0")
	}
	entries := observed.FilterMessage("line message semantic question answered").All()
	if len(entries) == 0 {
		t.Fatalf("expected semantic question answered log")
	}
	if entries[0].ContextMap()["cause"] != "low_action_confidence" {
		t.Fatalf("expected cause=low_action_confidence, got %v", entries[0].ContextMap()["cause"])
	}
	if observed.FilterMessage("line message final action decided").Len() != 0 {
		t.Fatalf("expected no final action log when action decision is zero-confidence no-match")
	}
}
