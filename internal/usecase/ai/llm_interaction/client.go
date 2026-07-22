package llminteraction

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

// ActionCandidate 為 reranker 精排後、提供給 LLM 互動層參考的候選描述。
// 只保留文字判斷所需的最小資訊，避免把 topkfilter/ranking 內部型別直接洩漏到這一層。
type ActionCandidate struct {
	Operation string
	SkillCode string
	RouteText string
	Prompt    string
	Score     *float64
}

// ActionDecision 表示 LLM 互動模型針對候選 action 選出的最終結果。
// 回傳的是 api_operation（對應 action 的實際執行操作）。
type ActionDecision struct {
	// SchemaVersion 用來標示目前 action 決策回應的契約版本，
	// 方便未來做灰度升級或多版本相容判斷。
	SchemaVersion string `json:"schema_version"`
	// NextStep 是結構化流程控制欄位，用來明確告知上游下一步應執行 action、追問或一般問答。
	NextStep string `json:"next_step"`
	// APIOperation 是模型最終挑選的操作名稱，
	// 呼叫端會以此值對應到實際執行 handler。
	APIOperation string `json:"api_operation"`
	// ActionParams 是動態參數容器：
	// key 為參數名稱，value 保持 json.RawMessage，讓不同 action 可各自解析型別。
	ActionParams map[string]json.RawMessage `json:"action_params,omitempty"`
	// MissingParameters 保存本次決策判定「仍缺失」的必要參數名稱清單，
	// 例如 target_locales、amount、billing_period 等。
	MissingParameters []string `json:"missing_parameters,omitempty"`
	// Confidence 表示模型對本次 action 決策的信心值（0~1）。
	Confidence float64 `json:"confidence"`
	// Reason 保留模型端的簡短決策理由，用於 observability 與排查。
	Reason string `json:"reason"`
}

const (
	NextStepExecuteAction         = "execute_action"
	NextStepAskClarifyingQuestion = "ask_clarifying_question"
	NextStepAnswerQuestion        = "answer_question"
)

// ParamString 讀取 action_params 裡的字串參數。
func (d *ActionDecision) ParamString(key string) (string, bool) {
	// 防禦式判斷：若決策或參數集合不存在，直接回傳 not found。
	if d == nil || len(d.ActionParams) == 0 {
		return "", false
	}
	// key 先做 trim，避免上游/呼叫端夾帶空白造成查詢 miss。
	raw, ok := d.ActionParams[strings.TrimSpace(key)]
	if !ok || len(raw) == 0 {
		return "", false
	}

	var value string
	// 僅接受 JSON string 型別；若是 number/object/array 視為型別不符。
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	// 空白字串不算有效參數，統一回 not found，避免下游誤觸發。
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}

// ParamStringSlice 讀取 action_params 裡的字串陣列參數，
// 同時容忍上游把單值以 string 回傳。
func (d *ActionDecision) ParamStringSlice(key string) []string {
	// 與 ParamString 相同，先做空值保護，確保呼叫端可安全重複使用。
	if d == nil || len(d.ActionParams) == 0 {
		return nil
	}
	raw, ok := d.ActionParams[strings.TrimSpace(key)]
	if !ok || len(raw) == 0 {
		return nil
	}

	var values []string
	var multi []string
	// 第一優先：嘗試解析為陣列，對應標準多值參數格式。
	if err := json.Unmarshal(raw, &multi); err == nil {
		for _, value := range multi {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				values = append(values, trimmed)
			}
		}
		// 輸出前做不分大小寫去重，避免同語系重覆寫入（如 en-US / en-us）。
		return dedupeFold(values)
	}

	var single string
	// 相容格式：若上游把單值塞到同一個 key，仍能正常轉為單元素陣列。
	if err := json.Unmarshal(raw, &single); err == nil {
		if trimmed := strings.TrimSpace(single); trimmed != "" {
			values = append(values, trimmed)
		}
	}

	return dedupeFold(values)
}

