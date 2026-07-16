package cloud

import (
	"context"

	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"
)

// Client 定義 cloud LLM 端的最小互動能力。
// 這層未來可對接 OpenAI、Gemini、Claude 等 provider。
type Client interface {
	ClassifyAction(ctx context.Context, prompt string, text string) (*llminteraction.ActionDecision, error)
	AnswerQuestion(ctx context.Context, prompt string, text string) (*llminteraction.QuestionAnswer, error)
}

// Config 集中描述 cloud LLM 端點設定。
// 目前先保留最小共通欄位，避免過早綁死單一 provider。
type Config struct {
	ServiceURL                  string
	ServiceTimeoutSeconds       int
	CommandConfidenceThreshold  float64
	QuestionConfidenceThreshold float64
}
