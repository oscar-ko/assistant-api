package conversationflow

import (
	"context"
	"strings"
	"time"

	"assistant-api/internal/ent"
	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"
	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"
	"assistant-api/internal/usecase/ai/topkfilter"

	"go.uber.org/zap"
)

// ActionDispatcher 定義跨平台共用的 action 後處理（post-action）分派介面。
type ActionDispatcher interface {
	// Dispatch 會依最終決策執行對應 operation 的副作用邏輯（例如寫入關聯表、更新狀態）。
	// 回傳值語意（非常重要）：
	// - true: 表示副作用已成功完成，呼叫端可以對使用者發送「執行成功」通知。
	// - false: 表示副作用失敗或被略過，呼叫端不得發送成功通知，以免誤導使用者。
	Dispatch(message *unifiedmessage.Message, senderUserID string, decision *llminteraction.ActionDecision, matchedSkillCode string) bool
}

// OutboundMessenger 定義平台層的外送訊息能力。
// Orchestrator 只依賴這個抽象，不直接碰 LINE/Slack/WhatsApp SDK。
type OutboundMessenger interface {
	// SendText 以平台中立參數送出文字訊息。
	// 參數對應約定：
	// - chatID: 目標對話識別（群組/聊天室/私聊）。
	// - userID: 群組情境下可作為 mention 目標；私聊可為空或等於 chatID。
	// - replyRef: 平台的回覆參考（如 reply token/thread ts）；空字串代表非回覆送出。
	// - quoteRef: 平台的引用參考；若平台不支援可忽略。
	// 回傳值：平台訊息 ID（若平台可提供），供後續落庫追蹤。
	SendText(ctx context.Context, chatID string, userID string, text string, replyRef string, quoteRef string) (string, error)
}

// Dependencies 收斂 Orchestrator 所需的所有依賴與門檻設定。
type Dependencies struct {
	// PlatformLabel 僅用於 log 前綴，讓同一份流程在多平台下仍可快速辨識來源。
	// 例如：line / slack / whatsapp。
	PlatformLabel string
	// BotSenderID 會寫入 outbound 訊息的 sender_id，應為機器人穩定身份。
	// 不應使用真人使用者 ID。
	BotSenderID string
	// SuccessText 為副作用成功後對使用者顯示的成功訊息文案。
	// 若為空，New() 會補預設值，避免呼叫端重複判斷。
	SuccessText string

	// CommandConfidenceThreshold 用於 execute_action 的信心門檻。
	// 若低於門檻，流程會降級為追問（clarifying question），避免誤執行。
	CommandConfidenceThreshold float64
	// QuestionConfidenceThreshold 目前不會阻擋回覆送出。
	// 目前僅用於產生 recommend_cloud_llm 的觀測訊號。
	QuestionConfidenceThreshold float64

	// Repo：負責 outbound 訊息落庫與 action_results 寫入。
	Repo *repository.ChannelMessageRepo
	// TopKFilter：根據訊息語意與上下文提供已排序的 action 候選。
	TopKFilter topkfilter.Service
	// LLM：輸出最終 action 決策與問答/追問結果。
	LLM llminteraction.InteractionService
	// Dispatcher：執行 operation 對應的副作用。
	Dispatcher ActionDispatcher
	// Messenger：平台外送訊息抽象（reply/push/thread 等由 adapter 實作）。
	Messenger OutboundMessenger
}

// Orchestrator 封裝「平台無關」的指令決策與後續互動流程。
type Orchestrator struct {
	deps Dependencies
}

// New 建立可跨平台重用的流程實例。
func New(deps Dependencies) *Orchestrator {
	// 先做預設值正規化，讓下游流程可直接使用，減少分支噪音。
	if strings.TrimSpace(deps.PlatformLabel) == "" {
		deps.PlatformLabel = "messaging"
	}
	if strings.TrimSpace(deps.SuccessText) == "" {
		deps.SuccessText = "指令已執行成功"
	}
	return &Orchestrator{deps: deps}
}

