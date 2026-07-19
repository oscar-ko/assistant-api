package actionpost

import (
	"context"
	"strings"

	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"
	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// NewPersistTodoReminderCommandStateHandler 建立待辦提醒啟用副作用的共用 handler。
//
// start_todo_reminder 不需要 action_params：
// - 作用範圍固定為目前訊息所在 channel。
// - 啟用者固定為下指令的 bound user。
// - 寫入 channel_service_member 後，後續非指令文字才會通過 realtime text-scan gate。
func NewPersistTodoReminderCommandStateHandler(repo *repository.ChannelMessageRepo) Handler {
	return func(message *unifiedmessage.Message, senderUserID string, decision *llminteraction.ActionDecision, matchedSkillCode string) bool {
		return persistTodoReminderCommandState(repo, message, senderUserID, decision, matchedSkillCode, true)
	}
}

// NewRemoveTodoReminderCommandStateHandler 建立待辦提醒停用副作用的共用 handler。
//
// stop_todo_reminder 只移除下指令者在目前 channel 的待辦提醒 service member，
// 不影響同 channel 其他使用者已啟用的待辦提醒服務。
func NewRemoveTodoReminderCommandStateHandler(repo *repository.ChannelMessageRepo) Handler {
	return func(message *unifiedmessage.Message, senderUserID string, decision *llminteraction.ActionDecision, matchedSkillCode string) bool {
		return persistTodoReminderCommandState(repo, message, senderUserID, decision, matchedSkillCode, false)
	}
}

func persistTodoReminderCommandState(repo *repository.ChannelMessageRepo, message *unifiedmessage.Message, senderUserID string, decision *llminteraction.ActionDecision, matchedSkillCode string, enabled bool) bool {
	// 這個 handler 是 command action 成功決策後的「狀態落庫」層，
	// 不負責重新判斷自然語言意圖，也不從 action_params 讀取作用範圍。
	// 待辦提醒的啟停範圍固定是目前訊息所在 channel；使用者是否真的啟用服務，
	// 由 channel_service_member 這張表表示。
	if repo == nil || message == nil || decision == nil {
		zap.L().Error("todo reminder command persistence failed: missing required dependency",
			zap.Bool("repo_nil", repo == nil),
			zap.Bool("message_nil", message == nil),
			zap.Bool("decision_nil", decision == nil),
		)
		return false
	}

	// Dispatcher 已經依 api_operation 找到 handler；這裡仍做一次 operation 防線，
	// 避免未來重用 helper 時把 start/stop 的 side-effect 寫反。
	apiOperation := strings.TrimSpace(decision.APIOperation)
	operationKey := strings.ToLower(apiOperation)
	if enabled && operationKey != todoReminderStartOperation {
		return false
	}
	if !enabled && operationKey != todoReminderStopOperation {
		return false
	}

	ctx := context.Background()
	// unified message 的 ChannelID 是平台外部識別（例如 LINE group id 或 Slack channel id），
	// 不是資料庫 channel 主鍵；寫入 service member 前必須先解析成內部 channel UUID。
	// 這裡不建立新 channel，因為 channel 的生命週期應由綁定/初始化流程負責。
	channelNode, err := repo.GetChannelByPlatformGroupID(
		ctx,
		strings.TrimSpace(message.Platform),
		strings.TrimSpace(message.ChannelID),
	)
	if err != nil || channelNode == nil || channelNode.ID == uuid.Nil {
		zap.L().Error("todo reminder command persistence failed: resolve internal channel failed",
			zap.String("platform", strings.TrimSpace(message.Platform)),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.Error(err),
		)
		return false
	}
	channelUUID := channelNode.ID

	// action_result 需要掛在已落庫的 channel_message 上。
	// command flow 會先 persist inbound message，再進 action decision；若此處找不到訊息，
	// 代表上游流程或資料一致性出問題，不能只寫 service member 而留下不可追蹤的副作用。
	persistedMessage, err := repo.FindMessageByPlatformMessageID(ctx, channelUUID, strings.TrimSpace(message.PlatformMessageID))
	if err != nil {
		zap.L().Error("todo reminder command persistence failed: resolve persisted message for action result",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("api_operation", apiOperation),
			zap.Error(err),
		)
		return false
	}
	if persistedMessage == nil || persistedMessage.ID == uuid.Nil {
		zap.L().Error("todo reminder command persistence failed: persisted message not found for action result",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("api_operation", apiOperation),
		)
		return false
	}

	persistResult := func(status string, resultMessage string) bool {
		// action_results 是指令副作用的可查狀態：
		// - success: service member 已完成啟用或停用
		// - failed: 缺少綁定、skill、channel 等必要資料，未完成副作用
		// 用 Upsert 可讓同一則訊息重跑時更新結果，而不是產生重複紀錄。
		if err := repo.UpsertActionResult(ctx, apiOperation, persistedMessage.ID, status, resultMessage); err != nil {
			zap.L().Error("todo reminder command persistence failed: upsert action result",
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

	// senderUserID 是平台來源使用者 ID，用來解析「誰」啟用了這個 channel service。
	// 空值或 unknown 不能寫入，否則會讓 service member 失去 owner，後續停用也無法精準刪除。
	senderUserID = strings.TrimSpace(senderUserID)
	if senderUserID == "" || strings.EqualFold(senderUserID, "unknown") {
		_ = persistResult("failed", "missing sender user id")
		zap.L().Error("todo reminder command persistence failed: missing sender user id",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("sender_user_id", senderUserID),
		)
		return false
	}

	// 待辦提醒會同時支援 LINE / Slack，因此使用平台中立的綁定解析入口。
	// LINE 會走 line 綁定表；Slack 會用 tenant + user id 找 slack 綁定。
	// 若使用者尚未綁定，保守失敗，不建立無法歸屬的 service member。
	ownerUserID, err := repo.ResolveBoundUserIDByPlatformIdentity(ctx, message.Platform, message.PlatformTenantID, senderUserID)
	if err != nil {
		_ = persistResult("failed", "resolve owner user failed")
		zap.L().Error("todo reminder command persistence failed: resolve owner user failed",
			zap.String("platform", strings.TrimSpace(message.Platform)),
			zap.String("platform_tenant_id", strings.TrimSpace(message.PlatformTenantID)),
			zap.String("platform_user_id", senderUserID),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.Error(err),
		)
		return false
	}
	if ownerUserID == uuid.Nil {
		_ = persistResult("failed", "platform user not bound")
		zap.L().Error("todo reminder command persistence failed: platform user not bound",
			zap.String("platform", strings.TrimSpace(message.Platform)),
			zap.String("platform_tenant_id", strings.TrimSpace(message.PlatformTenantID)),
			zap.String("platform_user_id", senderUserID),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		)
		return false
	}

	// matchedSkillCode 來自上游 action/skill 決策結果。
	// 這裡不硬編 todo.reminder，避免 handler 與 seed/catalog 分裂；
	// 若未來 operation 被搬到其他 skill，錯誤會被明確記錄，而不是靜默寫錯關聯。
	skillCode := strings.TrimSpace(matchedSkillCode)
	if skillCode == "" {
		_ = persistResult("failed", "missing matched skill code")
		zap.L().Error("todo reminder command persistence failed: missing matched skill code",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("platform_user_id", senderUserID),
		)
		return false
	}
	skillID, err := repo.ResolveSkillIDByCode(ctx, skillCode)
	if err != nil {
		_ = persistResult("failed", "resolve skill failed")
		zap.L().Error("todo reminder command persistence failed: resolve skill failed",
			zap.String("skill_code", skillCode),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.Error(err),
		)
		return false
	}
	if skillID == uuid.Nil {
		_ = persistResult("failed", "skill not found")
		zap.L().Error("todo reminder command persistence failed: skill not found",
			zap.String("skill_code", skillCode),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		)
		return false
	}

	if enabled {
		// 啟用待辦提醒的唯一狀態變更：
		// 在目前 channel、目前 bound user、目前 skill 之間建立 service member 關聯。
		// 後續每則非指令訊息是否需要 classification，就是由這筆資料搭配 skill.requires_text_scan 判斷。
		if err := repo.AddServiceMemberToChannel(ctx, channelUUID, ownerUserID, skillID); err != nil {
			_ = persistResult("failed", "add service member failed")
			zap.L().Error("todo reminder command persistence failed: add service member",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("platform_user_id", senderUserID),
				zap.String("skill_code", skillCode),
				zap.Error(err),
			)
			return false
		}
		if !persistResult("success", "service_member=enabled") {
			return false
		}
		zap.L().Info("todo reminder command state persisted",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("persisted_message_id", persistedMessage.ID.String()),
			zap.String("platform_user_id", senderUserID),
			zap.String("skill_code", skillCode),
			zap.String("api_operation", apiOperation),
		)
		return true
	}

	// 停用只移除「下指令者」在「目前 channel」的待辦提醒關聯。
	// 若同一 channel 其他使用者也啟用了待辦提醒，channel 仍然需要保留 text-scan gate，
	// 因為 HasChannelRealtimeTextScanService 只要看到任一 service member 就會繼續掃描。
	removedMembers, err := repo.RemoveServiceMemberFromChannel(ctx, channelUUID, ownerUserID, skillID)
	if err != nil {
		_ = persistResult("failed", "remove service member failed")
		zap.L().Error("todo reminder command persistence failed: remove service member",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("platform_user_id", senderUserID),
			zap.String("skill_code", skillCode),
			zap.Error(err),
		)
		return false
	}
	if !persistResult("success", "removed_service_members="+intString(removedMembers)) {
		return false
	}
	zap.L().Info("todo reminder command state removed",
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("persisted_message_id", persistedMessage.ID.String()),
		zap.String("platform_user_id", senderUserID),
		zap.String("skill_code", skillCode),
		zap.String("api_operation", apiOperation),
		zap.Int("removed_service_members", removedMembers),
	)
	return true
}
