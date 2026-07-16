package llmrouting

import (
	"context"

	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"
	cloudllm "assistant-api/internal/usecase/ai/llm_interaction/cloud"
)

// RouterConfig 控制 local 與 cloud 之間的接手門檻。
// 0 代表關閉該門檻判斷。
type RouterConfig struct {
	CommandConfidenceThreshold  float64
	QuestionConfidenceThreshold float64
}

// Orchestrator 定義 local / cloud LLM 的上層路由流程。
type Orchestrator interface {
	DecideFinalAction(ctx context.Context, text string, candidates []llminteraction.ActionCandidate) (*llminteraction.ActionDecision, error)
	AnswerQuestion(ctx context.Context, text string) (*llminteraction.QuestionAnswer, error)
	AskClarifyingQuestion(ctx context.Context, text string, reason string) (*llminteraction.QuestionAnswer, error)
}

type orchestrator struct {
	local  llminteraction.InteractionService
	cloud  cloudllm.Service
	config RouterConfig
}

// NewOrchestrator 建立 local 優先、cloud 接手的 LLM 路由器。
func NewOrchestrator(local llminteraction.InteractionService, cloud cloudllm.Service, config RouterConfig) Orchestrator {
	if local == nil && cloud == nil {
		return nil
	}
	return &orchestrator{
		local:  local,
		cloud:  cloud,
		config: config,
	}
}

func (o *orchestrator) DecideFinalAction(ctx context.Context, text string, candidates []llminteraction.ActionCandidate) (*llminteraction.ActionDecision, error) {
	if o == nil {
		return nil, nil
	}
	localDecision, localErr := o.localResultForFinalAction(ctx, text, candidates)
	if !shouldFallback(localErr, localDecision == nil, o.config.CommandConfidenceThreshold, confidenceOf(localDecision)) || o.cloud == nil {
		return localDecision, localErr
	}

	cloudDecision, cloudErr := o.cloud.DecideFinalAction(ctx, text, candidates)
	if cloudErr == nil && cloudDecision != nil {
		return cloudDecision, nil
	}
	if localDecision != nil {
		return localDecision, localErr
	}
	return cloudDecision, cloudErr
}

func (o *orchestrator) AnswerQuestion(ctx context.Context, text string) (*llminteraction.QuestionAnswer, error) {
	if o == nil {
		return nil, nil
	}
	localAnswer, localErr := o.localAnswerQuestion(ctx, text)
	if !shouldFallback(localErr, localAnswer == nil, o.config.QuestionConfidenceThreshold, confidenceOfAnswer(localAnswer)) || o.cloud == nil {
		return localAnswer, localErr
	}

	cloudAnswer, cloudErr := o.cloud.AnswerQuestion(ctx, text)
	if cloudErr == nil && cloudAnswer != nil {
		return cloudAnswer, nil
	}
	if localAnswer != nil {
		return localAnswer, localErr
	}
	return cloudAnswer, cloudErr
}

func (o *orchestrator) AskClarifyingQuestion(ctx context.Context, text string, reason string) (*llminteraction.QuestionAnswer, error) {
	if o == nil {
		return nil, nil
	}
	localAnswer, localErr := o.localClarifyingQuestion(ctx, text, reason)
	if !shouldFallback(localErr, localAnswer == nil, o.config.QuestionConfidenceThreshold, confidenceOfAnswer(localAnswer)) || o.cloud == nil {
		return localAnswer, localErr
	}

	cloudAnswer, cloudErr := o.cloud.AskClarifyingQuestion(ctx, text, reason)
	if cloudErr == nil && cloudAnswer != nil {
		return cloudAnswer, nil
	}
	if localAnswer != nil {
		return localAnswer, localErr
	}
	return cloudAnswer, cloudErr
}

func (o *orchestrator) localResultForFinalAction(ctx context.Context, text string, candidates []llminteraction.ActionCandidate) (*llminteraction.ActionDecision, error) {
	if o.local == nil {
		return nil, nil
	}
	return o.local.DecideFinalAction(ctx, text, candidates)
}

func (o *orchestrator) localAnswerQuestion(ctx context.Context, text string) (*llminteraction.QuestionAnswer, error) {
	if o.local == nil {
		return nil, nil
	}
	return o.local.AnswerQuestion(ctx, text)
}

func (o *orchestrator) localClarifyingQuestion(ctx context.Context, text string, reason string) (*llminteraction.QuestionAnswer, error) {
	if o.local == nil {
		return nil, nil
	}
	return o.local.AskClarifyingQuestion(ctx, text, reason)
}

func shouldFallback(err error, missing bool, threshold float64, confidence float64) bool {
	if err != nil || missing {
		return true
	}
	if threshold <= 0 {
		return false
	}
	return confidence < threshold
}

func confidenceOf(decision *llminteraction.ActionDecision) float64 {
	if decision == nil {
		return 0
	}
	return decision.Confidence
}

func confidenceOfAnswer(answer *llminteraction.QuestionAnswer) float64 {
	if answer == nil {
		return 0
	}
	return answer.Confidence
}
