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

// ActionCandidate 為 reranker 精排後、提供給最終決策模型參考的候選描述。
// 只保留文字判斷所需的最小資訊，避免把 topkfilter/ranking 內部型別直接洩漏到這一層。
type ActionCandidate struct {
	Operation string
	SkillCode string
	RouteText string
	Score     *float64
}

// ActionDecision 表示語意決策模型针對候選 action 選出的最終結果。
// 與 Classification 不同：這裡選出的是 api_operation（對應 action 的實際執行操作），
// 不是 command/message 的 intent_label，所以用独立型別避免權用權意。
type ActionDecision struct {
	SchemaVersion string  `json:"schema_version"`
	APIOperation  string  `json:"api_operation"`
	Confidence    float64 `json:"confidence"`
	Reason        string  `json:"reason"`
}

// Classifier 定義通用語意決策能力。
type Classifier interface {
	Classify(ctx context.Context, prompt string, text string) (*Classification, error)
	// ClassifyAction 與 Classify 使用同一套 prompt+text 請求模式，
	// 但回應 payload 解析成 ActionDecision（api_operation 而非 intent_label）。
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

// BuildFinalActionPrompt 依 reranker 精排後的候選清單組出最終決策提示詞。
// 每筆候選都以文字描述呈現（operation/skill/route_text/score），
// 交由模型依語意判斷唯一一個最終 action，並沿用既有 schema
// (schema_version, intent_label, confidence, reason)，其中 intent_label
// 即為模型選出的 action operation。
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

// ClassifyAction 與 Classify 使用同樣的 prompt+text 請求流程，
// 但把回應解析成 ActionDecision（api_operation 而非 intent_label）。
func (c *classifierClient) ClassifyAction(ctx context.Context, prompt string, text string) (*ActionDecision, error) {
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

	return decodeActionDecisionResponse(resp)
}

func decodeActionDecisionResponse(resp *http.Response) (*ActionDecision, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("semantic decision classifier returned status %d", resp.StatusCode)
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
