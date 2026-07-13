package messageintent

import (
	"context"
	"strings"

	"assistant-api/internal/integration/unifiedmessage"
)

// Service 定義可跨平台重用的訊息意圖解析流程。
type Service interface {
	ClassifyMessage(ctx context.Context, message *unifiedmessage.Message, mentionedBot bool) (*Classification, error)
}

type service struct {
	classifier Classifier
}

// NewService 建立通用意圖解析服務。
func NewService(classifier Classifier) Service {
	if classifier == nil {
		return nil
	}
	return &service{classifier: classifier}
}

// ClassifyMessage 依統一訊息內容執行 AI 意圖判讀。
func (s *service) ClassifyMessage(ctx context.Context, message *unifiedmessage.Message, mentionedBot bool) (*Classification, error) {
	if s == nil || s.classifier == nil || message == nil || !message.IsText() {
		return nil, nil
	}

	text := strings.TrimSpace(message.Text)
	if text == "" {
		return nil, nil
	}

	return s.classifier.Classify(ctx, DefaultPrompt(mentionedBot), text)
}