// dedupeFold 以大小寫不敏感方式去重，並保留第一個出現的原始大小寫。
// 這可以兼顧資料一致性（避免重覆）與可讀性（保留輸入格式）。
func dedupeFold(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// QuestionAnswer 表示語意服務把訊息當成問答問題時的回覆結果。
// answer 是回覆內容，confidence 代表該回覆可直接採用的把握程度。
type QuestionAnswer struct {
	SchemaVersion string  `json:"schema_version"`
	Answer        string  `json:"answer"`
	Confidence    float64 `json:"confidence"`
}

// ContextAnalysis 表示 LLM interaction 對短文本與近端上下文的內部結構化分析結果。
// 它只描述「目前訊息是否和某個內部服務相關」，不負責產生使用者可見回答。
// target_service / extracted_fields 皆保持中性，讓 todo、calendar、follow-up 等 realtime service 可共用同一契約。
type ContextAnalysis struct {
	SchemaVersion   string                     `json:"schema_version"`
	Decision        string                     `json:"decision"`
	TargetService   string                     `json:"target_service"`
	Confidence      float64                    `json:"confidence"`
	ExtractedFields map[string]json.RawMessage `json:"extracted_fields,omitempty"`
	MissingFields   []string                   `json:"missing_fields,omitempty"`
	Reason          string                     `json:"reason"`
}

// TodoAnalysis 表示 Todo Reminder 專用 structured analyzer 的輸出。
// 目前只用於 debug/log 與後續 todo_candidate 狀態機銜接，不直接建立待辦。
type TodoAnalysis struct {
	SchemaVersion   string   `json:"schema_version"`
	Decision        string   `json:"decision"`
	LinkedMessageID string   `json:"linked_message_id"`
	Summary         string   `json:"summary"`
	Assignees       []string `json:"assignees,omitempty"`
	DueText         string   `json:"due_text"`
	Confidence      float64  `json:"confidence"`
	MissingFields   []string `json:"missing_fields,omitempty"`
	Reason          string   `json:"reason"`
}

// TodoDueTimeAnalysis 表示 Todo Reminder 專用時間正規化輸出。
// 它只處理 due_text -> due_at，不承擔 todo candidate 是否成立的判斷。
type TodoDueTimeAnalysis struct {
	SchemaVersion string   `json:"schema_version"`
	Decision      string   `json:"decision"`
	DueAt         string   `json:"due_at"`
	Timezone      string   `json:"timezone"`
	Precision     string   `json:"precision"`
	Confidence    float64  `json:"confidence"`
	MissingFields []string `json:"missing_fields,omitempty"`
	Reason        string   `json:"reason"`
}

// InteractionClient 定義通用 LLM 互動能力。
type InteractionClient interface {
	// ClassifyAction 把 prompt+text 送進 actionDecisionPath，
	// 回應 payload 解析成 ActionDecision（api_operation）。
	ClassifyAction(ctx context.Context, prompt string, text string) (*ActionDecision, error)
	// AnswerQuestion 把 prompt+text 送進 questionAnswerPath，
	// 回應 payload 解析成 QuestionAnswer（answer + confidence）。
	AnswerQuestion(ctx context.Context, prompt string, text string) (*QuestionAnswer, error)
	// AnalyzeContext 把 prompt+text 送進 contextAnalyzePath，
	// 回應 payload 解析成 ContextAnalysis（系統內部上下文分析結果）。
	AnalyzeContext(ctx context.Context, prompt string, text string) (*ContextAnalysis, error)
	// AnalyzeTodo 把 prompt+text 送進 todoAnalyzePath，回應 payload 解析成 TodoAnalysis（Todo 專用結構化分析）。
	AnalyzeTodo(ctx context.Context, prompt string, text string) (*TodoAnalysis, error)
	// AnalyzeTodoDueTime 把 prompt+text 送進 todoDueTimePath，回應 payload 解析成 TodoDueTimeAnalysis。
	AnalyzeTodoDueTime(ctx context.Context, prompt string, text string) (*TodoDueTimeAnalysis, error)
}

type interactionClient struct {
	baseURL string
	// modelName 只對本地 LLM interaction 有意義。
	// 空值代表沿用服務啟動時的預設模型；非空值會隨 request 傳入，讓同一個服務可依 profile 切換模型。
	modelName          string
	actionDecisionPath string
	questionAnswerPath string
	// contextAnalyzePath 指向 dedicated context_analyze route，避免 AnalyzeContext 誤用一般問答端點。
	contextAnalyzePath string
	// todoAnalyzePath 指向 dedicated todo_analyze route，讓 Todo schema 與通用上下文分析分離。
	todoAnalyzePath string
	// todoDueTimePath 指向 dedicated todo_due_time_normalize route，讓時間正規化和 todo 抽取分離。
	todoDueTimePath string
	client          *http.Client
}

type classificationRequest struct {
	Prompt string `json:"prompt"`
	// ModelName 是本地 LLM interaction 的 per-request model selector。
	// 不把它放進 prompt，避免模型選擇變成自然語言規則；它必須是 transport contract 的結構化欄位。
	ModelName             string `json:"model_name,omitempty"`
	JSONDecodeRetryPrompt string `json:"json_decode_retry_prompt"`
	ValidationRetryPrompt string `json:"validation_retry_prompt"`
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
		return "llm interaction upstream error"
	}
	if strings.TrimSpace(e.Detail) != "" {
		return fmt.Sprintf("llm interaction upstream %s returned status %d: %s", e.Path, e.StatusCode, e.Detail)
	}
	if strings.TrimSpace(e.Body) != "" {
		return fmt.Sprintf("llm interaction upstream %s returned status %d: %s", e.Path, e.StatusCode, e.Body)
	}
	return fmt.Sprintf("llm interaction upstream %s returned status %d", e.Path, e.StatusCode)
}

