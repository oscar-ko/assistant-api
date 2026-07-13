package line

import "testing"

func TestAdaptLineEventToUnified_MessageEvent(t *testing.T) {
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
			Text:            "hello",
			QuotedMessageID: "M100",
			Mention: &webhookMessageMention{
				Mentionees: []webhookMentionee{{UserID: "BOT001"}},
			},
		},
		Timestamp: 123456,
	}

	msg, ok := adaptLineEventToUnified(event)
	if !ok || msg == nil {
		t.Fatalf("expected unified message")
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
}

func TestAdaptLineEventToUnified_NonMessage(t *testing.T) {
	event := webhookEvent{Type: "follow"}
	msg, ok := adaptLineEventToUnified(event)
	if ok || msg != nil {
		t.Fatalf("expected non-message event to be ignored")
	}
}
