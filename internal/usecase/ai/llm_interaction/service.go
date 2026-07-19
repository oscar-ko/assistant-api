package llminteraction

import (
	"context"
	"strings"
)

// InteractionService 定義可跨平台重用的 LLM 互動流程。
type InteractionService interface {
	// DecideFinalAction 依 reranker 篩選後的候選清單與原始訊息文字，
	// 讓 LLM 從候選中選出最終應執行的單一 action。
	DecideFinalAction(ctx context.Context, text string, candidates []ActionCandidate) (*ActionDecision, error)
	// AnswerQuestion 把訊息當作一般問題，回傳答案與信心度。
	AnswerQuestion(ctx context.Context, text string) (*QuestionAnswer, error)
	// AnalyzeContext 以呼叫端提供的 prompt 分析訊息與近端上下文，回傳內部結構化判斷。
	AnalyzeContext(ctx context.Context, prompt string, text string) (*ContextAnalysis, error)
	// AskClarifyingQuestion 在 action 決策信心不足時，依原訊息與決策理由生成追問問題。
	AskClarifyingQuestion(ctx context.Context, text string, reason string) (*QuestionAnswer, error)
}

type interactionService struct {
	client InteractionClient
}

// NewInteractionService 建立通用 LLM 互動服務。
func NewInteractionService(client InteractionClient) InteractionService {
	if client == nil {
		return nil
	}
	return &interactionService{client: client}
}

// DecideFinalAction 把 reranker 精排後的候選（含 route_text/skill/score 描述）組成文字提示，
// 交給語意決策模型做最後判斷，選出唯一一個 action。
// 回傳的 ActionDecision.APIOperation 即為最終選定的 action operation。
func (s *interactionService) DecideFinalAction(ctx context.Context, text string, candidates []ActionCandidate) (*ActionDecision, error) {
	if s == nil || s.client == nil {
		// 維持「可安全退化」：當服務尚未注入時，呼叫端可選擇直接略過互動決策階段。
		return nil, nil
	}
	trimmedText := strings.TrimSpace(text)
	if trimmedText == "" || len(candidates) == 0 {
		// 沒有文字或沒有候選時不進模型，避免浪費 token 並降低不必要誤判。
		return nil, nil
	}

	return s.client.ClassifyAction(ctx, BuildFinalActionPrompt(candidates), trimmedText)
}

// AnswerQuestion 把訊息視為一般問答問題，交給語意服務回覆 answer + confidence。
func (s *interactionService) AnswerQuestion(ctx context.Context, text string) (*QuestionAnswer, error) {
	if s == nil || s.client == nil {
		// 保持可安全退化：服務未注入時，不讓流程 panic。
		return nil, nil
	}
	trimmedText := strings.TrimSpace(text)
	if trimmedText == "" {
		// 空訊息不送問答模型，避免無意義呼叫。
		return nil, nil
	}

	return s.client.AnswerQuestion(ctx, BuildQuestionAnswerPrompt(), trimmedText)
}

// AnalyzeContext 把呼叫端建好的上下文 prompt 與目前訊息交給 dedicated context analyzer。
// prompt 由使用場景決定，例如 implicit reply linker 會放入近端訊息候選；
// service 層只負責空值檢查與轉送，不在這裡加入場景規則。
func (s *interactionService) AnalyzeContext(ctx context.Context, prompt string, text string) (*ContextAnalysis, error) {
	if s == nil || s.client == nil {
		return nil, nil
	}
	trimmedPrompt := strings.TrimSpace(prompt)
	trimmedText := strings.TrimSpace(text)
	if trimmedPrompt == "" || trimmedText == "" {
		return nil, nil
	}

	return s.client.AnalyzeContext(ctx, trimmedPrompt, trimmedText)
}

// AskClarifyingQuestion 在無法安全執行 action 時，要求模型提出一個最小必要追問。
func (s *interactionService) AskClarifyingQuestion(ctx context.Context, text string, reason string) (*QuestionAnswer, error) {
	if s == nil || s.client == nil {
		return nil, nil
	}
	trimmedText := strings.TrimSpace(text)
	if trimmedText == "" {
		return nil, nil
	}

	return s.client.AnswerQuestion(ctx, BuildClarifyingQuestionPrompt(reason), trimmedText)
}
