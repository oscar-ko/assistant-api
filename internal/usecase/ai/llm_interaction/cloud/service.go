package cloud

import (
	"context"
	"strings"

	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"
)

// Service 定義 cloud LLM 互動流程。
// 這層與 local package 保持對稱，方便未來依情境切換或 fallback。
type Service interface {
	DecideFinalAction(ctx context.Context, text string, candidates []llminteraction.ActionCandidate) (*llminteraction.ActionDecision, error)
	AnswerQuestion(ctx context.Context, text string) (*llminteraction.QuestionAnswer, error)
	AskClarifyingQuestion(ctx context.Context, text string, reason string) (*llminteraction.QuestionAnswer, error)
}

type service struct {
	client Client
}

// NewService 建立 cloud LLM 互動服務。
func NewService(client Client) Service {
	if client == nil {
		return nil
	}
	return &service{client: client}
}

func (s *service) DecideFinalAction(ctx context.Context, text string, candidates []llminteraction.ActionCandidate) (*llminteraction.ActionDecision, error) {
	if s == nil || s.client == nil {
		return nil, nil
	}
	trimmedText := strings.TrimSpace(text)
	if trimmedText == "" || len(candidates) == 0 {
		return nil, nil
	}
	return s.client.ClassifyAction(ctx, llminteraction.BuildFinalActionPrompt(candidates), trimmedText)
}

func (s *service) AnswerQuestion(ctx context.Context, text string) (*llminteraction.QuestionAnswer, error) {
	if s == nil || s.client == nil {
		return nil, nil
	}
	trimmedText := strings.TrimSpace(text)
	if trimmedText == "" {
		return nil, nil
	}
	return s.client.AnswerQuestion(ctx, llminteraction.BuildQuestionAnswerPrompt(), trimmedText)
}

func (s *service) AskClarifyingQuestion(ctx context.Context, text string, reason string) (*llminteraction.QuestionAnswer, error) {
	if s == nil || s.client == nil {
		return nil, nil
	}
	trimmedText := strings.TrimSpace(text)
	if trimmedText == "" {
		return nil, nil
	}
	return s.client.AnswerQuestion(ctx, llminteraction.BuildClarifyingQuestionPrompt(reason), trimmedText)
}
