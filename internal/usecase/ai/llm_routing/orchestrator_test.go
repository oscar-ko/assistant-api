package llmrouting

import (
	"context"
	"testing"

	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"
	cloudllm "assistant-api/internal/usecase/ai/llm_interaction/cloud"
)

type stubLocalInteraction struct {
	finalDecision *llminteraction.ActionDecision
	finalErr      error
	answer        *llminteraction.QuestionAnswer
	answerErr     error
	clarify       *llminteraction.QuestionAnswer
	clarifyErr    error
	finalCalled   bool
	answerCalled  bool
	clarifyCalled bool
}

func (s *stubLocalInteraction) DecideFinalAction(ctx context.Context, text string, candidates []llminteraction.ActionCandidate) (*llminteraction.ActionDecision, error) {
	_ = ctx
	_ = text
	_ = candidates
	s.finalCalled = true
	return s.finalDecision, s.finalErr
}

func (s *stubLocalInteraction) AnswerQuestion(ctx context.Context, text string) (*llminteraction.QuestionAnswer, error) {
	_ = ctx
	_ = text
	s.answerCalled = true
	return s.answer, s.answerErr
}

func (s *stubLocalInteraction) AskClarifyingQuestion(ctx context.Context, text string, reason string) (*llminteraction.QuestionAnswer, error) {
	_ = ctx
	_ = text
	_ = reason
	s.clarifyCalled = true
	return s.clarify, s.clarifyErr
}

type stubCloudService struct {
	finalDecision *llminteraction.ActionDecision
	finalErr      error
	answer        *llminteraction.QuestionAnswer
	answerErr     error
	clarify       *llminteraction.QuestionAnswer
	clarifyErr    error
	finalCalled   bool
	answerCalled  bool
	clarifyCalled bool
}

func (s *stubCloudService) DecideFinalAction(ctx context.Context, text string, candidates []llminteraction.ActionCandidate) (*llminteraction.ActionDecision, error) {
	_ = ctx
	_ = text
	_ = candidates
	s.finalCalled = true
	return s.finalDecision, s.finalErr
}

func (s *stubCloudService) AnswerQuestion(ctx context.Context, text string) (*llminteraction.QuestionAnswer, error) {
	_ = ctx
	_ = text
	s.answerCalled = true
	return s.answer, s.answerErr
}

func (s *stubCloudService) AskClarifyingQuestion(ctx context.Context, text string, reason string) (*llminteraction.QuestionAnswer, error) {
	_ = ctx
	_ = text
	_ = reason
	s.clarifyCalled = true
	return s.clarify, s.clarifyErr
}

func TestOrchestratorFallsBackToCloudOnLowConfidenceAction(t *testing.T) {
	local := &stubLocalInteraction{
		finalDecision: &llminteraction.ActionDecision{Confidence: 0.2, APIOperation: "local_operation"},
	}
	cloud := &stubCloudService{
		finalDecision: &llminteraction.ActionDecision{Confidence: 0.9, APIOperation: "cloud_operation"},
	}
	orchestrator := NewOrchestrator(local, cloud, RouterConfig{CommandConfidenceThreshold: 0.7})
	if orchestrator == nil {
		t.Fatal("expected orchestrator")
	}

	decision, err := orchestrator.DecideFinalAction(context.Background(), "hello", []llminteraction.ActionCandidate{{Operation: "local_operation"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision == nil || decision.APIOperation != "cloud_operation" {
		t.Fatalf("expected cloud fallback decision, got %+v", decision)
	}
	if !local.finalCalled || !cloud.finalCalled {
		t.Fatalf("expected both local and cloud decision paths to be called")
	}
}

func TestOrchestratorKeepsLocalAnswerWhenConfidenceIsHigh(t *testing.T) {
	local := &stubLocalInteraction{
		answer: &llminteraction.QuestionAnswer{Confidence: 0.85, Answer: "local answer"},
	}
	cloud := &stubCloudService{
		answer: &llminteraction.QuestionAnswer{Confidence: 0.99, Answer: "cloud answer"},
	}
	orchestrator := NewOrchestrator(local, cloud, RouterConfig{QuestionConfidenceThreshold: 0.6})
	if orchestrator == nil {
		t.Fatal("expected orchestrator")
	}

	answer, err := orchestrator.AnswerQuestion(context.Background(), "question")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if answer == nil || answer.Answer != "local answer" {
		t.Fatalf("expected local answer, got %+v", answer)
	}
	if !local.answerCalled {
		t.Fatalf("expected local answer path to be called")
	}
	if cloud.answerCalled {
		t.Fatalf("expected cloud answer path not to be called when local confidence is high")
	}
}

func TestOrchestratorFallsBackToCloudClarifyingQuestionWhenLocalMissing(t *testing.T) {
	local := &stubLocalInteraction{}
	cloud := &stubCloudService{
		clarify: &llminteraction.QuestionAnswer{Confidence: 0.88, Answer: "cloud clarification"},
	}
	orchestrator := NewOrchestrator(local, cloud, RouterConfig{QuestionConfidenceThreshold: 0.6})
	if orchestrator == nil {
		t.Fatal("expected orchestrator")
	}

	answer, err := orchestrator.AskClarifyingQuestion(context.Background(), "question", "reason")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if answer == nil || answer.Answer != "cloud clarification" {
		t.Fatalf("expected cloud clarification, got %+v", answer)
	}
	if !local.clarifyCalled || !cloud.clarifyCalled {
		t.Fatalf("expected local and cloud clarifying paths to be called")
	}
}

var _ cloudllm.Service = (*stubCloudService)(nil)
