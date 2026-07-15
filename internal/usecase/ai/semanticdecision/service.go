package semanticdecision

import (
	"context"
	"strings"
)

// Service 定義可跨平台重用的語意決策流程。
type Service interface {
	// DecideFinalAction 依 reranker 篩選後的候選清單與原始訊息文字，
	// 讓語意決策模型從候選中選出最終應執行的單一 action。
	DecideFinalAction(ctx context.Context, text string, candidates []ActionCandidate) (*ActionDecision, error)
	// AnswerQuestion 把訊息當作一般問題，回傳答案與信心度。
	AnswerQuestion(ctx context.Context, text string) (*QuestionAnswer, error)
}

type service struct {
	client Client
}

// NewService 建立通用語意決策服務。
func NewService(client Client) Service {
	if client == nil {
		return nil
	}
	return &service{client: client}
}

// DecideFinalAction 把 reranker 精排後的候選（含 route_text/skill/score 描述）組成文字提示，
// 交給語意決策模型做最後判斷，選出唯一一個 action。
// 回傳的 ActionDecision.APIOperation 即為最終選定的 action operation。
func (s *service) DecideFinalAction(ctx context.Context, text string, candidates []ActionCandidate) (*ActionDecision, error) {
	if s == nil || s.client == nil {
		// 維持「可安全退化」：當服務尚未注入時，呼叫端可選擇直接略過語意決策階段。
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
func (s *service) AnswerQuestion(ctx context.Context, text string) (*QuestionAnswer, error) {
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
