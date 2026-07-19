package line

import (
	"testing"

	"assistant-api/internal/config"
)

func TestAdaptLineEventToUnified_MessageEvent(t *testing.T) {
	originalBotUserID := config.Line.BotUserID
	config.Line.BotUserID = "BOT001"
	defer func() { config.Line.BotUserID = originalBotUserID }()

	event := webhookEvent{
		Type: "message",
		Source: webhookEventSource{
			Type:    "group",
			UserID:  "U123",
			GroupID: "G456",
		},
		Message: webhookMessage{
			ID:              "M789",
			Type:            "text",
			Text:            "@Jarvis hello",
			QuotedMessageID: "M100",
			Mention: &webhookMessageMention{
				Mentionees: []webhookMentionee{{Type: "user", Index: 0, Length: 7, UserID: "BOT001"}},
			},
		},
		Timestamp: 123456,
	}

	msg, ok, reason := adaptLineEventToUnified(event)
	if !ok || msg == nil {
		t.Fatalf("expected unified message")
	}
	if reason != "" {
		t.Fatalf("expected empty skip reason, got %q", reason)
	}
	if msg.Platform != "line" {
		t.Fatalf("unexpected platform: %q", msg.Platform)
	}
	if msg.ChannelID != "G456" {
		t.Fatalf("unexpected channel id: %q", msg.ChannelID)
	}
	if msg.ReplyToMsgID != "M100" {
		t.Fatalf("unexpected reply message id: %q", msg.ReplyToMsgID)
	}
	if !msg.MentionsUser("BOT001") {
		t.Fatalf("expected mention BOT001")
	}
	if len(msg.Mentions) != 1 {
		t.Fatalf("expected one mention, got %d", len(msg.Mentions))
	}
	mention := msg.Mentions[0]
	if mention.DisplayText != "@Jarvis" {
		t.Fatalf("unexpected mention display text: %q", mention.DisplayText)
	}
	if mention.Index == nil || *mention.Index != 0 {
		t.Fatalf("unexpected mention index: %#v", mention.Index)
	}
	if mention.Length == nil || *mention.Length != 7 {
		t.Fatalf("unexpected mention length: %#v", mention.Length)
	}
	if mention.Type != "user" || mention.IdentityKind != "bot" || !mention.IsBot {
		t.Fatalf("unexpected bot mention metadata: %#v", mention)
	}
}

func TestAdaptLineEventToUnified_NonMessage(t *testing.T) {
	event := webhookEvent{Type: "follow"}
	msg, ok, reason := adaptLineEventToUnified(event)
	if ok || msg != nil {
		t.Fatalf("expected non-message event to be ignored")
	}
	if reason == "" {
		t.Fatalf("expected skip reason")
	}
}
