package messageintent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Classification 表示單次訊息意圖分類結果。
type Classification struct {
	SchemaVersion string  `json:"schema_version"`
	IntentLabel   string  `json:"intent_label"`
	Confidence    float64 `json:"confidence"`
	Reason        string  `json:"reason"`
}

// Classifier 定義通用訊息分類能力。
type Classifier interface {
	Classify(ctx context.Context, prompt string, text string) (*Classification, error)
}

type classifierClient struct {
	baseURL string
	client  *http.Client
}

type request struct {
	Prompt string `json:"prompt"`
	Text   string `json:"text"`
}

// NewClassifier 建立通用訊息分類 client。
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
	// 先檢查 client 是否已初始化，避免呼叫到空物件。
	if c == nil {
		return nil, fmt.Errorf("message intent classifier is not initialized")
	}
	// baseURL 代表分類服務的位置，若未設定就無法發送請求。
	if c.baseURL == "" {
		return nil, fmt.Errorf("message intent classifier url is empty")
	}
	// 將 prompt 與文字整理成送往分類服務的 request payload。
	payload := request{
		Prompt: strings.TrimSpace(prompt),
		Text:   strings.TrimSpace(text),
	}
	// 轉成 JSON，準備送出 HTTP request。
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	// 建立 POST request，目標是 message intent 的預測端點。
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/predict/message_intent", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// 告訴對方這是一個 JSON 格式的 API 呼叫。
	req.Header.Set("Content-Type", "application/json")

	// 實際送出請求到分類服務。
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 只接受 2xx 回應；其他都視為分類服務失敗。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("message intent classifier returned status %d", resp.StatusCode)
	}

	// 解析分類服務回傳的 JSON 結果。
	var decoded Classification
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	// 如果模型沒有回 intent_label，預設視為一般訊息。
	if decoded.IntentLabel == "" {
		decoded.IntentLabel = "message"
	}
	return &decoded, nil
}