// defaultActionDecisionPath 是獨立於 command/message 分類的端點，
// 服務端會接受 api_operation 欄位。
const defaultActionDecisionPath = "/predict/action_decision"

// defaultQuestionAnswerPath 負責把訊息當作一般問題回答，
// 服務端會回覆 answer 與 confidence。
const defaultQuestionAnswerPath = "/predict/question_answer"

// defaultContextAnalyzePath 負責內部短文本上下文分析，與一般使用者問答分離。
// 即使外部設定漏填 path，AnalyzeContext 仍會走 dedicated route，而不是回退到 question_answer。
const defaultContextAnalyzePath = "/predict/context_analyze"

// defaultTodoAnalyzePath 負責 Todo Reminder 專用結構化分析，與通用 context_analyze 分離。
const defaultTodoAnalyzePath = "/predict/todo_analyze"

// defaultTodoDueTimePath 負責 Todo Reminder 專用時間正規化，與 todo_analyze 主契約分離。
const defaultTodoDueTimePath = "/predict/todo_due_time_normalize"

// NewInteractionClient 建立通用 LLM 互動 client。
func NewInteractionClient(baseURL string, timeoutSeconds int) InteractionClient {
	return NewInteractionClientWithPaths(baseURL, timeoutSeconds, defaultActionDecisionPath, defaultQuestionAnswerPath)
}

// NewInteractionClientWithPaths 建立可指定本地 endpoint path 的通用 LLM 互動 client。
func NewInteractionClientWithPaths(baseURL string, timeoutSeconds int, actionDecisionPath string, questionAnswerPath string) InteractionClient {
	return NewInteractionClientWithModel(baseURL, timeoutSeconds, "", actionDecisionPath, questionAnswerPath, defaultContextAnalyzePath, defaultTodoAnalyzePath, defaultTodoDueTimePath)
}

// NewInteractionClientWithModel 建立可指定本地 endpoint path 與 Ollama model 的通用 LLM 互動 client。
// 這個建構子用在 local provider profile：profile.model_name 會被轉成 9003 request 的 model_name，
// 因此不需要為不同模型另外開不同服務，只需新增 provider profile。
func NewInteractionClientWithModel(baseURL string, timeoutSeconds int, modelName string, actionDecisionPath string, questionAnswerPath string, contextAnalyzePath string, todoAnalyzePath string, todoDueTimePath string) InteractionClient {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return nil
	}
	actionPath := strings.TrimSpace(actionDecisionPath)
	if actionPath == "" {
		actionPath = defaultActionDecisionPath
	}
	if !strings.HasPrefix(actionPath, "/") {
		actionPath = "/" + actionPath
	}
	questionPath := strings.TrimSpace(questionAnswerPath)
	if questionPath == "" {
		questionPath = defaultQuestionAnswerPath
	}
	if !strings.HasPrefix(questionPath, "/") {
		questionPath = "/" + questionPath
	}
	// context path 的處理規則和 action/question 一致：允許設定檔省略前導斜線，
	// 但空值只能補 context_analyze 預設值，不能借用 question_answer 做替代。
	contextPath := strings.TrimSpace(contextAnalyzePath)
	if contextPath == "" {
		contextPath = defaultContextAnalyzePath
	}
	if !strings.HasPrefix(contextPath, "/") {
		contextPath = "/" + contextPath
	}
	todoPath := strings.TrimSpace(todoAnalyzePath)
	if todoPath == "" {
		todoPath = defaultTodoAnalyzePath
	}
	if !strings.HasPrefix(todoPath, "/") {
		todoPath = "/" + todoPath
	}
	todoDuePath := strings.TrimSpace(todoDueTimePath)
	if todoDuePath == "" {
		todoDuePath = defaultTodoDueTimePath
	}
	if !strings.HasPrefix(todoDuePath, "/") {
		todoDuePath = "/" + todoDuePath
	}
	return &interactionClient{
		baseURL:            strings.TrimRight(trimmed, "/"),
		modelName:          strings.TrimSpace(modelName),
		actionDecisionPath: actionPath,
		questionAnswerPath: questionPath,
		contextAnalyzePath: contextPath,
		todoAnalyzePath:    todoPath,
		todoDueTimePath:    todoDuePath,
		client:             &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
	}
}