// ProcessCommand 是核心主流程：
// 1) 取候選 action
// 2) 請 LLM 做最終決策
// 3) 依 next_step 分流（執行 / 追問 / 問答）
// 4) 執行 post-action
// 5) 成功時發送通知並落庫
func (o *Orchestrator) ProcessCommand(message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, senderUserID string, replyRef string, quoteRef string) {
	// 基本防禦：沒有流程實例或訊息本體時直接返回。
	if o == nil || message == nil {
		return
	}
	// 若未注入 top-k，代表此部署尚未啟用 action 決策能力。
	// 這裡選擇靜默返回，避免半套配置導致錯誤行為。
	if o.deps.TopKFilter == nil {
		return
	}

	// 階段 1：取得已排序候選(使用 Top-K 及 Reranker)。
	// 候選為空表示目前訊息沒有可執行 action，流程應安靜結束。
	candidates := o.deps.TopKFilter.FilterMessage(context.Background(), message)
	if len(candidates) == 0 || o.deps.LLM == nil {
		return
	}

	// 階段 2：轉換候選格式到 LLM 契約型別。
	actionCandidates := toActionCandidates(candidates)
	// 階段 3：請 LLM 產生最終決策。
	finalDecision, err := o.deps.LLM.DecideFinalAction(context.Background(), message.Text, actionCandidates)
	if err != nil {
		// 決策錯誤分兩類商業語意：
		// - 契約/驗證錯誤：可修復，走 ask_clarifying_question
		// - 其他執行錯誤：先走 answer_question 避免流程中斷
		// 核心原則：不讓使用者遇到「無回應」的靜默失敗。
		zap.L().Warn(o.logKey("message final action decision failed"),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("text", strings.TrimSpace(message.Text)),
			zap.String("error_message", err.Error()),
			zap.Error(err),
		)

		cause := "answer_question"
		apiOperation := ""
		missingParameters := []string(nil)
		if llminteraction.IsDecisionValidationError(err) {
			// 驗證錯誤通常可透過追問補參數修復，因此轉成追問路徑。
			cause = "ask_clarifying_question"
			if vErr := llminteraction.AsDecisionValidationError(err); vErr != nil {
				// 盡量使用 typed error 內的結構化欄位，提升資料可分析性。
				apiOperation = strings.TrimSpace(vErr.APIOperation)
				missingParameters = append([]string(nil), vErr.MissingParameters...)
			}
		}

		// 即使尚未進入 dispatch，也把缺參數狀態寫入 action_results。
		// source=contract_reject 代表：模型產生了執行型輸出，但在契約層被拒絕。
		o.persistMissingParameterResult(savedMessage, apiOperation, "contract_reject", missingParameters, err.Error())
		// 立刻切到問答/追問路徑，避免錯誤被吞掉。
		o.routeMessageToQuestionAnswer(message, savedMessage, strings.TrimSpace(senderUserID), 0, o.deps.CommandConfidenceThreshold, cause, err.Error(), missingParameters)
		return
	}
	if finalDecision == nil {
		// nil 決策視為上游策略性略過，不當作錯誤。
		return
	}

	zap.L().Info(o.logKey("message action decision evaluated"),
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("text", strings.TrimSpace(message.Text)),
		zap.String("next_step", strings.TrimSpace(finalDecision.NextStep)),
		zap.String("api_operation", finalDecision.APIOperation),
		zap.Float64("confidence", finalDecision.Confidence),
		zap.String("reason", strings.TrimSpace(finalDecision.Reason)),
	)

	nextStep := strings.TrimSpace(finalDecision.NextStep)
	confidenceThreshold := o.deps.CommandConfidenceThreshold
	switch nextStep {
	case llminteraction.NextStepAskClarifyingQuestion:
		// 模型主動要求追問：寫成 missing_parameter，
		// 與 contract_reject 共用同一狀態，便於統一查詢與統計。
		o.persistMissingParameterResult(savedMessage, strings.TrimSpace(finalDecision.APIOperation), "llm_clarify", finalDecision.MissingParameters, strings.TrimSpace(finalDecision.Reason))
		o.routeMessageToQuestionAnswer(message, savedMessage, strings.TrimSpace(senderUserID), finalDecision.Confidence, confidenceThreshold, "ask_clarifying_question", finalDecision.Reason, finalDecision.MissingParameters)
		return
	case llminteraction.NextStepAnswerQuestion:
		// 明確問答模式：不進入 action dispatch。
		o.routeMessageToQuestionAnswer(message, savedMessage, strings.TrimSpace(senderUserID), finalDecision.Confidence, confidenceThreshold, "answer_question", finalDecision.Reason, nil)
		return
	case llminteraction.NextStepExecuteAction:
		// execute_action：繼續往下執行。
	default:
		// 未知 next_step 視為契約無效輸出，直接終止本次流程。
		zap.L().Warn(o.logKey("message llm interaction next_step invalid"),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("next_step", nextStep),
		)
		return
	}

	if confidenceThreshold > 0 && finalDecision.Confidence < confidenceThreshold {
		// execute_action 信心不足時降級為追問，避免低品質誤操作。
		o.routeMessageToQuestionAnswer(message, savedMessage, strings.TrimSpace(senderUserID), finalDecision.Confidence, confidenceThreshold, "low_action_confidence", finalDecision.Reason, nil)
		return
	}

	// 檢查模型選到的 api_operation 是否在候選集合內，
	// 目的：防止幻覺 operation 直接進入 dispatch。
	matchedSkillCode := ""
	matchedRouteText := ""
	validSelection := false
	for _, candidate := range actionCandidates {
		if candidate.Operation == finalDecision.APIOperation {
			matchedSkillCode = candidate.SkillCode
			matchedRouteText = candidate.RouteText
			validSelection = true
			break
		}
	}
	if !validSelection {
		// 只告警不 panic：真正執行權仍在 dispatcher，
		// 若沒有對應 handler 會安全略過。
		zap.L().Warn(o.logKey("message final action not in candidates"),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("api_operation", finalDecision.APIOperation),
		)
	}

	zap.L().Info(o.logKey("message final action decided"),
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("text", strings.TrimSpace(message.Text)),
		zap.String("next_step", strings.TrimSpace(finalDecision.NextStep)),
		zap.String("api_operation", finalDecision.APIOperation),
		zap.String("skill_code", matchedSkillCode),
		zap.String("matched_route_text", matchedRouteText),
		zap.Bool("valid_selection", validSelection),
		zap.Float64("confidence", finalDecision.Confidence),
		zap.String("reason", finalDecision.Reason),
	)

	o.dispatchActionPostHandlers(message, savedMessage, strings.TrimSpace(senderUserID), strings.TrimSpace(replyRef), strings.TrimSpace(quoteRef), finalDecision, matchedSkillCode)
}

