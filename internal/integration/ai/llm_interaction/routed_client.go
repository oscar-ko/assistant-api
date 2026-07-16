package llminteraction

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// routedInteractionClient 讓 action 決策與聊天可分別走不同 provider/model。
type routedInteractionClient struct {
	decision      InteractionClient
	chat          InteractionClient
	decisionLabel string
	chatLabel     string
}

// NewRoutedInteractionClient 建立角色分流 client。
func NewRoutedInteractionClient(decision InteractionClient, decisionLabel string, chat InteractionClient, chatLabel string) (InteractionClient, error) {
	if decision == nil {
		return nil, fmt.Errorf("decision client is required")
	}
	if chat == nil {
		return nil, fmt.Errorf("chat client is required")
	}
	if strings.TrimSpace(decisionLabel) == "" {
		decisionLabel = "unknown-decision-profile"
	}
	if strings.TrimSpace(chatLabel) == "" {
		chatLabel = "unknown-chat-profile"
	}
	return &routedInteractionClient{decision: decision, chat: chat, decisionLabel: decisionLabel, chatLabel: chatLabel}, nil
}

func (c *routedInteractionClient) ClassifyAction(ctx context.Context, prompt string, text string) (*ActionDecision, error) {
	if c == nil || c.decision == nil {
		return nil, fmt.Errorf("decision client is not initialized")
	}
	label := strings.TrimSpace(c.decisionLabel)
	zap.L().Info("ai request prompt",
		zap.String("role", "decision"),
		zap.String("profile", label),
		zap.String("prompt", strings.TrimSpace(prompt)),
		zap.String("text", strings.TrimSpace(text)),
	)
	result, err := c.decision.ClassifyAction(ctx, prompt, text)
	if err != nil {
		return nil, fmt.Errorf("ai decision profile=%s: %w", label, err)
	}
	return result, nil
}

func (c *routedInteractionClient) AnswerQuestion(ctx context.Context, prompt string, text string) (*QuestionAnswer, error) {
	if c == nil || c.chat == nil {
		return nil, fmt.Errorf("chat client is not initialized")
	}
	label := strings.TrimSpace(c.chatLabel)
	zap.L().Info("ai request prompt",
		zap.String("role", "chat"),
		zap.String("profile", label),
		zap.String("prompt", strings.TrimSpace(prompt)),
		zap.String("text", strings.TrimSpace(text)),
	)
	result, err := c.chat.AnswerQuestion(ctx, prompt, text)
	if err != nil {
		return nil, fmt.Errorf("ai chat profile=%s: %w", label, err)
	}
	return result, nil
}
