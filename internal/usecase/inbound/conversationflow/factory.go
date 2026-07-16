package conversationflow

import (
	"strings"

	"assistant-api/internal/repository"
	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"
	"assistant-api/internal/usecase/ai/topkfilter"
)

// FactoryOptions 定義 provider-agnostic factory 的輸入。
// 設計原則：
// 1) 工廠只做依賴組裝與預設值補齊，不執行業務流程。
// 2) 平台差異（LINE/Slack/WhatsApp）透過 Messenger 與 PlatformLabel 注入。
// 3) 可先部分注入（例如 Dispatcher 為 nil），由呼叫端採漸進式上線。
type FactoryOptions struct {
	PlatformLabel string
	BotSenderID   string
	SuccessText   string

	CommandConfidenceThreshold  float64
	QuestionConfidenceThreshold float64
	DecisionJSONRetryCount      int

	Repo       *repository.ChannelMessageRepo
	TopKFilter topkfilter.Service
	LLM        llminteraction.InteractionService
	Dispatcher ActionDispatcher
	Messenger  OutboundMessenger
}

// BuildDependencies 依 FactoryOptions 產生標準化 Dependencies。
// 注意：
//   - 這裡不強制檢查依賴完整性（例如 LLM/TopKFilter 可為 nil），
//     因為 Orchestrator 內部已採防禦式 early-return。
//   - 目標是讓不同 provider 的建構流程一致，減少重複 wiring 程式碼。
func BuildDependencies(options FactoryOptions) Dependencies {
	deps := Dependencies{
		PlatformLabel:               strings.TrimSpace(options.PlatformLabel),
		BotSenderID:                 strings.TrimSpace(options.BotSenderID),
		SuccessText:                 strings.TrimSpace(options.SuccessText),
		CommandConfidenceThreshold:  options.CommandConfidenceThreshold,
		QuestionConfidenceThreshold: options.QuestionConfidenceThreshold,
		DecisionJSONRetryCount:      options.DecisionJSONRetryCount,
		Repo:                        options.Repo,
		TopKFilter:                  options.TopKFilter,
		LLM:                         options.LLM,
		Dispatcher:                  options.Dispatcher,
		Messenger:                   options.Messenger,
	}
	if deps.PlatformLabel == "" {
		deps.PlatformLabel = "messaging"
	}
	if deps.SuccessText == "" {
		deps.SuccessText = "指令已執行成功"
	}
	return deps
}

// NewFromFactory 以 provider-agnostic factory 建立 Orchestrator。
// 這是給各平台 adapter 使用的統一入口。
func NewFromFactory(options FactoryOptions) *Orchestrator {
	return &Orchestrator{deps: BuildDependencies(options)}
}
