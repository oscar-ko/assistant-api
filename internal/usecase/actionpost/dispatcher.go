package actionpost

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"
	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// translationStartOperation 是翻譯啟用動作在 action 決策層的 operation 名稱。
	// Dispatcher 會用這個值把決策結果路由到對應 side-effect handler。
	translationStartOperation = "start_translation_locale"
	// translationStopAllOperation 代表關閉發起者在目前 channel 的整體翻譯服務。
	translationStopAllOperation = "stop_translation_all"
	// translationStopLocaleOperation 代表移除發起者在目前 channel 建立的指定翻譯語系。
	translationStopLocaleOperation = "stop_translation_locale"
	// todoReminderStartOperation 代表在目前 channel 啟用待辦提醒即時文字掃描服務。
	todoReminderStartOperation = "start_todo_reminder"
	// todoReminderStopOperation 代表移除發起者在目前 channel 啟用的待辦提醒服務。
	todoReminderStopOperation = "stop_todo_reminder"
)

// Handler 是 action 決策後的擴充處理函式簽名。
//
// 參數說明：
// - message: 統一訊息模型（跨平台 adapter 轉換後）
// - senderUserID: 平台來源使用者 ID（可為空，handler 內可再 fallback）
// - decision: final action decision（包含 api_operation 與 action_params）
// - matchedSkillCode: 上游候選比對出的 skill_code（可為空）
// 回傳值：true 代表該 action 副作用執行成功。
type Handler func(message *unifiedmessage.Message, senderUserID string, decision *llminteraction.ActionDecision, matchedSkillCode string) bool

// Dispatcher 依 api_operation 分派 post-action handler。
//
// 設計目標：
// - 把「平台 webhook 流程」與「action 副作用執行」解耦
// - 新增 action 時，優先在這層註冊 handler，避免散落到各 provider
// - 讓 LINE/Slack/WhatsApp 等平台可共用同一份 post-action 邏輯
type Dispatcher struct {
	handlers map[string]Handler
}

// New 建立 dispatcher，並把 operation key 正規化為 trim+lower。
//
// 正規化策略可避免以下問題：
// - 上游決策含多餘空白
// - operation 大小寫不一致
//
// 若 key 為空或 handler 為 nil 會直接略過，避免 runtime dispatch 觸發 panic。
func New(handlers map[string]Handler) *Dispatcher {
	normalized := make(map[string]Handler, len(handlers))
	for operation, handler := range handlers {
		key := strings.ToLower(strings.TrimSpace(operation))
		if key == "" || handler == nil {
			continue
		}
		normalized[key] = handler
	}
	return &Dispatcher{handlers: normalized}
}

// NewDefaultDispatcher 提供目前通用的預設 handler 組合。
//
// 擴充方式：
// 1) 先實作新的 Handler（建議放在本 package）
// 2) 在此 map 註冊 operation -> handler
// 3) 各 provider 無需修改 dispatch 流程，即可共用新能力
func NewDefaultDispatcher(repo *repository.ChannelMessageRepo) *Dispatcher {
	return New(map[string]Handler{
		translationStartOperation:      NewPersistTranslationCommandStateHandler(repo),
		translationStopAllOperation:    NewRemoveTranslationCommandStateHandler(repo),
		translationStopLocaleOperation: NewRemoveTranslationCommandStateHandler(repo),
		todoReminderStartOperation:     NewPersistTodoReminderCommandStateHandler(repo),
		todoReminderStopOperation:      NewRemoveTodoReminderCommandStateHandler(repo),
	})
}

