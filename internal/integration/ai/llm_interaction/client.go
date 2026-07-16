package llminteraction

import usecasellminteraction "assistant-api/internal/usecase/ai/llm_interaction"

// ActionCandidate 為 reranker 精排後、提供給 LLM 互動層參考的候選描述。
type ActionCandidate = usecasellminteraction.ActionCandidate

// ActionDecision 表示 LLM 互動模型針對候選 action 選出的最終結果。
type ActionDecision = usecasellminteraction.ActionDecision

// QuestionAnswer 表示語意服務把訊息當成問答問題時的回覆結果。
type QuestionAnswer = usecasellminteraction.QuestionAnswer

// InteractionClient 定義通用 LLM 互動能力。
type InteractionClient = usecasellminteraction.InteractionClient

// NewInteractionClient 由 AI integration 層建立 LLM 互動 client。
func NewInteractionClient(baseURL string, timeoutSeconds int) InteractionClient {
	return usecasellminteraction.NewInteractionClient(baseURL, timeoutSeconds)
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
