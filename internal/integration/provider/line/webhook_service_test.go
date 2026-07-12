package line

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

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

func TestConsoleWebhookService_ProcessIncoming_InvalidJSON(t *testing.T) {
	oldWriter := log.Writer()
	oldFlags := log.Flags()
	defer log.SetOutput(oldWriter)
	defer log.SetFlags(oldFlags)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)

	consoleWebhookService{}.ProcessIncoming([]byte("{invalid"), "sig")

	if !strings.Contains(buf.String(), "line webhook parse failed") {
		t.Fatalf("expected parse failed log, got: %s", buf.String())
	}
}

func TestConsoleWebhookService_ProcessIncoming_TextMessage(t *testing.T) {
	oldWriter := log.Writer()
	oldFlags := log.Flags()
	defer log.SetOutput(oldWriter)
	defer log.SetFlags(oldFlags)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)

	body := []byte(`{"events":[{"type":"message","source":{"userId":"U123"},"message":{"type":"text","text":"hello"}}]}`)
	consoleWebhookService{}.ProcessIncoming(body, "sig")

	logs := buf.String()
	if !strings.Contains(logs, "line message: sender=U123, text=hello") {
		t.Fatalf("expected text message log, got: %s", logs)
	}
	if !strings.Contains(logs, "line webhook received:") {
		t.Fatalf("expected summary log, got: %s", logs)
	}
}
