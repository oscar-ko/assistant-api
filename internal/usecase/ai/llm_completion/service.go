package llmcompletion

import (
	"context"
	"fmt"
	"strings"

	openaiprovider "assistant-api/internal/integration/provider/openai"
)

// Service 定義可直接對外提供的 ChatGPT 互動流程。
// 這層只做 prompt 入口與輸入正規化，不處理網路請求細節。
type Service interface {
	Ask(ctx context.Context, prompt string) (string, error)
}

type service struct {
	client openaiprovider.Client
}

// NewService 建立 LLM completion use case service。
func NewService(client openaiprovider.Client) Service {
	if client == nil {
		return nil
	}
	return &service{client: client}
}

// Ask 直接把 prompt 送到 provider，回傳模型輸出。
func (s *service) Ask(ctx context.Context, prompt string) (string, error) {
	if s == nil || s.client == nil {
		return "", fmt.Errorf("llm completion service not initialized")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is empty")
	}
	return s.client.Complete(ctx, prompt)
}
