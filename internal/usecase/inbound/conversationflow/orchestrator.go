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

	"github.com/google/uuid"
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

type chainCommandContext struct {
	APIOperation      string
	SkillCode         string
	ActionPrompt      string
	MissingParameters []string
}

// New 建立可跨平台重用的流程實例，並補齊必要預設值。
// 回傳：初始化完成的 Orchestrator 指標；若輸入缺省會帶入安全預設。
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

// ProcessCommand 是核心主流程，負責 command 決策、分流、執行與通知。
// 1) 取候選 action
// 2) 請 LLM 做最終決策
// 3) 依 next_step 分流（執行 / 追問 / 問答）
// 4) 執行 post-action
// 5) 成功時發送通知並落庫
// 回傳：無；以副作用形式執行流程（dispatch、send message、persist log/result）。
func (o *Orchestrator) ProcessCommand(message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, senderUserID string, replyRef string, quoteRef string) {
	// 基本防禦：沒有流程實例或訊息本體時直接返回。
	if o == nil || message == nil {
		return
	}

	actionCandidates := []llminteraction.ActionCandidate(nil)
	matchedSkillCode := ""
	matchedRouteText := ""
	validSelection := false
	reusedCommand := false
	decisionInputText := buildInitialDecisionInputText(message.Text)

	// 優先路徑：reply 鏈上若已存在 action_result 對應指令，
	// 先把既有指令上下文（含缺參數）組回提示，再重跑 AI 解析。
	//
	// 這個流程的目的：
	// 1) 降低重複解析成本（top-k + LLM）
	// 2) 保持同一條指令會話的一致性，避免前後步驟被重新判成不同操作
	// 3) 讓「補參數」訊息能透過上下文重新組合成完整指令
	finalDecision := (*llminteraction.ActionDecision)(nil)
	chainContext := (*chainCommandContext)(nil)
	if resolved, resolveErr := o.resolveActionDecisionFromChain(context.Background(), savedMessage); resolveErr != nil {
		zap.L().Warn(o.logKey("resolve action command from chain failed"),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.Error(resolveErr),
		)
	} else if resolved != nil && strings.TrimSpace(resolved.APIOperation) != "" {
		reusedCommand = true
		chainContext = resolved
		matchedSkillCode = strings.TrimSpace(resolved.SkillCode)
		decisionInputText = o.buildCommandDecisionInputText(message.Text, resolved)
		zap.L().Info(o.logKey("message action command chain context resolved"),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("api_operation", strings.TrimSpace(resolved.APIOperation)),
			zap.String("skill_code", matchedSkillCode),
			zap.Strings("missing_parameters", append([]string(nil), resolved.MissingParameters...)),
		)
	}

	if finalDecision == nil {
		// 若未命中既有指令鍊，且本訊息本身也沒有明確指令訊號（例如 mention bot），
		// 則視為一般訊息直接略過，不再進入指令解析流程。
		// 這可避免純補充敘述或一般回覆在 command chain 情境下被誤判成新指令。
		if !reusedCommand && !o.hasDirectCommandSignal(message) {
			return
		}

		if reusedCommand {
			if o.deps.LLM == nil {
				return
			}
			// 命中指令鍊時固定既有 operation，不再重新挑選指令。
			// AI 只需在該 operation 下判斷：
			// 1) 參數是否已補齊
			// 2) 是否仍需追問
			actionCandidates = o.buildFixedChainOperationCandidates(chainContext)
			if len(actionCandidates) == 0 {
				return
			}
		} else {
			// 回退路徑：未命中既有指令時，走既有 top-k + LLM 指令解析流程。
			if o.deps.TopKFilter == nil {
				return
			}
			candidates := o.deps.TopKFilter.FilterMessage(context.Background(), message)
			if len(candidates) == 0 || o.deps.LLM == nil {
				return
			}
			actionCandidates = toActionCandidates(candidates)
		}

		var err error
		finalDecision, err = o.deps.LLM.DecideFinalAction(context.Background(), decisionInputText, actionCandidates)
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
		if reusedCommand && chainContext != nil && strings.TrimSpace(chainContext.APIOperation) != "" {
			// 指令鍊模式下固定 operation，避免 AI 將補參數回覆誤改成其他操作。
			finalDecision.APIOperation = strings.TrimSpace(chainContext.APIOperation)
		}
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

	// 若走解析路徑，檢查模型選到的 api_operation 是否在候選集合內，
	// 目的：防止幻覺 operation 直接進入 dispatch。
	//
	// 若走重用路徑（reusedCommand=true），
	// validSelection 已在上方命中既有指令時設為 true，
	// 因為該 operation 來源是歷史 action_result，而非模型即時生成。
	if !reusedCommand {
		for _, candidate := range actionCandidates {
			if candidate.Operation == finalDecision.APIOperation {
				matchedSkillCode = candidate.SkillCode
				matchedRouteText = candidate.RouteText
				validSelection = true
				break
			}
		}
	} else {
		for _, candidate := range actionCandidates {
			if candidate.Operation == finalDecision.APIOperation {
				if strings.TrimSpace(matchedSkillCode) == "" {
					matchedSkillCode = candidate.SkillCode
				}
				matchedRouteText = candidate.RouteText
				validSelection = true
				break
			}
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

// resolveActionDecisionFromChain 沿 reply/related 訊息鏈上溯，提取可重跑 AI 的指令上下文。
// 回傳：chainCommandContext 與 error；若未命中指令則回傳 nil 與 nil error。
func (o *Orchestrator) resolveActionDecisionFromChain(ctx context.Context, message *ent.ChannelMessage) (*chainCommandContext, error) {
	if o == nil || o.deps.Repo == nil || message == nil {
		return nil, nil
	}

	// visited 用於防止關聯資料異常形成回圈（例如 A->B->A）。
	visited := map[uuid.UUID]struct{}{}
	current := message
	for current != nil {
		if current.ID == uuid.Nil {
			return nil, nil
		}
		if _, seen := visited[current.ID]; seen {
			return nil, nil
		}
		visited[current.ID] = struct{}{}

		// 每一層都先查 action_result 是否已有 operation。
		// 一旦命中就立即回傳，避免不必要的持續上溯。
		actionResult, err := o.deps.Repo.FindLatestActionResultByMessageID(ctx, current.ID)
		if err != nil {
			return nil, err
		}
		if actionResult != nil && actionResult.Edges.Action != nil {
			operation := strings.TrimSpace(actionResult.Edges.Action.APIOperation)
			if operation == "" {
				continue
			}
			// 命中 operation 後同步回填 skill_code，
			// 讓 post-action handler 能維持嚴格 skill 驗證與資料關聯。
			skillCode, skillErr := o.deps.Repo.ResolveSkillCodeByAPIOperation(ctx, operation)
			if skillErr != nil {
				return nil, skillErr
			}
			actionPrompt, promptErr := o.deps.Repo.ResolveActionPromptByAPIOperation(ctx, operation)
			if promptErr != nil {
				return nil, promptErr
			}
			missing := parseMissingParametersFromActionResult(actionResult)
			return &chainCommandContext{
				APIOperation:      operation,
				SkillCode:         strings.TrimSpace(skillCode),
				ActionPrompt:      strings.TrimSpace(actionPrompt),
				MissingParameters: missing,
			}, nil
		}

		// 未命中時才往父節點繼續追溯。
		parent, parentErr := o.deps.Repo.ResolveParentMessage(ctx, current)
		if parentErr != nil {
			return nil, parentErr
		}
		current = parent
	}

	return nil, nil
}

// buildCommandDecisionInputText 把鏈上既有指令與缺參數上下文拼入本次訊息，供 AI 重新解析。
// 回傳：可直接送進 DecideFinalAction 的輸入文字。
func (o *Orchestrator) buildCommandDecisionInputText(messageText string, chainCtx *chainCommandContext) string {
	text := strings.TrimSpace(messageText)
	if chainCtx == nil || strings.TrimSpace(chainCtx.APIOperation) == "" {
		return text
	}

	parts := []string{
		"[decision_phase]",
		"mode=chain_parameter_fill",
		"[command_chain_context]",
		"api_operation=" + strings.TrimSpace(chainCtx.APIOperation),
	}
	if len(chainCtx.MissingParameters) > 0 {
		parts = append(parts, "missing_parameters="+strings.Join(chainCtx.MissingParameters, ","))
	}
	if strings.EqualFold(strings.TrimSpace(chainCtx.APIOperation), "start_translation_locale") && containsMissingParameter(chainCtx.MissingParameters, "target_locales") {
		parts = append(parts,
			"[parameter_fill_policy]",
			"目前任務是補齊 target_locales。",
			"使用者可用自然語言語言名稱，不需要自行輸入 BCP47。",
			"你必須把語言名稱轉成 BCP47 locale 並填入 action_params.target_locales。",
			"例如：德文=de-DE、法文=fr-FR、西班牙文=es-ES、英文=en-US、日文=ja-JP。",
			"若本句已明確提及語言名稱，應直接 execute_action，不可再次 ask_clarifying_question。",
		)
	}
	parts = append(parts, "[user_reply]", text)
	return strings.Join(parts, "\n")
}

func buildInitialDecisionInputText(messageText string) string {
	text := strings.TrimSpace(messageText)
	if text == "" {
		return ""
	}
	return strings.Join([]string{
		"[decision_phase]",
		"mode=initial_action_decision",
		"[user_message]",
		text,
	}, "\n")
}

func containsMissingParameter(params []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return false
	}
	for _, param := range params {
		if strings.ToLower(strings.TrimSpace(param)) == target {
			return true
		}
	}
	return false
}

// buildFixedChainOperationCandidates 建立「固定單一 operation」候選，用於指令鍊補參數解析。
// 回傳：只包含鏈上 operation 的候選陣列；若 operation 無效則回傳 nil。
func (o *Orchestrator) buildFixedChainOperationCandidates(chainCtx *chainCommandContext) []llminteraction.ActionCandidate {
	if chainCtx == nil {
		return nil
	}
	operation := strings.TrimSpace(chainCtx.APIOperation)
	if operation == "" {
		return nil
	}
	return []llminteraction.ActionCandidate{{
		Operation: operation,
		SkillCode: strings.TrimSpace(chainCtx.SkillCode),
		Prompt:    strings.TrimSpace(chainCtx.ActionPrompt),
	}}
}

// parseMissingParametersFromActionResult 從 action_result.result_message 抽取 missing_parameters 清單。
// 回傳：正規化且去重後的參數鍵；若無資料則回傳 nil。
func parseMissingParametersFromActionResult(item *ent.ActionResult) []string {
	if item == nil || item.ResultMessage == nil {
		return nil
	}
	result := strings.TrimSpace(*item.ResultMessage)
	if result == "" {
		return nil
	}

	segments := strings.Split(result, ";")
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		const keyPrefix = "missing_parameters="
		lowered := strings.ToLower(segment)
		if !strings.HasPrefix(lowered, keyPrefix) {
			continue
		}
		value := strings.TrimSpace(segment[len(keyPrefix):])
		if value == "" {
			return nil
		}
		raw := strings.Split(value, ",")
		seen := make(map[string]struct{}, len(raw))
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			key := strings.ToLower(strings.TrimSpace(item))
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, key)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}

	return nil
}

// hasDirectCommandSignal 判斷訊息本身是否具備直接進入指令模式的訊號。
// 回傳：true 表示可視為 command 訊息；false 表示需依賴鏈上既有指令才可進流程。
func (o *Orchestrator) hasDirectCommandSignal(message *unifiedmessage.Message) bool {
	if message == nil {
		return false
	}
	// 目前 direct command 訊號定義：
	// - 私聊訊息（沿用既有 command gate）
	// - 訊息明確 mention bot（與 command gate 一致）
	// - 若 bot id 尚未配置，退回「存在 mention 標記」作為保守判定
	// 其餘情境則需依賴「鏈上既有指令」才能繼續 command 流程。
	if strings.EqualFold(strings.TrimSpace(message.ChannelType), "private") {
		return true
	}
	botUserID := strings.TrimSpace(o.deps.BotSenderID)
	if botUserID != "" {
		return message.MentionsUser(botUserID)
	}
	return len(message.Mentions) > 0
}

// dispatchActionPostHandlers 依最終決策分派 post-action handler，並在成功後送出成功通知。
// 回傳：無；失敗情境採早退，不拋錯中斷主流程。
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

// sendActionSuccessNotice 發送「指令執行成功」通知，並將 outbound 訊息落庫。
// 回傳：無；發送或落庫失敗僅記錄 log，不回傳錯誤阻斷流程。
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

// routeMessageToQuestionAnswer 將訊息導向問答或追問流程，並處理低信心降級。
// 回傳：無；必要時會透過 messenger 發送追問訊息並落庫。
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

// sendClarifyingQuestion 發送釐清問題（clarifying question）並寫入訊息紀錄。
// 回傳：無；若發送失敗僅記錄告警並結束。
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

// buildMissingParameterTemplateQuestion 依缺參數清單產生固定追問模板。
// 回傳：可直接回覆給使用者的追問字串；若無可用模板則回傳空字串。
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

// persistMissingParameterResult 將 missing_parameter 結果統一寫入 action_results。
// 回傳：無；寫入失敗僅記錄警告，避免阻斷主要互動流程。
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

// toActionCandidates 將 topkfilter 候選轉換為 LLM 互動層使用的契約型別。
// 回傳：llm_interaction.ActionCandidate 陣列，供最終決策模型使用。
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

// logKey 生成帶平台前綴的統一日誌 key。
// 回傳：格式化後的 log key 字串（例如 line message final action decided）。
func (o *Orchestrator) logKey(suffix string) string {
	// logKey 確保抽出共用流程後，log 仍可辨識平台來源。
	prefix := strings.TrimSpace(o.deps.PlatformLabel)
	if prefix == "" {
		prefix = "messaging"
	}
	return prefix + " " + strings.TrimSpace(suffix)
}