// BuildFinalActionPrompt 依 reranker 精排後的候選清單組出最終決策提示詞。
// 每筆候選都以文字描述呈現（operation/skill/route_text/score），
// 交由模型依語意判斷唯一一個最終 action，並要求輸出
// (schema_version, next_step, api_operation, action_params, missing_parameters, confidence, reason)，其中 api_operation
// 即為模型選出的 action operation。這個 prompt 只會送到 actionDecisionPath，
// 不會混用 command/message 分類用的 intent_label schema。
func BuildFinalActionPrompt(candidates []ActionCandidate) string {
	var lines []string
	guidanceByOperation := make(map[string]string, len(candidates))
	guidanceOrder := make([]string, 0, len(candidates))
	for idx, candidate := range candidates {
		operation := strings.TrimSpace(candidate.Operation)
		if operation == "" {
			continue
		}
		line := fmt.Sprintf("%d. operation=%s skill=%s route_text=%s",
			idx+1,
			operation,
			strings.TrimSpace(candidate.SkillCode),
			strings.TrimSpace(candidate.RouteText),
		)
		if candidate.Score != nil {
			line += fmt.Sprintf(" score=%.6f", *candidate.Score)
		}
		lines = append(lines, line)

		if guidance := strings.TrimSpace(candidate.Prompt); guidance != "" {
			if _, exists := guidanceByOperation[operation]; !exists {
				guidanceByOperation[operation] = guidance
				guidanceOrder = append(guidanceOrder, operation)
			}
		}
	}

	guidanceSection := ""
	if len(guidanceOrder) > 0 {
		guidanceLines := make([]string, 0, len(guidanceOrder))
		for _, operation := range guidanceOrder {
			guidanceLines = append(guidanceLines,
				fmt.Sprintf("- operation=%s prompt=%s", operation, strings.ReplaceAll(guidanceByOperation[operation], "\n", " ")),
			)
		}
		guidanceSection = "\n\noperation 專屬動態規則（由 action.prompt 注入）：\n" + strings.Join(guidanceLines, "\n")
	}

	return strings.TrimSpace(`
以下是系統依向量召回與 cross-encoder 精排後，篩選出的候選 action，
依 rerank 分數由高到低排序（第 1 筆為目前分數最高的候選）：

` + strings.Join(lines, "\n") + `
` + guidanceSection + `

請只根據使用者訊息與上述候選，選出「唯一一個」最終應執行的 action。

輸出格式必須是 JSON，並遵守以下 contract：
` + actionDecisionContractPromptBlock() + `

規則：
- 先完成兩階段決策：
	1) 先從候選清單選出唯一 api_operation
	2) 再只套用該 api_operation 對應的 operation 專屬動態規則來填 action_params
- 候選排序與 score 只代表召回/精排參考，不是最終答案；選擇 api_operation 時必須以使用者訊息的真實意圖與 operation 用途為準
- 選擇 api_operation 前，先判斷該 operation 的必要語意條件是否已由使用者訊息明確提供；若某候選需要指定目標、範圍或參數，但使用者訊息只表達整體啟用/停用意圖，應優先選擇不需要該參數且語意涵蓋整體操作的候選
- route_text 是召回提示語，不是使用者已提供參數的證據；包含「某、指定、特定、目標」等佔位描述的 route_text 不代表使用者訊息已指定該參數
- 不可套用未被選中 operation 的動態規則
- next_step 只能是 execute_action、ask_clarifying_question、answer_question 三者之一
- api_operation 只能是候選 operation 之一；不可創造新值
- 若 next_step=execute_action：api_operation 必須非空，action_params 只可填必要且可明確擷取的參數
- 若 next_step=ask_clarifying_question：保留最可能的 api_operation，並把缺失參數放入 missing_parameters
- 若 next_step=answer_question：api_operation 必須為空字串，action_params 必須為空物件
- missing_parameters 非空時，next_step 必須是 ask_clarifying_question
- 不可把 route_text、skill、score、operation 這類候選描述欄位複製到 action_params
- action_params 是唯一可承載執行參數的機器可讀欄位；所有執行參數都必須依被選中 operation 的動態規則放在 action_params，且型別必須完全符合該規則
- reason 只能說明決策理由，不可承載或替代 action_params，不可包含 JSON 片段、key=value、參數清單或可被下游解析為執行參數的內容
- 缺少必要參數時不可猜測
- 若使用者訊息語意明顯對應某個候選，即使非分數最高也應選語意最貼合者
- confidence 為 0 到 1，表示對「下一步判斷」的把握程度
- reason 只允許一句純文字，不可包含雙引號、JSON 片段或候選清單原文
`)
}

