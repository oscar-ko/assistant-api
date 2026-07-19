package llminteraction

import usecasellminteraction "assistant-api/internal/usecase/ai/llm_interaction"

// ActionCandidate 為 reranker 精排後、提供給 LLM 互動層參考的候選描述。
type ActionCandidate = usecasellminteraction.ActionCandidate

// ActionDecision 表示 LLM 互動模型針對候選 action 選出的最終結果。
type ActionDecision = usecasellminteraction.ActionDecision

// QuestionAnswer 表示語意服務把訊息當成問答問題時的回覆結果。
type QuestionAnswer = usecasellminteraction.QuestionAnswer

// ContextAnalysis 表示語意服務對短文本與近端上下文的內部結構化分析結果。
// integration 層只做型別轉接，實際欄位驗證仍集中在 usecase client，避免兩層規則漂移。
type ContextAnalysis = usecasellminteraction.ContextAnalysis

// TodoAnalysis 表示 Todo Reminder 專用的結構化分析結果。
type TodoAnalysis = usecasellminteraction.TodoAnalysis

// TodoDueTimeAnalysis 表示 Todo Reminder 專用時間正規化結果。
type TodoDueTimeAnalysis = usecasellminteraction.TodoDueTimeAnalysis

// InteractionClient 定義通用 LLM 互動能力。
type InteractionClient = usecasellminteraction.InteractionClient

// NewInteractionClient 由 AI integration 層建立 LLM 互動 client。
func NewInteractionClient(baseURL string, timeoutSeconds int) InteractionClient {
	return usecasellminteraction.NewInteractionClient(baseURL, timeoutSeconds)
}

// NewLocalContractInteractionClient 允許本地 provider 指定 action/question 的 endpoint path。
func NewLocalContractInteractionClient(baseURL string, timeoutSeconds int, actionDecisionPath string, questionAnswerPath string) InteractionClient {
	return usecasellminteraction.NewInteractionClientWithPaths(baseURL, timeoutSeconds, actionDecisionPath, questionAnswerPath)
}

// NewLocalContractInteractionClientWithModel 允許同一個本地 LLM interaction 服務依 profile 指定模型。
// integration 層只轉接設定，不把具體模型名稱硬編在 usecase 或 provider webhook 裡。
func NewLocalContractInteractionClientWithModel(baseURL string, timeoutSeconds int, modelName string, actionDecisionPath string, questionAnswerPath string, contextAnalyzePath string, todoAnalyzePath string, todoDueTimePath string) InteractionClient {
	// contextAnalyzePath 由 context_analyzer profile 注入，讓內部上下文分析固定走 dedicated route，
	// 不需要沿用 question_answer，也不需要在 prompt 中描述「請你扮演上下文分析器」。
	return usecasellminteraction.NewInteractionClientWithModel(baseURL, timeoutSeconds, modelName, actionDecisionPath, questionAnswerPath, contextAnalyzePath, todoAnalyzePath, todoDueTimePath)
}

// NewOpenAIInteractionClient 建立直接呼叫 OpenAI 的 interaction client。
func NewOpenAIInteractionClient(baseURL string, token string, decisionModel string, chatModel string, timeoutSeconds int, maxTokens *int, temperature *float64, useJSONResponseFmt *bool) (InteractionClient, error) {
	return usecasellminteraction.NewOpenAIInteractionClient(baseURL, token, decisionModel, chatModel, timeoutSeconds, maxTokens, temperature, useJSONResponseFmt)
}

// BuildFinalActionPrompt 依 reranker 精排後的候選清單組出最終決策提示詞。
func BuildFinalActionPrompt(candidates []ActionCandidate) string {
	return usecasellminteraction.BuildFinalActionPrompt(candidates)
}

// BuildQuestionAnswerPrompt 組出一般問答提示詞。
func BuildQuestionAnswerPrompt() string {
	return usecasellminteraction.BuildQuestionAnswerPrompt()
}

// BuildClarifyingQuestionPrompt 組出追問提示詞。
func BuildClarifyingQuestionPrompt(reason string) string {
	return usecasellminteraction.BuildClarifyingQuestionPrompt(reason)
}

// ActionParamTargetLocales 是翻譯 action 的語系參數鍵名。
const ActionParamTargetLocales = usecasellminteraction.ActionParamTargetLocales