// Dispatch 依 decision 的 api_operation 做 post-action 分派。
//
// 行為特性：
// - d 或 decision 為 nil 時靜默返回（不阻塞主流程）
// - 找不到 handler 時靜默返回（允許部分 action 尚未有 side-effect）
// - 不會回傳錯誤；錯誤由各 handler 內自行記錄與觀測
func (d *Dispatcher) Dispatch(message *unifiedmessage.Message, senderUserID string, decision *llminteraction.ActionDecision, matchedSkillCode string) bool {
	if d == nil || decision == nil {
		return false
	}
	channelID := ""
	messageID := ""
	if message != nil {
		channelID = strings.TrimSpace(message.ChannelID)
		messageID = strings.TrimSpace(message.PlatformMessageID)
	}
	operation := strings.ToLower(strings.TrimSpace(decision.APIOperation))
	handler, ok := d.handlers[operation]
	if !ok || handler == nil {
		zap.L().Debug("action post handler not registered",
			zap.String("channel_id", channelID),
			zap.String("message_id", messageID),
			zap.String("api_operation", operation),
		)
		return false
	}
	zap.L().Debug("action post handler dispatching",
		zap.String("channel_id", channelID),
		zap.String("message_id", messageID),
		zap.String("api_operation", operation),
	)
	succeeded := handler(message, senderUserID, decision, matchedSkillCode)
	// succeeded 只代表「該 operation 的 post-action side-effect 已完成」。
	// Webhook 會據此決定是否發送使用者可見的成功通知。
	zap.L().Debug("action post handler executed",
		zap.String("channel_id", channelID),
		zap.String("message_id", messageID),
		zap.String("api_operation", operation),
		zap.Bool("succeeded", succeeded),
	)
	return succeeded
}

