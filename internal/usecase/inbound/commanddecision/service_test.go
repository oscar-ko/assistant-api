package commanddecision

import (
	"context"
	"testing"

	"assistant-api/internal/ent"
	"assistant-api/internal/integration/unifiedmessage"

	"github.com/google/uuid"
)

type mockCommandChain struct {
	onChain bool
	err     error
	called  bool
}

func (m *mockCommandChain) IsCommandChainMessage(ctx context.Context, message *ent.ChannelMessage, mentionedBot bool) (bool, error) {
	_ = ctx
	_ = message
	_ = mentionedBot
	m.called = true
	return m.onChain, m.err
}

func TestDecideMessageMentionAndChain(t *testing.T) {
	message := &unifiedmessage.Message{Mentions: []unifiedmessage.Mention{{UserID: "BOT001"}}}
	saved := &ent.ChannelMessage{ID: uuid.New()}

	chain := &mockCommandChain{onChain: true}
	svc := NewService(chain)
	decision := svc.DecideMessage(context.Background(), message, saved, "BOT001")

	if !decision.IsMentionedBot {
		t.Fatal("expected mentioned_bot=true")
	}
	if !decision.IsOnCommandChain {
		t.Fatal("expected on_command_chain=true")
	}
	if !decision.IsEffectiveMentionedBot {
		t.Fatal("expected effective_mentioned_bot=true")
	}
	if !decision.IsCommand() {
		t.Fatal("expected command mode to be true")
	}
	if !chain.called {
		t.Fatal("expected command chain to be called")
	}
}

func TestDecideMessageChainErrorDoesNotBlockCommandMode(t *testing.T) {
	message := &unifiedmessage.Message{Text: "help", Mentions: []unifiedmessage.Mention{{UserID: "BOT001"}}}
	saved := &ent.ChannelMessage{ID: uuid.New()}

	chain := &mockCommandChain{err: context.Canceled}
	svc := NewService(chain)
	decision := svc.DecideMessage(context.Background(), message, saved, "BOT001")

	if decision.CommandChainError == nil {
		t.Fatal("expected command chain error")
	}
	if !decision.IsCommand() {
		t.Fatal("expected command mode to stay true")
	}
	if !chain.called {
		t.Fatal("expected command chain to be called")
	}
}

func TestDecideMessageNilMessage(t *testing.T) {
	svc := NewService(nil)
	if svc != nil {
		t.Fatal("expected nil service when dependency is nil")
	}

	impl := &service{}
	decision := impl.DecideMessage(context.Background(), nil, nil, "BOT001")
	if decision == nil {
		t.Fatal("expected decision")
	}
	if decision.IsMentionedBot || decision.IsOnCommandChain || decision.IsEffectiveMentionedBot || decision.IsCommand() {
		t.Fatal("expected all decision flags false for nil message")
	}
}

func TestDecideMessagePrivateChannelForcesCommandMode(t *testing.T) {
	message := &unifiedmessage.Message{ChannelType: "private", Text: "hello"}
	saved := &ent.ChannelMessage{ID: uuid.New()}

	decision := (&service{}).DecideMessage(context.Background(), message, saved, "BOT001")
	if decision == nil {
		t.Fatal("expected decision")
	}
	if !decision.IsPrivateChannel {
		t.Fatal("expected private channel to force command mode")
	}
	if !decision.IsCommand() {
		t.Fatal("expected private channel command mode to be true")
	}
	if !decision.IsEffectiveMentionedBot {
		t.Fatal("expected private channel to behave like effective mention")
	}
	if !decision.IsCommand() {
		t.Fatal("expected private channel decision to be command")
	}
}

func TestDecideMessageSkipsComparisonOutsideCommandMode(t *testing.T) {
	message := &unifiedmessage.Message{ChannelType: "group", Text: "hello"}
	saved := &ent.ChannelMessage{ID: uuid.New()}
	chain := &mockCommandChain{}

	decision := (&service{commandChain: chain}).DecideMessage(context.Background(), message, saved, "BOT001")
	if decision == nil {
		t.Fatal("expected decision")
	}
	if decision.IsCommand() {
		t.Fatal("expected non-command message to stay out of command mode")
	}
	if chain.called {
		t.Fatal("expected command chain to be skipped")
	}
	if decision.CommandChainError != nil || decision.IsOnCommandChain {
		t.Fatal("expected command comparison to be skipped")
	}
}

func TestDecideMessageReplyContextChecksCommandChain(t *testing.T) {
	// 非 mention、非 private 的訊息，只要具有 reply context，
	// 仍需觸發 command chain 判斷，避免補參數訊息被錯誤略過。
	parentID := uuid.New()
	message := &unifiedmessage.Message{ChannelType: "group", Text: "補參數"}
	saved := &ent.ChannelMessage{ID: uuid.New(), RelatedMessageID: &parentID}
	chain := &mockCommandChain{onChain: true}

	decision := (&service{commandChain: chain}).DecideMessage(context.Background(), message, saved, "BOT001")
	if decision == nil {
		t.Fatal("expected decision")
	}
	if !chain.called {
		t.Fatal("expected command chain to be called for reply context")
	}
	if !decision.IsOnCommandChain {
		t.Fatal("expected on_command_chain=true for reply context")
	}
	if !decision.IsCommand() {
		t.Fatal("expected reply context on command chain to enter command mode")
	}
}
