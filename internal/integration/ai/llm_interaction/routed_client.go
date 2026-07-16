package llminteraction

import (
	"context"
	"encoding/json"
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
	if result == nil {
		return nil, fmt.Errorf("ai decision profile=%s returned nil result", label)
	}
	actionParamsJSON := "{}"
	if len(result.ActionParams) > 0 {
		if payload, marshalErr := json.Marshal(result.ActionParams); marshalErr == nil {
			actionParamsJSON = string(payload)
		}
	}
	zap.L().Info("ai response payload",
		zap.String("role", "decision"),
		zap.String("profile", label),
		zap.String("next_step", strings.TrimSpace(result.NextStep)),
		zap.String("api_operation", strings.TrimSpace(result.APIOperation)),
		zap.String("action_params", actionParamsJSON),
		zap.Strings("missing_parameters", append([]string(nil), result.MissingParameters...)),
		zap.Float64("confidence", result.Confidence),
		zap.String("reason", strings.TrimSpace(result.Reason)),
	)
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
	if result == nil {
		return nil, fmt.Errorf("ai chat profile=%s returned nil result", label)
	}
	zap.L().Info("ai response payload",
		zap.String("role", "chat"),
		zap.String("profile", label),
		zap.String("schema_version", strings.TrimSpace(result.SchemaVersion)),
		zap.String("answer", strings.TrimSpace(result.Answer)),
		zap.Float64("confidence", result.Confidence),
	)
	return result, nil
}