func (o *Orchestrator) dispatchActionPostHandlers(message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, userID string, replyRef string, quoteRef string, decision *llminteraction.ActionDecision, matchedSkillCode string) {
	// Dispatcher 可選，方便分階段上線或測試時局部注入。
	if o == nil || decision == nil || o.deps.Dispatcher == nil {
		return
	}
	// 只有副作用成功，才允許發送成功通知。
	succeeded := o.deps.Dispatcher.Dispatch(message, userID, decision, matchedSkillCode)
	if !succeeded {
		return
	}
	o.sendActionSuccessNotice(message, savedMessage, userID, replyRef, quoteRef)
}

func (o *Orchestrator) sendActionSuccessNotice(message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, userID string, replyRef string, quoteRef string) {
	// 發送成功通知至少需要 messenger 與訊息上下文。
	if o == nil || o.deps.Messenger == nil || message == nil {
		return
	}

	userID = strings.TrimSpace(userID)
	replyRef = strings.TrimSpace(replyRef)
	quoteRef = strings.TrimSpace(quoteRef)
	if userID == "" {
		return
	}

	text := strings.TrimSpace(o.deps.SuccessText)
	if text == "" {
		text = "指令已執行成功"
	}

	chatID := strings.TrimSpace(message.ChannelID)
	sentPlatformMessageID := ""
	usedReply := false
	if replyRef != "" {
		// 優先走 reply：可把成功訊息掛在同一指令脈絡下。
		replySentID, err := o.deps.Messenger.SendText(
			context.Background(),
			chatID,
			userID,
			text,
			replyRef,
			quoteRef,
		)
		if err != nil {
			zap.L().Warn(o.logKey("action success notification reply failed, fallback to push"),
				zap.String("channel_id", chatID),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("user_id", userID),
				zap.Error(err),
			)
		} else {
			sentPlatformMessageID = replySentID
			usedReply = true
		}
	}

	if sentPlatformMessageID == "" {
		// reply 不可用或失敗時改走 push/direct，確保至少送達。
		pushSentID, err := o.deps.Messenger.SendText(
			context.Background(),
			chatID,
			userID,
			text,
			"",
			quoteRef,
		)
		if err != nil {
			zap.L().Warn(o.logKey("action success notification push failed"),
				zap.String("channel_id", chatID),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("user_id", userID),
				zap.Error(err),
			)
			return
		}
		sentPlatformMessageID = pushSentID
	}

	zap.L().Info(o.logKey("action success notification sent"),
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("user_id", userID),
		zap.Bool("used_reply", usedReply),
		zap.String("sent_platform_message_id", sentPlatformMessageID),
	)

	if o.deps.Repo == nil || savedMessage == nil {
		// 持久化採 best-effort，不可阻塞使用者可見回覆。
		return
	}
	// 將成功通知落庫，related_message_id 指回觸發指令，
	// 便於前後端做對話鏈追溯與稽核。
	if _, err := o.deps.Repo.SaveSentMessage(
		context.Background(),
		savedMessage.ChannelID,
		strings.TrimSpace(o.deps.BotSenderID),
		"",
		sentPlatformMessageID,
		text,
		"text",
		time.Now().UnixMilli(),
		savedMessage.ID,
	); err != nil {
		zap.L().Warn(o.logKey("persist action success notification failed"),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("user_id", userID),
			zap.String("sent_platform_message_id", sentPlatformMessageID),
			zap.Error(err),
		)
	}
}

