package commanddecision

import (
	"context"
	"errors"
	"testing"

	"assistant-api/internal/ent"
	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/usecase/ai/semanticdecision"

	"github.com/google/uuid"
)

type mockCommandChain struct {
	onChain bool
	err     error
}

func (m mockCommandChain) IsCommandChainMessage(ctx context.Context, message *ent.ChannelMessage, mentionedBot bool) (bool, error) {
	_ = ctx
	_ = message
	_ = mentionedBot
	return m.onChain, m.err
}

type mockSemanticService struct {
	classification *semanticdecision.Classification
	err            error
}

func (m mockSemanticService) ClassifyMessage(ctx context.Context, message *unifiedmessage.Message, mentionedBot bool) (*semanticdecision.Classification, error) {
	_ = ctx
	_ = message
	_ = mentionedBot
	return m.classification, m.err
}

func TestDecideMessageMentionAndChain(t *testing.T) {
	message := &unifiedmessage.Message{Mentions: []unifiedmessage.Mention{{UserID: "BOT001"}}}
	saved := &ent.ChannelMessage{ID: uuid.New()}

	svc := NewService(mockCommandChain{onChain: true}, nil)
	decision := svc.DecideMessage(context.Background(), message, saved, "BOT001")

	if !decision.MentionedBot {
		t.Fatal("expected mentioned_bot=true")
	}
	if !decision.OnCommandChain {
		t.Fatal("expected on_command_chain=true")
	}
	if !decision.EffectiveMentionedBot {
		t.Fatal("expected effective_mentioned_bot=true")
	}
}

func TestDecideMessageChainErrorDoesNotBlockClassification(t *testing.T) {
	message := &unifiedmessage.Message{Text: "help"}
	saved := &ent.ChannelMessage{ID: uuid.New()}
	classification := &semanticdecision.Classification{IntentLabel: "command", Confidence: 0.8}

	svc := NewService(
		mockCommandChain{err: errors.New("chain failure")},
		mockSemanticService{classification: classification},
	)
	decision := svc.DecideMessage(context.Background(), message, saved, "BOT001")

	if decision.CommandChainError == nil {
		t.Fatal("expected command chain error")
	}
	if decision.Classification == nil {
		t.Fatal("expected classification still available")
	}
	if decision.Classification.IntentLabel != "command" {
		t.Fatalf("unexpected intent label: %s", decision.Classification.IntentLabel)
	}
}

func TestDecideMessageNilMessage(t *testing.T) {
	svc := NewService(nil, nil)
	if svc != nil {
		t.Fatal("expected nil service when both dependencies are nil")
	}

	impl := &service{}
	decision := impl.DecideMessage(context.Background(), nil, nil, "BOT001")
	if decision == nil {
		t.Fatal("expected decision")
	}
	if decision.MentionedBot || decision.OnCommandChain || decision.EffectiveMentionedBot {
		t.Fatal("expected all decision flags false for nil message")
	}
}

func TestDecisionIsCommand(t *testing.T) {
	if (&Decision{}).IsCommand() {
		t.Fatal("expected false when classification is nil")
	}

	if (&Decision{Classification: &semanticdecision.Classification{IntentLabel: "message"}}).IsCommand() {
		t.Fatal("expected false for message label")
	}

	if !(&Decision{Classification: &semanticdecision.Classification{IntentLabel: "command"}}).IsCommand() {
		t.Fatal("expected true for command label")
	}

	if !(&Decision{Classification: &semanticdecision.Classification{IntentLabel: " Command "}}).IsCommand() {
		t.Fatal("expected true for case/space-insensitive command label")
	}
}
