package llminteraction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// routedInteractionClient 讓 action 決策、一般聊天與內部上下文分析可分別走不同 provider/model/path。
type routedInteractionClient struct {
	decision InteractionClient
	chat     InteractionClient
	// contextAnalyzer 是內部結構化分析 client，專門處理短文本與近端上下文是否相關。
	// 它不承擔使用者可見回答，避免和 chat/question_answer role 混用。
	contextAnalyzer      InteractionClient
	decisionLabel        string
	chatLabel            string
	contextAnalyzerLabel string
}

// NewRoutedInteractionClient 建立角色分流 client。
func NewRoutedInteractionClient(decision InteractionClient, decisionLabel string, chat InteractionClient, chatLabel string, contextAnalyzer InteractionClient, contextAnalyzerLabel string) (InteractionClient, error) {
	if decision == nil {
		return nil, fmt.Errorf("decision client is required")
	}
	if chat == nil {
		return nil, fmt.Errorf("chat client is required")
	}
	// context_analyzer 是必要 role；若設定遺漏就直接 fail-fast，
	// 避免 runtime 才把內部分析請求錯送到 question_answer 或 decision route。
	if contextAnalyzer == nil {
		return nil, fmt.Errorf("context analyzer client is required")
	}
	if strings.TrimSpace(decisionLabel) == "" {
		decisionLabel = "unknown-decision-profile"
	}
	if strings.TrimSpace(chatLabel) == "" {
		chatLabel = "unknown-chat-profile"
	}
	if strings.TrimSpace(contextAnalyzerLabel) == "" {
		contextAnalyzerLabel = "unknown-context-analyzer-profile"
	}
	return &routedInteractionClient{decision: decision, chat: chat, contextAnalyzer: contextAnalyzer, decisionLabel: decisionLabel, chatLabel: chatLabel, contextAnalyzerLabel: contextAnalyzerLabel}, nil
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

func (c *routedInteractionClient) AnalyzeContext(ctx context.Context, prompt string, text string) (*ContextAnalysis, error) {
	if c == nil || c.contextAnalyzer == nil {
		return nil, fmt.Errorf("context analyzer client is not initialized")
	}
	label := strings.TrimSpace(c.contextAnalyzerLabel)
	// request log 使用 context_analyzer role 名稱，讓 log 搜尋時可和 decision/chat 明確分開。
	zap.L().Info("ai request prompt",
		zap.String("role", "context_analyzer"),
		zap.String("profile", label),
		zap.String("prompt", strings.TrimSpace(prompt)),
		zap.String("text", strings.TrimSpace(text)),
	)
	result, err := c.contextAnalyzer.AnalyzeContext(ctx, prompt, text)
	if err != nil {
		return nil, fmt.Errorf("ai context analyzer profile=%s: %w", label, err)
	}
	if result == nil {
		return nil, fmt.Errorf("ai context analyzer profile=%s returned nil result", label)
	}
	// response log 只記錄結構化摘要，不展開 extracted_fields，避免把任意服務欄位寫進高噪音 log。
	zap.L().Info("ai response payload",
		zap.String("role", "context_analyzer"),
		zap.String("profile", label),
		zap.String("schema_version", strings.TrimSpace(result.SchemaVersion)),
		zap.String("decision", strings.TrimSpace(result.Decision)),
		zap.String("target_service", strings.TrimSpace(result.TargetService)),
		zap.Strings("missing_fields", append([]string(nil), result.MissingFields...)),
		zap.Float64("confidence", result.Confidence),
		zap.String("reason", strings.TrimSpace(result.Reason)),
	)
	return result, nil
}
