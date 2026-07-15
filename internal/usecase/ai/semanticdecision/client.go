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

// QuestionAnswer 表示語意服務把訊息當成問答問題時的回覆結果。
// answer 是回覆內容，confidence 代表該回覆可直接採用的把握程度。
type QuestionAnswer struct {
	SchemaVersion string  `json:"schema_version"`
	Answer        string  `json:"answer"`
	Confidence    float64 `json:"confidence"`
}

// Client 定義通用語意決策能力。
type Client interface {
	// ClassifyAction 把 prompt+text 送進 actionDecisionPath，
	// 回應 payload 解析成 ActionDecision（api_operation）。
	ClassifyAction(ctx context.Context, prompt string, text string) (*ActionDecision, error)
	// AnswerQuestion 把 prompt+text 送進 questionAnswerPath，
	// 回應 payload 解析成 QuestionAnswer（answer + confidence）。
	AnswerQuestion(ctx context.Context, prompt string, text string) (*QuestionAnswer, error)
}

type decisionClient struct {
	baseURL string
	client  *http.Client
}

type classificationRequest struct {
	Prompt string `json:"prompt"`
	Text   string `json:"text"`
}

type upstreamErrorPayload struct {
	Detail string `json:"detail"`
}

// UpstreamError 描述 9002 服務回傳的非 2xx 錯誤。
// 會保留 status 與 detail，讓呼叫端可做更精準的告警與降級處理。
type UpstreamError struct {
	Path       string
	StatusCode int
	Detail     string
	Body       string
}

func (e *UpstreamError) Error() string {
	if e == nil {
		return "semantic decision upstream error"
	}
	if strings.TrimSpace(e.Detail) != "" {
		return fmt.Sprintf("semantic decision upstream %s returned status %d: %s", e.Path, e.StatusCode, e.Detail)
	}
	if strings.TrimSpace(e.Body) != "" {
		return fmt.Sprintf("semantic decision upstream %s returned status %d: %s", e.Path, e.StatusCode, e.Body)
	}
	return fmt.Sprintf("semantic decision upstream %s returned status %d", e.Path, e.StatusCode)
}

// actionDecisionPath 是獨立於 command/message 分類的端點，
// 服務端會接受 api_operation 欄位。
const actionDecisionPath = "/predict/action_decision"

// questionAnswerPath 負責把訊息當作一般問題回答，
// 服務端會回覆 answer 與 confidence。
const questionAnswerPath = "/predict/question_answer"

// NewClient 建立通用語意決策 client。
func NewClient(baseURL string, timeoutSeconds int) Client {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return nil
	}
	return &decisionClient{
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

// BuildQuestionAnswerPrompt 產生問答模式提示詞。
// 目標是要求模型輸出可直接回覆使用者的答案與信心度，
// 讓上游可依 confidence 判斷是否改送 cloud LLM。
func BuildQuestionAnswerPrompt() string {
	return strings.TrimSpace(`
你是通訊助理的問答回覆器。
請直接回答使用者問題，並輸出 JSON，欄位固定如下：
schema_version, answer, confidence

規則：
- answer: 直接可讀的最終回答（繁體中文）
- confidence: 0 到 1 的數字，代表此回答是否足夠可靠
- 若問題涉及即時資訊、查詢網路、資料不足或高風險推論，請大幅降低 confidence
- 不可輸出額外欄位
`)
}

func (c *decisionClient) buildRequest(ctx context.Context, path string, prompt string, text string) (*http.Request, error) {
	// 這裡只傳兩個欄位：prompt + text。
	// 目的是讓 9002 只做「語意到 action 的最後選擇」，不承擔上游 rerank 結構細節。
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

func decodeUpstreamError(resp *http.Response, path string) error {
	// 9002 錯誤通常是 FastAPI 風格：{"detail":"..."}。
	// 這裡把 detail/body 都保留，讓上層能做條件分流：
	// - detail=no_match -> 問答分支
	// - 其他 detail -> 告警或重試
	body := readErrorBodyPreview(resp)
	detail := ""
	if strings.TrimSpace(body) != "" {
		var payload upstreamErrorPayload
		if err := json.Unmarshal([]byte(body), &payload); err == nil {
			detail = strings.TrimSpace(payload.Detail)
		}
	}

	return &UpstreamError{
		Path:       path,
		StatusCode: resp.StatusCode,
		Detail:     detail,
		Body:       body,
	}
}

// ClassifyAction 把 prompt+text 送進 actionDecisionPath，回應解析成 ActionDecision（api_operation）。
func (c *decisionClient) ClassifyAction(ctx context.Context, prompt string, text string) (*ActionDecision, error) {
	if c == nil {
		return nil, fmt.Errorf("semantic decision client is not initialized")
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("semantic decision client url is empty")
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
		// 非 2xx 改成結構化錯誤，保留 status/detail 供上層精準處理。
		return nil, decodeUpstreamError(resp, actionDecisionPath)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var decoded ActionDecision
	if err := json.Unmarshal(data, &decoded); err != nil {
		// 這裡刻意不做容錯 fallback，若回傳 JSON 不符合契約就直接失敗，
		// 避免把不完整/錯誤資料默默帶到後續 action 執行路徑。
		return nil, err
	}

	return &decoded, nil
}

// AnswerQuestion 把 prompt+text 送進 questionAnswerPath，回應解析成 QuestionAnswer。
func (c *decisionClient) AnswerQuestion(ctx context.Context, prompt string, text string) (*QuestionAnswer, error) {
	if c == nil {
		return nil, fmt.Errorf("semantic decision client is not initialized")
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("semantic decision client url is empty")
	}

	req, err := c.buildRequest(ctx, questionAnswerPath, prompt, text)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return decodeQuestionAnswerResponse(resp)
}

func decodeQuestionAnswerResponse(resp *http.Response) (*QuestionAnswer, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 問答端點也統一回傳結構化上游錯誤，便於上層判斷是否改走 cloud LLM。
		return nil, decodeUpstreamError(resp, questionAnswerPath)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var decoded QuestionAnswer
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}

	return &decoded, nil
}
