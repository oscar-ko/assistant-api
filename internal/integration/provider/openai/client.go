package openai

import "context"

// Client 定義 OpenAI provider 在本專案需要提供的最小能力。
// 實際 HTTP 呼叫與認證細節應放在此 package 的實作檔案中。
type Client interface {
	Complete(ctx context.Context, prompt string) (string, error)
}
