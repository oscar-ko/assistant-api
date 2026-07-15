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
}

type service struct {
	classifier Classifier
}

// NewService 建立通用語意決策服務。
func NewService(classifier Classifier) Service {
	if classifier == nil {
		return nil
	}
	return &service{classifier: classifier}
}

// DecideFinalAction 把 reranker 精排後的候選（含 route_text/skill/score 描述）組成文字提示，
// 交給語意決策模型做最後判斷，選出唯一一個 action。
// 回傳的 ActionDecision.APIOperation 即為最終選定的 action operation。
func (s *service) DecideFinalAction(ctx context.Context, text string, candidates []ActionCandidate) (*ActionDecision, error) {
	if s == nil || s.classifier == nil {
		return nil, nil
	}
	trimmedText := strings.TrimSpace(text)
	if trimmedText == "" || len(candidates) == 0 {
		return nil, nil
	}

	return s.classifier.ClassifyAction(ctx, BuildFinalActionPrompt(candidates), trimmedText)
}
