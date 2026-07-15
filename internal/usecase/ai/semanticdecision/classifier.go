package semanticdecision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ActionCandidate 為 reranker 精排後、提供給最終決策模型參考的候選描述。
// 只保留文字判斷所需的最小資訊，避免把 topkfilter/ranking 內部型別直接洩漏到這一層。
type ActionCandidate struct {
	Operation string
	SkillCode string
	RouteText string
	Score     *float64
}

// ActionDecision 表示語意決策模型針對候選 action 選出的最終結果。
// 回傳的是 api_operation（對應 action 的實際執行操作）。
type ActionDecision struct {
	SchemaVersion string  `json:"schema_version"`
	APIOperation  string  `json:"api_operation"`
	Confidence    float64 `json:"confidence"`
	Reason        string  `json:"reason"`
}

// Classifier 定義通用語意決策能力。
type Classifier interface {
	// ClassifyAction 把 prompt+text 送進 actionDecisionPath，
	// 回應 payload 解析成 ActionDecision（api_operation）。
	ClassifyAction(ctx context.Context, prompt string, text string) (*ActionDecision, error)
}

type classifierClient struct {
	baseURL string
	client  *http.Client
}

type classificationRequest struct {
	Prompt string `json:"prompt"`
	Text   string `json:"text"`
}

// actionDecisionPath 是獨立於 command/message 分類的端點，
// 服務端會接受 api_operation 欄位。
const actionDecisionPath = "/predict/action_decision"

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

// BuildFinalActionPrompt 依 reranker 精排後的候選清單組出最終決策提示詞。
// 每筆候選都以文字描述呈現（operation/skill/route_text/score），
// 交由模型依語意判斷唯一一個最終 action，並要求輸出
// (schema_version, api_operation, confidence, reason)，其中 api_operation
// 即為模型選出的 action operation。這個 prompt 只會送到 actionDecisionPath，
// 不會混用 command/message 分類用的 intent_label schema。
func BuildFinalActionPrompt(candidates []ActionCandidate) string {
	var lines []string
	for idx, candidate := range candidates {
		operation := strings.TrimSpace(candidate.Operation)
		if operation == "" {
			continue
		}
		line := fmt.Sprintf("%d. operation=%s skill=%s route_text=%q",
			idx+1,
			operation,
			strings.TrimSpace(candidate.SkillCode),
			strings.TrimSpace(candidate.RouteText),
		)
		if candidate.Score != nil {
			line += fmt.Sprintf(" score=%.6f", *candidate.Score)
		}
		lines = append(lines, line)
	}

	return strings.TrimSpace(`
你是跨通訊軟體的最終動作決策器。
以下是系統依向量召回與 cross-encoder 精排後，篩選出的候選 action，
依 rerank 分數由高到低排序（第 1 筆為目前分數最高的候選）：

` + strings.Join(lines, "\n") + `

請只根據使用者訊息與上述候選，選出「唯一一個」最終應執行的 action。

輸出格式必須是 JSON，欄位固定如下：
schema_version, api_operation, confidence, reason

規則：
- api_operation 必須是上述候選其中一個 operation 的原始值，不可自行創造新值
- 若使用者訊息語意明顯對應某個候選，即使非分數最高的候選，也應選擇語意最貼合的那個
- confidence 為 0 到 1 的數字，表示你對這個選擇的把握程度
- reason 用一句話簡述為何選擇該 action，而非其他候選
`)
}

func (c *classifierClient) buildRequest(ctx context.Context, path string, prompt string, text string) (*http.Request, error) {
	payload := classificationRequest{Prompt: strings.TrimSpace(prompt), Text: strings.TrimSpace(text)}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// readErrorBodyPreview 在非 2xx 回應時讀出一小段 body，方便直接在 Go 端 log 看到
// 服務端具體拒絕原因（例如 "ollama output contains unknown keys"），
// 不需要另外去查 Python 服務的 log。
func readErrorBodyPreview(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ClassifyAction 把 prompt+text 送進 actionDecisionPath，回應解析成 ActionDecision（api_operation）。
func (c *classifierClient) ClassifyAction(ctx context.Context, prompt string, text string) (*ActionDecision, error) {
	if c == nil {
		return nil, fmt.Errorf("semantic decision classifier is not initialized")
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("semantic decision classifier url is empty")
	}

	req, err := c.buildRequest(ctx, actionDecisionPath, prompt, text)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return decodeActionDecisionResponse(resp)
}

func decodeActionDecisionResponse(resp *http.Response) (*ActionDecision, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("action decision classifier returned status %d: %s", resp.StatusCode, readErrorBodyPreview(resp))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var decoded ActionDecision
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}

	return &decoded, nil
}