// BuildQuestionAnswerPrompt 產生問答模式提示詞。
// 目標是要求模型輸出可直接回覆使用者的答案與信心度，
// 讓上游可依 confidence 判斷是否改送 cloud LLM。
func BuildQuestionAnswerPrompt() string {
	return strings.TrimSpace(`
你是通訊助理的問答回覆器。
請直接回答使用者問題，並輸出 JSON，遵守以下 contract：
` + questionAnswerContractPromptBlock() + `

規則：
- answer: 直接可讀的最終回答（繁體中文）
- confidence: 0 到 1 的數字，代表此回答是否足夠可靠
- 若問題涉及即時資訊、查詢網路、資料不足或高風險推論，請大幅降低 confidence
- 不可輸出額外欄位
`)
}

// BuildClarifyingQuestionPrompt 產生追問模式提示詞。
// 當 action 決策信心不足時，要求模型根據原訊息與決策理由，
// 提出一個最小必要的澄清問題，而不是直接回答或執行動作。
func BuildClarifyingQuestionPrompt(reason string) string {
	trimmedReason := strings.TrimSpace(reason)
	if trimmedReason == "" {
		trimmedReason = "目前資訊不足，無法安全決定唯一 action。"
	}

	return strings.TrimSpace(`
你是通訊助理的追問問題產生器。
目前系統無法安全執行 action，需要先向使用者追問一個最小必要問題。

目前無法直接執行的原因：
` + trimmedReason + `

請根據使用者原始訊息與上述原因，提出一個最能消除歧義的追問問題。

輸出格式必須是 JSON，並遵守以下 contract：
` + questionAnswerContractPromptBlock() + `

規則：
- answer 必須是一句直接對使用者提問的繁體中文追問句
- 只能問一個最小必要問題，不可同時追問多件事
- 不可直接回答問題、不可執行動作、不可假設缺失參數
- 若原因已指出缺少的必要資訊，應優先針對該資訊追問
- confidence 為 0 到 1 的數字，代表這個追問是否足以澄清下一步
- 不可輸出額外欄位
`)
}

