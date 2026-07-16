package llminteraction

import usecasellminteraction "assistant-api/internal/usecase/ai/llm_interaction"

// InteractionService 定義可跨平台重用的 LLM 互動流程。
type InteractionService = usecasellminteraction.InteractionService

// NewInteractionService 建立通用 LLM 互動服務。
func NewInteractionService(client InteractionClient) InteractionService {
	return usecasellminteraction.NewInteractionService(client)
}