func (o *Orchestrator) routeMessageToQuestionAnswer(message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, userID string, actionConfidence float64, actionThreshold float64, cause string, decisionReason string, missingParameters []string) {
	// 此函式是問答/追問共用路由，涵蓋三種來源：
	// - execute_action 低信心降級
	// - next_step=answer_question
	// - 決策契約失敗後的追問 fallback
	if o == nil || o.deps.LLM == nil || message == nil {
		return
	}

	var (
		qa     *llminteraction.QuestionAnswer
		qaErr  error
		qaMode = "question_answer"
	)
	if strings.EqualFold(strings.TrimSpace(cause), "low_action_confidence") || strings.EqualFold(strings.TrimSpace(cause), "ask_clarifying_question") {
		qaMode = "clarifying_question"
		// 若缺參數是明確的，優先使用固定模板追問，
		// 可降低模型波動並提升回覆一致性。
		if template := buildMissingParameterTemplateQuestion(missingParameters); strings.TrimSpace(template) != "" {
			qa = &llminteraction.QuestionAnswer{
				SchemaVersion: "v1",
				Answer:        template,
				Confidence:    1,
			}
		} else {
			// 無可用模板時，再交給模型生成情境化追問。
			qa, qaErr = o.deps.LLM.AskClarifyingQuestion(context.Background(), message.Text, decisionReason)
		}
	} else {
		// 非追問情境走一般問答。
		qa, qaErr = o.deps.LLM.AnswerQuestion(context.Background(), message.Text)
	}
	if qaErr != nil {
		zap.L().Warn(o.logKey("message semantic question answer failed"),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("cause", cause),
			zap.String("decision_reason", strings.TrimSpace(decisionReason)),
			zap.String("mode", qaMode),
			zap.Float64("action_confidence", actionConfidence),
			zap.Float64("action_threshold", actionThreshold),
			zap.Error(qaErr),
		)
		return
	}
	if qa == nil {
		zap.L().Info(o.logKey("message semantic question answer skipped"),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("cause", cause),
			zap.String("decision_reason", strings.TrimSpace(decisionReason)),
			zap.String("mode", qaMode),
			zap.Float64("action_confidence", actionConfidence),
			zap.Float64("action_threshold", actionThreshold),
		)
		return
	}

	questionThreshold := o.deps.QuestionConfidenceThreshold
	// 目前策略：即使答案信心偏低仍可回覆，但會打觀測訊號，
	// 供後續 cloud 升級策略使用。
	recommendCloudLLM := questionThreshold > 0 && qa.Confidence < questionThreshold
	zap.L().Info(o.logKey("message semantic question answered"),
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("text", strings.TrimSpace(message.Text)),
		zap.String("cause", cause),
		zap.String("decision_reason", strings.TrimSpace(decisionReason)),
		zap.String("mode", qaMode),
		zap.Float64("action_confidence", actionConfidence),
		zap.Float64("action_threshold", actionThreshold),
		zap.String("answer", strings.TrimSpace(qa.Answer)),
		zap.Float64("answer_confidence", qa.Confidence),
		zap.Float64("answer_threshold", questionThreshold),
		zap.Bool("recommend_cloud_llm", recommendCloudLLM),
	)
	if recommendCloudLLM {
		zap.L().Warn(o.logKey("message semantic answer suggests cloud llm fallback"),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.Float64("answer_confidence", qa.Confidence),
			zap.Float64("answer_threshold", questionThreshold),
		)
	}

	if qaMode == "clarifying_question" {
		// 目前只有追問模式會發送 follow-up 訊息。
		o.sendClarifyingQuestion(message, savedMessage, userID, strings.TrimSpace(qa.Answer))
	}
}

