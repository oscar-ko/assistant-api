package line

import (
	"context"
	"testing"

	"assistant-api/internal/integration/unifiedmessage"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type stubTopKFilter struct {
	called bool
}

func (s *stubTopKFilter) FilterMessage(ctx context.Context, message *unifiedmessage.Message) {
	_ = ctx
	_ = message
	s.called = true
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
