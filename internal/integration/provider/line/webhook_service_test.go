package line

import (
	"context"
	"testing"

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
	called     bool
	decision   *semanticdecision.ActionDecision
	err        error
	candidates []semanticdecision.ActionCandidate
}

func (s *stubSemanticDecision) ClassifyMessage(ctx context.Context, message *unifiedmessage.Message, mentionedBot bool) (*semanticdecision.Classification, error) {
	_ = ctx
	_ = message
	_ = mentionedBot
	return nil, nil
}

func (s *stubSemanticDecision) DecideFinalAction(ctx context.Context, text string, candidates []semanticdecision.ActionCandidate) (*semanticdecision.ActionDecision, error) {
	_ = ctx
	_ = text
	s.called = true
	s.candidates = candidates
	return s.decision, s.err
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
	if observed.FilterMessage("line message final action decided").Len() == 0 {
		t.Fatalf("expected final action decision log")
	}
}