func (o *Orchestrator) sendClarifyingQuestion(message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, userID string, question string) {
	// 追問目前採非 reply 送出，
	// 以支援延遲生成、重送或非同步流程情境。
	if o == nil || o.deps.Messenger == nil || message == nil {
		return
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return
	}

	sentPlatformMessageID, err := o.deps.Messenger.SendText(
		context.Background(),
		strings.TrimSpace(message.ChannelID),
		strings.TrimSpace(userID),
		question,
		"",
		"",
	)
	if err != nil {
		zap.L().Warn(o.logKey("clarifying question push failed"),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("user_id", strings.TrimSpace(userID)),
			zap.Error(err),
		)
		return
	}

	zap.L().Info(o.logKey("clarifying question pushed"),
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("user_id", strings.TrimSpace(userID)),
		zap.String("sent_platform_message_id", sentPlatformMessageID),
	)

	if o.deps.Repo == nil || savedMessage == nil {
		return
	}
	// 追問落庫後與觸發訊息建立關聯，確保對話鏈可追溯。
	if _, err := o.deps.Repo.SaveSentMessage(
		context.Background(),
		savedMessage.ChannelID,
		strings.TrimSpace(o.deps.BotSenderID),
		"",
		sentPlatformMessageID,
		question,
		"text",
		time.Now().UnixMilli(),
		savedMessage.ID,
	); err != nil {
		zap.L().Warn(o.logKey("persist clarifying question failed"),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("sent_platform_message_id", sentPlatformMessageID),
			zap.Error(err),
		)
	}
}