// NewPersistTranslationCommandStateHandler 建立翻譯啟用副作用的共用 handler。
//
// 目前副作用包含：
// 1) 將發起者加入 channel_service_member
// 2) 將 target locale 寫入 translation_locales
//
// 錯誤策略：
// - 採「可觀測但不中斷主流程」：遇錯只記錄 warn 並返回
// - locale 寫入採逐筆容錯：單一 locale 失敗不影響其他 locale
func NewPersistTranslationCommandStateHandler(repo *repository.ChannelMessageRepo) Handler {
	return func(message *unifiedmessage.Message, senderUserID string, decision *llminteraction.ActionDecision, matchedSkillCode string) bool {
		// 嚴格模式：必要依賴缺失時直接記錄 error，並停止本次副作用。
		if repo == nil || message == nil || decision == nil {
			zap.L().Error("translation command persistence failed: missing required dependency",
				zap.Bool("repo_nil", repo == nil),
				zap.Bool("message_nil", message == nil),
				zap.Bool("decision_nil", decision == nil),
			)
			return false
		}
		// 只處理 start_translation_locale；其他 operation 由其他 handler 負責。
		if !strings.EqualFold(strings.TrimSpace(decision.APIOperation), translationStartOperation) {
			return false
		}

		apiOperation := strings.TrimSpace(decision.APIOperation)

		// unified message 的 ChannelID 是平台外部識別（例如 LINE group/user id），
		// 不是資料庫 channel 主鍵；這裡必須先解析/查回內部 channel UUID。
		channel, err := repo.GetChannelByPlatformGroupID(
			context.Background(),
			strings.TrimSpace(message.Platform),
			strings.TrimSpace(message.ChannelID),
		)
		if err != nil || channel == nil || channel.ID == uuid.Nil {
			// 此處不允許補建 channel：
			// 若 channel 不存在，代表來源尚未完成綁定初始化，
			// 持久化 action_result 會失去正確歸屬，故直接失敗返回。
			zap.L().Error("translation command persistence failed: resolve internal channel failed",
				zap.String("platform", strings.TrimSpace(message.Platform)),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("channel_type", strings.TrimSpace(message.ChannelType)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.Error(err),
			)
			return false
		}
		channelUUID := channel.ID

		persistedMessage, err := repo.FindMessageByPlatformMessageID(context.Background(), channelUUID, strings.TrimSpace(message.PlatformMessageID))
		if err != nil {
			zap.L().Error("translation command persistence failed: resolve persisted message for action result",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("api_operation", apiOperation),
				zap.Error(err),
			)
			return false
		}
		if persistedMessage == nil || persistedMessage.ID == uuid.Nil {
			zap.L().Error("translation command persistence failed: persisted message not found for action result",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("api_operation", apiOperation),
			)
			return false
		}

		persistResult := func(status string, resultMessage string) bool {
			// action_results 在此作為「可查詢、可統計」的執行狀態來源。
			// status 建議語意：
			// - missing_parameter: 指令意圖成立，但缺必要參數
			// - failed: 已具備參數但執行過程失敗
			// - success: 執行完成
			// 這樣前後端不必解析 log 就能還原每次指令執行結果。
			if err := repo.UpsertActionResult(context.Background(), apiOperation, persistedMessage.ID, status, resultMessage); err != nil {
				zap.L().Error("translation command persistence failed: upsert action result",
					zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
					zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
					zap.String("persisted_message_id", persistedMessage.ID.String()),
					zap.String("api_operation", apiOperation),
					zap.String("result_status", status),
					zap.Error(err),
				)
				return false
			}
			return true
		}

		// 從通用 action_params 抽取 locale；缺參時記錄 missing_parameter。
		targetLocales := ExtractTranslationTargetLocales(decision)
		if len(targetLocales) == 0 {
			_ = persistResult("missing_parameter", "target_locales is required")
			zap.L().Error("translation command persistence failed: missing required locale params",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("api_operation", apiOperation),
			)
			return false
		}

		// senderUserID 必須由 caller 提供；不再 fallback，避免來源不明。
		senderUserID = strings.TrimSpace(senderUserID)
		if senderUserID == "" || strings.EqualFold(senderUserID, "unknown") {
			_ = persistResult("failed", "missing sender user id")
			zap.L().Error("translation command persistence failed: missing sender user id",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("sender_user_id", senderUserID),
			)
			return false
		}

		// 先把平台 user id 解析成系統內 user id；解析失敗則不進行後續任何寫入。
		ownerUserID, err := repo.ResolveUserIDByLineUserID(context.Background(), senderUserID)
		if err != nil {
			_ = persistResult("failed", "resolve owner user failed")
			zap.L().Error("translation command persistence failed: resolve owner user failed",
				zap.String("line_user_id", senderUserID),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.Error(err),
			)
			return false
		}
		// uuid.Nil 代表未綁定，視為錯誤。
		if ownerUserID == uuid.Nil {
			_ = persistResult("failed", "line user not bound")
			zap.L().Error("translation command persistence failed: line user not bound",
				zap.String("line_user_id", senderUserID),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			)
			return false
		}

		// 嚴格模式：必須帶上游映射出的 skillCode，不再使用預設值。
		skillCode := strings.TrimSpace(matchedSkillCode)
		if skillCode == "" {
			_ = persistResult("failed", "missing matched skill code")
			zap.L().Error("translation command persistence failed: missing matched skill code",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("line_user_id", senderUserID),
			)
			return false
		}
		// skillID 解析失敗或不存在都不應落庫，避免資料關聯錯誤。
		skillID, err := repo.ResolveSkillIDByCode(context.Background(), skillCode)
		if err != nil {
			_ = persistResult("failed", "resolve skill failed")
			zap.L().Error("translation command persistence failed: resolve skill failed",
				zap.String("skill_code", skillCode),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.Error(err),
			)
			return false
		}
		if skillID == uuid.Nil {
			_ = persistResult("failed", "skill not found")
			zap.L().Error("translation command persistence failed: skill not found",
				zap.String("skill_code", skillCode),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			)
			return false
		}

		// 先建立 service member，確保 locale 寫入前已具備服務成員關聯。
		if err := repo.AddServiceMemberToChannel(context.Background(), channelUUID, ownerUserID, skillID); err != nil {
			_ = persistResult("failed", "add service member failed")
			zap.L().Error("translation command persistence failed: add service member",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("line_user_id", senderUserID),
				zap.String("skill_code", skillCode),
				zap.Error(err),
			)
			return false
		}

		// locale 逐筆寫入：單筆失敗繼續其餘語系，避免整批因局部錯誤失敗。
		appliedLocales := make([]string, 0, len(targetLocales))
		for _, targetLocale := range targetLocales {
			if err := repo.AddTranslationLocaleToChannel(context.Background(), channelUUID, skillID, ownerUserID, targetLocale); err != nil {
				zap.L().Error("translation command persistence failed: add target locale",
					zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
					zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
					zap.String("line_user_id", senderUserID),
					zap.String("skill_code", skillCode),
					zap.String("target_locale", targetLocale),
					zap.Error(err),
				)
				continue
			}
			appliedLocales = append(appliedLocales, targetLocale)
		}

		// 全失敗視為錯誤，直接輸出 error。
		if len(appliedLocales) == 0 {
			_ = persistResult("failed", "all locale writes failed")
			zap.L().Error("translation command persistence failed: all locale writes failed",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("line_user_id", senderUserID),
				zap.String("skill_code", skillCode),
			)
			return false
		}

		if !persistResult("success", "applied_locales="+strings.Join(appliedLocales, ",")) {
			return false
		}

		// 只有至少一個 locale 寫入成功時才記錄 success。
		zap.L().Info("line translation command state persisted",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("persisted_message_id", persistedMessage.ID.String()),
			zap.String("line_user_id", senderUserID),
			zap.String("skill_code", skillCode),
			zap.String("api_operation", apiOperation),
			zap.Strings("target_locales", appliedLocales),
		)
		return true
	}
}

// NewRemoveTranslationCommandStateHandler 建立翻譯停用副作用的共用 handler。
//
// stop_translation_all：
// 1) 刪除發起者在目前 channel 的 translation locales
// 2) 刪除發起者在 channel_service_member 中的翻譯 skill 關聯
//
// stop_translation_locale：
// 1) 只刪除 action_params.target_locales 指定的 locales
// 2) 若該使用者已沒有剩餘 translation locales，再移除 service member
func NewRemoveTranslationCommandStateHandler(repo *repository.ChannelMessageRepo) Handler {
	return func(message *unifiedmessage.Message, senderUserID string, decision *llminteraction.ActionDecision, matchedSkillCode string) bool {
		if repo == nil || message == nil || decision == nil {
			zap.L().Error("translation command removal failed: missing required dependency",
				zap.Bool("repo_nil", repo == nil),
				zap.Bool("message_nil", message == nil),
				zap.Bool("decision_nil", decision == nil),
			)
			return false
		}

		apiOperation := strings.TrimSpace(decision.APIOperation)
		operationKey := strings.ToLower(apiOperation)
		// 同一個 handler 同時支援「整體關閉」與「指定語系關閉」，
		// 其他 operation 一律交回 dispatcher 視為未處理。
		if operationKey != translationStopAllOperation && operationKey != translationStopLocaleOperation {
			return false
		}

		// unified message 的 ChannelID 是平台外部識別；刪除資料前必須先解析成內部 channel UUID，
		// 才能精準命中 channel_service_member 與 translation_locale 的資料列。
		channel, err := repo.GetChannelByPlatformGroupID(
			context.Background(),
			strings.TrimSpace(message.Platform),
			strings.TrimSpace(message.ChannelID),
		)
		if err != nil || channel == nil || channel.ID == uuid.Nil {
			zap.L().Error("translation command removal failed: resolve internal channel failed",
				zap.String("platform", strings.TrimSpace(message.Platform)),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("channel_type", strings.TrimSpace(message.ChannelType)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.Error(err),
			)
			return false
		}
		channelUUID := channel.ID

		// 停用指令同樣寫 action_result，讓前後端能查到這次指令是 success、failed 或 missing_parameter。
		// 因此需要先用平台訊息 ID 找回已落庫的 channel_message。
		persistedMessage, err := repo.FindMessageByPlatformMessageID(context.Background(), channelUUID, strings.TrimSpace(message.PlatformMessageID))
		if err != nil {
			zap.L().Error("translation command removal failed: resolve persisted message for action result",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("api_operation", apiOperation),
				zap.Error(err),
			)
			return false
		}
		if persistedMessage == nil || persistedMessage.ID == uuid.Nil {
			zap.L().Error("translation command removal failed: persisted message not found for action result",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("api_operation", apiOperation),
			)
			return false
		}

		persistResult := func(status string, resultMessage string) bool {
			// action_results 的 key 由 api_operation + channel_message 決定；
			// 同一則停用指令重跑時會更新既有結果，而不是新增重複紀錄。
			if err := repo.UpsertActionResult(context.Background(), apiOperation, persistedMessage.ID, status, resultMessage); err != nil {
				zap.L().Error("translation command removal failed: upsert action result",
					zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
					zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
					zap.String("persisted_message_id", persistedMessage.ID.String()),
					zap.String("api_operation", apiOperation),
					zap.String("result_status", status),
					zap.Error(err),
				)
				return false
			}
			return true
		}

		// senderUserID 代表下指令的人；關閉翻譯必須以此人為 owner 範圍，
		// 不允許用 unknown 或空值模糊刪除資料。
		senderUserID = strings.TrimSpace(senderUserID)
		if senderUserID == "" || strings.EqualFold(senderUserID, "unknown") {
			_ = persistResult("failed", "missing sender user id")
			zap.L().Error("translation command removal failed: missing sender user id",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("sender_user_id", senderUserID),
			)
			return false
		}

		// 目前翻譯 command state 以 LINE 綁定解析 owner user id；
		// 若使用者尚未綁定，不能刪除任何 channel_member 或 locale。
		ownerUserID, err := repo.ResolveUserIDByLineUserID(context.Background(), senderUserID)
		if err != nil {
			_ = persistResult("failed", "resolve owner user failed")
			zap.L().Error("translation command removal failed: resolve owner user failed",
				zap.String("line_user_id", senderUserID),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.Error(err),
			)
			return false
		}
		if ownerUserID == uuid.Nil {
			_ = persistResult("failed", "line user not bound")
			zap.L().Error("translation command removal failed: line user not bound",
				zap.String("line_user_id", senderUserID),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			)
			return false
		}

		// matchedSkillCode 來自上游 action/skill 決策結果；停用時也要求明確 skill，
		// 避免用預設翻譯 skill 誤刪其他 skill 的 service member。
		skillCode := strings.TrimSpace(matchedSkillCode)
		if skillCode == "" {
			_ = persistResult("failed", "missing matched skill code")
			zap.L().Error("translation command removal failed: missing matched skill code",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("line_user_id", senderUserID),
			)
			return false
		}
		skillID, err := repo.ResolveSkillIDByCode(context.Background(), skillCode)
		if err != nil {
			_ = persistResult("failed", "resolve skill failed")
			zap.L().Error("translation command removal failed: resolve skill failed",
				zap.String("skill_code", skillCode),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.Error(err),
			)
			return false
		}
		if skillID == uuid.Nil {
			_ = persistResult("failed", "skill not found")
			zap.L().Error("translation command removal failed: skill not found",
				zap.String("skill_code", skillCode),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			)
			return false
		}

		targetLocales := ExtractTranslationTargetLocales(decision)
		// 指定語系關閉必須帶 target_locales；缺參時只記 action_result，
		// 不進行任何資料刪除，保留使用者現有翻譯設定。
		if operationKey == translationStopLocaleOperation && len(targetLocales) == 0 {
			_ = persistResult("missing_parameter", "target_locales is required")
			zap.L().Error("translation command removal failed: missing required locale params",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("api_operation", apiOperation),
			)
			return false
		}

		if operationKey == translationStopAllOperation {
			// 整體關閉不需要 locale filter；repository 會刪除此 owner 在該 channel 的全部翻譯語系。
			targetLocales = nil
		}
		removedLocales, err := repo.RemoveTranslationLocalesFromChannel(context.Background(), channelUUID, skillID, ownerUserID, targetLocales)
		if err != nil {
			_ = persistResult("failed", "remove translation locales failed")
			zap.L().Error("translation command removal failed: remove translation locales",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("line_user_id", senderUserID),
				zap.String("skill_code", skillCode),
				zap.Strings("target_locales", targetLocales),
				zap.Error(err),
			)
			return false
		}

		// 刪除指定語系後先查剩餘數量，避免使用者仍有其他翻譯語系時就把 service member 移掉。
		remainingLocales, err := repo.CountOwnerTranslationLocales(context.Background(), channelUUID, skillID, ownerUserID)
		if err != nil {
			_ = persistResult("failed", "count remaining translation locales failed")
			zap.L().Error("translation command removal failed: count remaining locales",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("line_user_id", senderUserID),
				zap.String("skill_code", skillCode),
				zap.Error(err),
			)
			return false
		}

		removedMembers := 0
		if operationKey == translationStopAllOperation || remainingLocales == 0 {
			// stop_translation_all 一律移除 service member；
			// stop_translation_locale 則只在該 owner 已無剩餘 locale 時移除。
			removedMembers, err = repo.RemoveServiceMemberFromChannel(context.Background(), channelUUID, ownerUserID, skillID)
			if err != nil {
				_ = persistResult("failed", "remove service member failed")
				zap.L().Error("translation command removal failed: remove service member",
					zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
					zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
					zap.String("line_user_id", senderUserID),
					zap.String("skill_code", skillCode),
					zap.Error(err),
				)
				return false
			}
		}

		// result_message 用簡單 key=value 格式保存，方便 command chain 之後回溯與除錯。
		resultParts := []string{
			"removed_locales=" + intString(removedLocales),
			"removed_service_members=" + intString(removedMembers),
		}
		if len(targetLocales) > 0 {
			resultParts = append(resultParts, "target_locales="+strings.Join(targetLocales, ","))
		}
		if !persistResult("success", strings.Join(resultParts, ";")) {
			return false
		}

		zap.L().Info("line translation command state removed",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("persisted_message_id", persistedMessage.ID.String()),
			zap.String("line_user_id", senderUserID),
			zap.String("skill_code", skillCode),
			zap.String("api_operation", apiOperation),
			zap.Strings("target_locales", targetLocales),
			zap.Int("removed_locales", removedLocales),
			zap.Int("removed_service_members", removedMembers),
		)
		return true
	}
}

// ExtractTranslationTargetLocales 從通用 action_params 擷取翻譯語系參數。
//
// 契約固定使用 target_locales（字串陣列），
// 單一語系也使用單元素陣列表示。
//
// 輸出會經過 dedupeLocales 做清理與去重。
func ExtractTranslationTargetLocales(decision *llminteraction.ActionDecision) []string {
	if decision == nil || len(decision.ActionParams) == 0 {
		return nil
	}
	raw, ok := decision.ActionParams[llminteraction.ActionParamTargetLocales]
	if !ok || len(raw) == 0 {
		return nil
	}
	var locales []string
	if err := json.Unmarshal(raw, &locales); err != nil {
		// 契約要求 target_locales 必須是陣列；非陣列一律視為無效。
		return nil
	}
	return dedupeLocales(locales)
}

// dedupeLocales 對 locale 清單做正規化去重。
//
// 規則：
// - 去除空白與空字串
// - 大小寫不敏感去重（en-US 與 en-us 視為同一值）
// - 保留第一個出現值的原始字面（維持可讀性與順序可預期）
func dedupeLocales(locales []string) []string {
	if len(locales) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(locales))
	out := make([]string, 0, len(locales))
	for _, locale := range locales {
		trimmed := strings.TrimSpace(locale)
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

func intString(value int) string {
	return strconv.Itoa(value)
}
