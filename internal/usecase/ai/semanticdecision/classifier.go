package semanticdecision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Classification 表示單次訊息語意決策結果。
type Classification struct {
	SchemaVersion string  `json:"schema_version"`
	IntentLabel   string  `json:"intent_label"`
	Confidence    float64 `json:"confidence"`
	Reason        string  `json:"reason"`
}

// Classifier 定義通用語意決策能力。
type Classifier interface {
	Classify(ctx context.Context, prompt string, text string) (*Classification, error)
}

type classifierClient struct {
	baseURL string
	client  *http.Client
}

type classificationRequest struct {
	Prompt string `json:"prompt"`
	Text   string `json:"text"`
}

const classificationPath = "/predict/semantic_decision"

// NewClassifier 建立通用語意決策 client。
func NewClassifier(baseURL string, timeoutSeconds int) Classifier {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return nil
	}
	return &classifierClient{
		baseURL: strings.TrimRight(trimmed, "/"),
		client:  &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
	}
}

// DefaultPrompt 回傳由 Go 端注入的通用分類提示詞。
// mentionedBot 會寫入 prompt，讓模型先套用「有 mention bot 就視為 command」的優先規則。
func DefaultPrompt(mentionedBot bool) string {
	return strings.TrimSpace(`
你是跨通訊軟體的訊息分類器。
請只根據輸入訊息與系統規則判斷它是否是「指令」或「一般訊息」。

第一個規則：如果 mentioned_bot=true，intent_label 一律視為 command。

輸出格式必須是 JSON，欄位固定如下：
schema_version, intent_label, confidence, reason

規則：
- mentioned_bot=` + strconv.FormatBool(mentionedBot) + `
- intent_label 只能是 command 或 message
- command 表示使用者希望系統執行動作、查詢、建立、更新、刪除、設定或觸發流程
- message 表示一般聊天、閒聊、回覆、通知、描述或不需要執行動作的內容
- confidence 為 0 到 1 的數字
- reason 用一句話簡述判斷依據
`)
}

func (c *classifierClient) Classify(ctx context.Context, prompt string, text string) (*Classification, error) {
	if c == nil {
		return nil, fmt.Errorf("semantic decision classifier is not initialized")
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("semantic decision classifier url is empty")
	}

	req, err := c.buildRequest(ctx, prompt, text)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return decodeClassificationResponse(resp)
}

func (c *classifierClient) buildRequest(ctx context.Context, prompt string, text string) (*http.Request, error) {
	payload := classificationRequest{Prompt: strings.TrimSpace(prompt), Text: strings.TrimSpace(text)}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+classificationPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func decodeClassificationResponse(resp *http.Response) (*Classification, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("semantic decision classifier returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var decoded Classification
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}

	return normalizeClassification(&decoded), nil
}

func normalizeClassification(decoded *Classification) *Classification {
	if decoded.IntentLabel == "" {
		decoded.IntentLabel = "message"
	}
	return decoded
}