func (c *interactionClient) buildRequest(ctx context.Context, path string, prompt string, text string) (*http.Request, error) {
	// 9003 的 llm_interaction 服務只負責「執行模型 + 驗證輸出」，
	// 不再自己拼接使用者訊息或重試提示詞。這裡由 API 端一次組好完整 prompt，
	// 讓 prompt 契約集中在 Go 端，避免 Python 服務與 API 端各維護一份規則。
	composedPrompt := buildLocalInteractionPrompt(prompt, text)
	payload := classificationRequest{
		Prompt: composedPrompt,
		// modelName 為空時會被 omitempty 省略，讓既有只靠 OLLAMA_MODEL 的本地部署仍可運作。
		ModelName:             strings.TrimSpace(c.modelName),
		JSONDecodeRetryPrompt: buildJSONDecodeRetryPromptForPath(path, composedPrompt),
		ValidationRetryPrompt: buildValidationRetryPromptForPath(path, composedPrompt),
	}
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

func buildLocalInteractionPrompt(prompt string, text string) string {
	// prompt 內含 action 候選、輸出 contract 與規則；text 是當次使用者訊息。
	// 兩者在 API 端合併後送出，Python 端不再需要理解哪一段是系統規則、哪一段是訊息本文。
	return strings.TrimSpace(strings.TrimSpace(prompt) + "\n\nMessage content:\n" + strings.TrimSpace(text))
}

func buildJSONDecodeRetryPrompt(composedPrompt string) string {
	// JSON decode 失敗代表模型沒有輸出合法 JSON。重試時保留原完整 prompt，
	// 只追加格式修正要求，避免第二次請求失去原本候選與使用者訊息脈絡。
	return strings.TrimSpace(composedPrompt + `

Your previous output was not valid JSON. Return exactly one-line JSON only.
Do not include markdown or extra keys.
Follow the exact output schema and constraints already defined in the original instruction above.`)
}

func buildJSONDecodeRetryPromptForPath(path string, composedPrompt string) string {
	// todo_analyze 的 JSON 失敗常見於小模型把 contract 欄位片段拼成 key；
	// 重試時直接重述完整 todo_analysis contract，讓第二輪修正聚焦在固定欄位，而不是只靠通用 JSON 指令。
	if strings.TrimSpace(path) == defaultTodoAnalyzePath {
		return buildTodoAnalysisJSONDecodeRetryPrompt(composedPrompt)
	}
	if strings.TrimSpace(path) == defaultTodoDueTimePath {
		return buildTodoDueTimeJSONDecodeRetryPrompt(composedPrompt)
	}
	return buildJSONDecodeRetryPrompt(composedPrompt)
}

func buildTodoAnalysisJSONDecodeRetryPrompt(composedPrompt string) string {
	return strings.TrimSpace(composedPrompt + `

Your previous output was not valid JSON for todo_analysis. Return exactly one complete JSON object and nothing else.
Output exactly these fields and no others:
` + todoAnalysisContractPromptBlock() + `

Todo JSON formatting rules:
- Use double quotes around every key and string value.
- Use commas between fields.
- Do not put JSON fragments inside key names.
- assignees and missing_fields must be JSON arrays, never strings or objects.
- reason is required for every decision and must be a non-empty string.
- Use "" for empty string fields except reason; use [] for empty array fields.
- Do not include markdown, code fences, explanations, or extra keys.`)
}

func buildTodoDueTimeJSONDecodeRetryPrompt(composedPrompt string) string {
	return strings.TrimSpace(composedPrompt + `

Your previous output was not valid JSON for todo_due_time. Return exactly one complete JSON object and nothing else.
Output exactly these fields and no others:
schema_version, decision, due_at, timezone, precision, confidence, missing_fields, reason

Todo due-time JSON formatting rules:
- Use double quotes around every key and string value.
- Use commas between fields.
- decision must be one of normalized, needs_more_info, no_due_time.
- precision must be one of datetime, date, relative_window, unknown.
- missing_fields must be a JSON string array; use [] when empty.
- reason is required and must be a non-empty string.
- Do not include markdown, code fences, explanations, or extra keys.`)
}

func buildValidationRetryPrompt(composedPrompt string) string {
	// venv 會把 {validation_error} 替換成實際驗證錯誤。
	// placeholder 由 API 端固定提供，確保每個本地 LLM endpoint 的 retry 行為一致。
	return strings.TrimSpace(composedPrompt + `

Your previous JSON output did not satisfy validation. Return strict JSON only.
Follow the exact output schema and constraints already defined in the original instruction above.
Validation failure: {validation_error}`)
}

func buildValidationRetryPromptForPath(path string, composedPrompt string) string {
	// todo_analyze 的輸出欄位比一般 action/question 更嚴格；
	// 用 path 分流 retry prompt，讓第二次修正仍明確回到 todo_analysis contract，而不是只靠通用「照原 schema」描述。
	if strings.TrimSpace(path) == defaultTodoAnalyzePath {
		return buildTodoAnalysisValidationRetryPrompt(composedPrompt)
	}
	if strings.TrimSpace(path) == defaultTodoDueTimePath {
		return buildTodoDueTimeValidationRetryPrompt(composedPrompt)
	}
	return buildValidationRetryPrompt(composedPrompt)
}

func buildTodoAnalysisValidationRetryPrompt(composedPrompt string) string {
	// 這裡刻意把完整 contract block 再放進 validation retry：
	// 小模型第一次漏欄位時，第二次若只看到 validation_error，容易只補單一欄位而忘記其他 required key。
	return strings.TrimSpace(composedPrompt + `

Your previous JSON output did not satisfy todo_analysis validation. Return strict JSON only.
Output exactly these fields and no others:
` + todoAnalysisContractPromptBlock() + `

Todo-specific validation rules:
- decision must be one of create_candidate, update_candidate, acknowledge, cancel_candidate, needs_more_info, no_action.
- confidence is required and must be a JSON number between 0 and 1.
- assignees and missing_fields are always JSON string arrays; use [] when empty.
- linked_message_id, summary, and due_text are always JSON strings; use "" when empty.
- reason is required for every decision and must be a non-empty JSON string.
- linked_message_id must only be one of the provided Explicit reply or Context messages IDs; never use Todo candidate row IDs.
- Todo candidate context message IDs are linkage hints, not row IDs: if a source_message_id, last_message_id, or previous_linked_message_id also appears in the provided Explicit reply or Context messages, it may be used as linked_message_id.
- create_candidate requires linked_message_id="", non-empty summary, missing_fields=[], and non-empty reason.
- update_candidate, acknowledge, and cancel_candidate require linked_message_id from the provided message ID list, non-empty summary, missing_fields=[], and non-empty reason.
- if the current message says the original time, availability, or condition no longer works and proposes an alternative time, condition, or execution arrangement for the same trackable todo, use update_candidate with linked_message_id pointing to the original task or previous update proposal; do not change it to no_action merely because the text looks like personal status or a question.
- if the current message's pragmatic function is to propose an alternative time, alternative condition, or feasibility confirmation for an existing todo and it does not introduce a new independent task objective, do not use create_candidate; use update_candidate linked to the modified existing task message.
- for update_candidate, acknowledge, and cancel_candidate, summary, assignees, and due_text may inherit known values from the linked message and Todo candidate contexts when the current message does not change them; do not list inherited known fields in missing_fields, because missing_fields must be [] for linked action decisions.
- needs_more_info requires non-empty summary, missing_fields to name the missing fields, and reason to be non-empty.
- use needs_more_info only when the current message is already a trackable todo or a valid continuation of an existing todo, but required fields are missing; if the message is not a trackable todo, use no_action instead.
- no_action requires linked_message_id="", summary="", assignees=[], due_text="", missing_fields=[], and non-empty reason.
- missing_fields may be non-empty only when decision=needs_more_info; for create_candidate, update_candidate, acknowledge, cancel_candidate, and no_action, missing_fields must be [].
- for no_action, explain why it is not a todo in reason and keep missing_fields=[].
- if no provided message ID is semantically valid for acknowledge/update/cancel, change decision to no_action or needs_more_info instead of inventing or copying another ID.

When correcting decision=no_action, use this exact field shape and only change confidence/reason text if needed:
{"schema_version":"v1","decision":"no_action","linked_message_id":"","summary":"","assignees":[],"due_text":"","confidence":0.0,"missing_fields":[],"reason":"message is not a trackable todo"}

Return one complete JSON object on one line. Do not truncate the JSON. Do not omit reason.
Validation failure: {validation_error}`)
}

func buildTodoDueTimeValidationRetryPrompt(composedPrompt string) string {
	return strings.TrimSpace(composedPrompt + `

Your previous JSON output did not satisfy todo_due_time validation. Return strict JSON only.
Output exactly these fields and no others:
schema_version, decision, due_at, timezone, precision, confidence, missing_fields, reason

Todo due-time validation rules:
- decision must be one of normalized, needs_more_info, no_due_time.
- normalized requires due_at, timezone, and precision; due_at must be RFC3339.
- timezone must use the input timezone.
- precision must be one of datetime, date, relative_window, unknown.
- confidence is required and must be a JSON number between 0 and 1.
- missing_fields is always a JSON string array; use [] when empty.
- reason is required and must be a non-empty string explaining how the due time was resolved or why it could not be resolved.
- needs_more_info requires missing_fields to name the missing fields.
- no_due_time and needs_more_info must use an empty due_at string.
Validation failure: {validation_error}`)
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
func (c *interactionClient) ClassifyAction(ctx context.Context, prompt string, text string) (*ActionDecision, error) {
	if c == nil {
		return nil, fmt.Errorf("semantic decision client is not initialized")
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("semantic decision client url is empty")
	}

	req, err := c.buildRequest(ctx, c.actionDecisionPath, prompt, text)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return decodeActionDecisionResponse(resp, c.actionDecisionPath)
}

func decodeActionDecisionResponse(resp *http.Response, path string) (*ActionDecision, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 非 2xx 改成結構化錯誤，保留 status/detail 供上層精準處理。
		return nil, decodeUpstreamError(resp, path)
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
	if err := validateActionDecision(&decoded); err != nil {
		// 把純文字驗證錯誤升級成結構化錯誤，
		// 讓 webhook 可區分這是「可追問修復」的契約問題，而不是一般執行失敗。
		// 同時附上推斷出的 missing parameters，供 action_results 寫入與模板追問使用。
		inferredMissing := InferMissingParametersFromReason(err.Error())
		return nil, &DecisionValidationError{
			Reason:            err.Error(),
			APIOperation:      strings.TrimSpace(decoded.APIOperation),
			MissingParameters: append([]string(nil), inferredMissing...),
		}
	}

	return &decoded, nil
}

// AnswerQuestion 把 prompt+text 送進 questionAnswerPath，回應解析成 QuestionAnswer。
func (c *interactionClient) AnswerQuestion(ctx context.Context, prompt string, text string) (*QuestionAnswer, error) {
	if c == nil {
		return nil, fmt.Errorf("semantic decision client is not initialized")
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("semantic decision client url is empty")
	}

	req, err := c.buildRequest(ctx, c.questionAnswerPath, prompt, text)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return decodeQuestionAnswerResponse(resp, c.questionAnswerPath)
}

func decodeQuestionAnswerResponse(resp *http.Response, path string) (*QuestionAnswer, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 問答端點也統一回傳結構化上游錯誤，便於上層判斷是否改走 cloud LLM。
		return nil, decodeUpstreamError(resp, path)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var decoded QuestionAnswer
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	if err := validateQuestionAnswer(&decoded); err != nil {
		return nil, err
	}

	return &decoded, nil
}

// AnalyzeContext 把 prompt+text 送進 contextAnalyzePath，回應解析成 ContextAnalysis。
// 這條路徑只用於系統內部上下文判斷；使用者的一般生活問答仍應呼叫 AnswerQuestion。
func (c *interactionClient) AnalyzeContext(ctx context.Context, prompt string, text string) (*ContextAnalysis, error) {
	if c == nil {
		return nil, fmt.Errorf("context analysis client is not initialized")
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("context analysis client url is empty")
	}

	req, err := c.buildRequest(ctx, c.contextAnalyzePath, prompt, text)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return decodeContextAnalysisResponse(resp, c.contextAnalyzePath)
}

func decodeContextAnalysisResponse(resp *http.Response, path string) (*ContextAnalysis, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeUpstreamError(resp, path)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var decoded ContextAnalysis
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	// Go 端再驗一次 contract，確保 Python 服務或雲端模型若輸出漂移，
	// 會在 client 邊界直接失敗，而不是把不完整 decision 帶進 realtime service。
	if err := validateContextAnalysis(&decoded); err != nil {
		return nil, err
	}

	return &decoded, nil
}

// AnalyzeTodo 把 prompt+text 送進 todoAnalyzePath，回應解析成 TodoAnalysis。
// 這條路徑只做 Todo Reminder 專用結構化抽取；目前不落庫、不建立待辦。
func (c *interactionClient) AnalyzeTodo(ctx context.Context, prompt string, text string) (*TodoAnalysis, error) {
	if c == nil {
		return nil, fmt.Errorf("todo analysis client is not initialized")
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("todo analysis client url is empty")
	}

	req, err := c.buildRequest(ctx, c.todoAnalyzePath, prompt, text)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return decodeTodoAnalysisResponse(resp, c.todoAnalyzePath)
}

func decodeTodoAnalysisResponse(resp *http.Response, path string) (*TodoAnalysis, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeUpstreamError(resp, path)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var decoded TodoAnalysis
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	if err := validateTodoAnalysis(&decoded); err != nil {
		return nil, err
	}

	return &decoded, nil
}

// AnalyzeTodoDueTime 把 prompt+text 送進 todoDueTimePath，回應解析成 TodoDueTimeAnalysis。
func (c *interactionClient) AnalyzeTodoDueTime(ctx context.Context, prompt string, text string) (*TodoDueTimeAnalysis, error) {
	if c == nil {
		return nil, fmt.Errorf("todo due time client is not initialized")
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("todo due time client url is empty")
	}

	req, err := c.buildRequest(ctx, c.todoDueTimePath, prompt, text)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return decodeTodoDueTimeResponse(resp, c.todoDueTimePath)
}

func decodeTodoDueTimeResponse(resp *http.Response, path string) (*TodoDueTimeAnalysis, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeUpstreamError(resp, path)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var decoded TodoDueTimeAnalysis
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	if err := validateTodoDueTimeAnalysis(&decoded); err != nil {
		return nil, err
	}
	return &decoded, nil
}