func buildMissingParameterTemplateQuestion(missingParameters []string) string {
	// 將缺參數清單轉成固定追問文案。
	// 正規化目標：
	// - 去除空白並轉小寫
	// - 去重
	// - 命中特定高頻參數時使用專用模板
	if len(missingParameters) == 0 {
		return ""
	}
	normalized := make([]string, 0, len(missingParameters))
	seen := make(map[string]struct{}, len(missingParameters))
	for _, parameter := range missingParameters {
		key := strings.ToLower(strings.TrimSpace(parameter))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	if len(normalized) == 0 {
		return ""
	}
	// 領域特化模板：翻譯語系參數。
	if len(normalized) == 1 && normalized[0] == llminteraction.ActionParamTargetLocales {
		return "請告訴我要翻譯成哪些語言？例如英文、日文。"
	}
	// 通用 fallback：直接列出缺少的參數鍵。
	return "請補充以下必要資訊後我才能執行指令：" + strings.Join(normalized, ", ")
}

func (o *Orchestrator) persistMissingParameterResult(savedMessage *ent.ChannelMessage, apiOperation string, source string, missingParameters []string, reason string) {
	// missing_parameter 狀態統一落庫入口。
	// source 目前約定：
	// - llm_clarify: 模型主動追問
	// - contract_reject: 模型輸出被契約拒絕
	if o == nil || o.deps.Repo == nil || savedMessage == nil {
		return
	}
	apiOperation = strings.TrimSpace(apiOperation)
	if apiOperation == "" {
		return
	}
	seen := make(map[string]struct{}, len(missingParameters))
	normalized := make([]string, 0, len(missingParameters))
	for _, parameter := range missingParameters {
		key := strings.ToLower(strings.TrimSpace(parameter))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	if len(normalized) == 0 {
		// 若上游未提供結構化缺參數，嘗試從 reason 文字推斷。
		normalized = llminteraction.InferMissingParametersFromReason(reason)
	}
	// resultMessage 採 key=value 壓縮格式，方便快速搜尋與稽核。
	resultMessage := "source=" + strings.TrimSpace(source)
	if len(normalized) > 0 {
		resultMessage += ";missing_parameters=" + strings.Join(normalized, ",")
	}
	if trimmedReason := strings.TrimSpace(reason); trimmedReason != "" {
		resultMessage += ";reason=" + trimmedReason
	}
	if err := o.deps.Repo.UpsertActionResult(context.Background(), apiOperation, savedMessage.ID, "missing_parameter", resultMessage); err != nil {
		zap.L().Warn(o.logKey("persist missing-parameter action result failed"),
			zap.String("channel_id", savedMessage.ChannelID.String()),
			zap.String("message_id", savedMessage.ID.String()),
			zap.String("api_operation", apiOperation),
			zap.Error(err),
		)
	}
}

func toActionCandidates(candidates []topkfilter.ScoredCandidate) []llminteraction.ActionCandidate {
	// 候選轉換集中在這裡，避免 topkfilter 型別滲透到 LLM 互動層。
	out := make([]llminteraction.ActionCandidate, 0, len(candidates))
	for _, item := range candidates {
		out = append(out, llminteraction.ActionCandidate{
			Operation: item.Candidate.APIOperation,
			SkillCode: item.Candidate.SkillCode,
			RouteText: item.Candidate.RouteText,
			Prompt:    item.Candidate.Prompt,
			Score:     item.Score,
		})
	}
	return out
}

func (o *Orchestrator) logKey(suffix string) string {
	// logKey 確保抽出共用流程後，log 仍可辨識平台來源。
	prefix := strings.TrimSpace(o.deps.PlatformLabel)
	if prefix == "" {
		prefix = "messaging"
	}
	return prefix + " " + strings.TrimSpace(suffix)
}
