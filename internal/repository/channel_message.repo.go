package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"assistant-api/internal/ent"
	"assistant-api/internal/ent/action"
	"assistant-api/internal/ent/actionresult"
	"assistant-api/internal/ent/channel"
	"assistant-api/internal/ent/channelmessage"
	"assistant-api/internal/ent/channelservicemember"
	"assistant-api/internal/ent/line"
	"assistant-api/internal/ent/skill"
	"assistant-api/internal/ent/translationlocale"
	"assistant-api/internal/ent/user"

	"github.com/google/uuid"
)

// ChannelMessageRepo handles channel and inbound message persistence.
type ChannelMessageRepo struct {
	db *ent.Client
}

func NewChannelMessageRepo(db *ent.Client) *ChannelMessageRepo {
	return &ChannelMessageRepo{db: db}
}

// ResolveLineDisplayNameByLineUserID resolves sender display name from LINE binding table.
func (r *ChannelMessageRepo) ResolveLineDisplayNameByLineUserID(ctx context.Context, lineUserID string) (string, error) {
	if r == nil || r.db == nil {
		return "", fmt.Errorf("channel repository not initialized")
	}
	lineUserID = strings.TrimSpace(lineUserID)
	if lineUserID == "" {
		return "", nil
	}

	item, err := r.db.Line.Query().
		Where(line.LineUserIDEQ(lineUserID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("query line binding failed: %w", err)
	}
	if item.DisplayName == nil {
		return "", nil
	}
	return strings.TrimSpace(*item.DisplayName), nil
}

// ResolveUserIDByLineUserID 依 LINE 綁定表反查系統內部使用者 UUID。
//
// 這個方法保留給 LINE 專用流程使用，原因是 LINE 的顯示名稱與綁定資訊
// 仍然是從獨立的 line edge 讀取，而不是從 channel_service_members 反查。
// 當上層流程需要的是「LINE 綁定關係」而非「跨平台服務啟用關係」時，
// 就應該走這個入口，避免把兩種不同資料來源混在一起。
func (r *ChannelMessageRepo) ResolveUserIDByLineUserID(ctx context.Context, lineUserID string) (uuid.UUID, error) {
	if r == nil || r.db == nil {
		return uuid.Nil, fmt.Errorf("channel repository not initialized")
	}
	// line_user_id 先做 trim，避免因輸入含空白造成查詢 miss。
	lineUserID = strings.TrimSpace(lineUserID)
	if lineUserID == "" {
		// 空值視為「無法解析」，由呼叫端決定是否降級或略過。
		return uuid.Nil, nil
	}

	// 透過 user.HasLineWith(...) 反查綁定關係，
	// 可確保回傳的是系統內部 user 主鍵，而非平台外部識別。
	boundUser, err := r.db.User.Query().
		Where(user.HasLineWith(line.LineUserIDEQ(lineUserID))).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			// 尚未綁定時不回錯誤，維持流程可控（由上層決定是否提示綁定）。
			return uuid.Nil, nil
		}
		return uuid.Nil, fmt.Errorf("query user by line user id failed: %w", err)
	}
	if boundUser == nil {
		// 防禦式保護：理論上 Only 不會回 nil，但仍保留安全分支。
		return uuid.Nil, nil
	}
	return boundUser.ID, nil
}

// ResolveUserIDByPlatformUserID 依 channel_service_members 的 platform_user_id 反查系統內部使用者 UUID。
//
// 這是跨平台共用入口，適合 LINE、Slack 以及未來會共用同一張
// channel_service_members 表的 provider 使用。它和 ResolveUserIDByLineUserID
// 的差異在於：前者走 LINE 專屬綁定表，後者走跨平台服務成員表。
//
// 這個設計的目的，是讓即時服務（例如翻譯）只依賴一個平台中立的識別欄位，
// 不需要為每個 provider 各寫一套查詢路徑。
func (r *ChannelMessageRepo) ResolveUserIDByPlatformUserID(ctx context.Context, platformUserID string) (uuid.UUID, error) {
	if r == nil || r.db == nil {
		return uuid.Nil, fmt.Errorf("channel repository not initialized")
	}
	platformUserID = strings.TrimSpace(platformUserID)
	if platformUserID == "" {
		return uuid.Nil, nil
	}

	member, err := r.db.ChannelServiceMember.Query().
		Where(channelservicemember.PlatformUserIDEQ(platformUserID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return uuid.Nil, nil
		}
		return uuid.Nil, fmt.Errorf("query user by platform user id failed: %w", err)
	}
	if member == nil {
		return uuid.Nil, nil
	}
	return member.UserID, nil
}

// ResolveSkillIDByCode resolves skill UUID from skill_code.
func (r *ChannelMessageRepo) ResolveSkillIDByCode(ctx context.Context, skillCode string) (uuid.UUID, error) {
	if r == nil || r.db == nil {
		return uuid.Nil, fmt.Errorf("channel repository not initialized")
	}
	// skill_code 先正規化，避免上游帶空白導致查詢不到。
	skillCode = strings.TrimSpace(skillCode)
	if skillCode == "" {
		// 空 skill_code 不當作系統錯誤，交由上層決定 fallback。
		return uuid.Nil, nil
	}

	// skill_code 為業務語意上的穩定鍵，
	// 先解析到 skill.id 才能供 relation table（member/locale）使用。
	skillItem, err := r.db.Skill.Query().
		Where(skill.SkillCodeEQ(skillCode)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			// 查無技能時回 uuid.Nil，讓上層可記錄告警並略過 side-effect。
			return uuid.Nil, nil
		}
		return uuid.Nil, fmt.Errorf("query skill by code failed: %w", err)
	}
	if skillItem == nil {
		// 防禦式分支：避免意外 nil 導致後續 panic。
		return uuid.Nil, nil
	}
	return skillItem.ID, nil
}

// GetOrCreateChannel 依 (platform, group_id) 取得 channel；不存在則建立。
//
// 使用時機：
// - 綁定流程初始化 private channel
// - 系統明確允許建立新 channel 的管理流程
//
// 禁止使用時機：
// - 入站 webhook/message persist 主流程（避免未綁定來源自動落地）
func (r *ChannelMessageRepo) GetOrCreateChannel(
	ctx context.Context,
	platform string,
	groupID string,
	channelType string,
) (*ent.Channel, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("channel repository not initialized")
	}

	platformValue := channel.Platform(strings.ToLower(strings.TrimSpace(platform)))
	switch platformValue {
	case channel.PlatformLine, channel.PlatformWhatsapp, channel.PlatformSlack, channel.PlatformTelegram:
	default:
		return nil, fmt.Errorf("invalid channel platform: %s", platform)
	}

	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return nil, fmt.Errorf("group id is required")
	}

	// channel type 缺值時預設 group，避免寫入非法 enum。
	typeValue := channel.Type(strings.ToLower(strings.TrimSpace(channelType)))
	switch typeValue {
	case channel.TypeGroup, channel.TypePrivate:
	default:
		typeValue = channel.TypeGroup
	}

	ch, err := r.db.Channel.Query().
		Where(channel.PlatformEQ(platformValue), channel.GroupIDEQ(groupID)).
		Only(ctx)
	if err == nil {
		// 既有 channel 若型別與當前期望不同，嘗試對齊更新；
		// 更新失敗不阻斷流程，回傳既有資料由上層決定是否重試。
		if ch.Type != typeValue {
			updated, updateErr := r.db.Channel.UpdateOneID(ch.ID).SetType(typeValue).Save(ctx)
			if updateErr == nil {
				ch = updated
			}
		}
		return ch, nil
	}
	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("query channel failed: %w", err)
	}

	return r.db.Channel.Create().
		SetName(platformValue.String() + " Group: " + groupID).
		SetPlatform(platformValue).
		SetGroupID(groupID).
		SetType(typeValue).
		Save(ctx)
}

// GetChannelByPlatformGroupID 只查詢 (platform, group_id) 對應 channel。
//
// 與 GetOrCreateChannel 的差異：
// - 這個方法只查詢，不會自動建立 channel。
// - 找不到時回傳 nil, nil，讓上層決定是否中止流程。
func (r *ChannelMessageRepo) GetChannelByPlatformGroupID(
	ctx context.Context,
	platform string,
	groupID string,
) (*ent.Channel, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("channel repository not initialized")
	}

	platformValue := channel.Platform(strings.ToLower(strings.TrimSpace(platform)))
	switch platformValue {
	case channel.PlatformLine, channel.PlatformWhatsapp, channel.PlatformSlack, channel.PlatformTelegram:
	default:
		return nil, fmt.Errorf("invalid channel platform: %s", platform)
	}

	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return nil, fmt.Errorf("group id is required")
	}

	item, err := r.db.Channel.Query().
		Where(channel.PlatformEQ(platformValue), channel.GroupIDEQ(groupID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("query channel failed: %w", err)
	}
	return item, nil
}

// SaveReceivedMessage stores an incoming channel message.
func (r *ChannelMessageRepo) SaveReceivedMessage(
	ctx context.Context,
	channelID uuid.UUID,
	senderID string,
	senderName string,
	platformMessageID string,
	replyToMsgID string,
	content string,
	messageType string,
	platformTimestamp int64,
) (*ent.ChannelMessage, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("channel repository not initialized")
	}
	if channelID == uuid.Nil {
		return nil, fmt.Errorf("channel id is required")
	}

	senderID = strings.TrimSpace(senderID)
	if senderID == "" {
		senderID = "unknown"
	}
	messageType = strings.TrimSpace(messageType)
	if messageType == "" {
		messageType = "text"
	}
	content = strings.TrimSpace(content)
	if content == "" {
		content = "[" + messageType + "]"
	}

	builder := r.db.ChannelMessage.Create().
		SetChannelID(channelID).
		SetSenderID(senderID).
		SetMessageType(messageType).
		SetContent(content)
	if value := strings.TrimSpace(senderName); value != "" {
		builder = builder.SetSenderName(value)
	}
	if value := strings.TrimSpace(platformMessageID); value != "" {
		builder = builder.SetPlatformMessageID(value)
	}
	if value := strings.TrimSpace(replyToMsgID); value != "" {
		builder = builder.SetReplyToMsgID(value)
	}
	if platformTimestamp > 0 {
		builder = builder.SetPlatformTimestamp(platformTimestamp)
	}

	item, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("save received message failed: %w", err)
	}
	return item, nil
}

// SaveSentMessage stores an outbound channel message generated by the assistant.
func (r *ChannelMessageRepo) SaveSentMessage(
	ctx context.Context,
	channelID uuid.UUID,
	senderID string,
	senderName string,
	platformMessageID string,
	content string,
	messageType string,
	platformTimestamp int64,
	relatedMessageID uuid.UUID,
) (*ent.ChannelMessage, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("channel repository not initialized")
	}
	if channelID == uuid.Nil {
		return nil, fmt.Errorf("channel id is required")
	}

	senderID = strings.TrimSpace(senderID)
	if senderID == "" {
		senderID = "bot"
	}
	messageType = strings.TrimSpace(messageType)
	if messageType == "" {
		messageType = "text"
	}
	content = strings.TrimSpace(content)
	if content == "" {
		content = "[" + messageType + "]"
	}

	builder := r.db.ChannelMessage.Create().
		SetChannelID(channelID).
		SetSenderID(senderID).
		SetMessageType(messageType).
		SetContent(content)
	if value := strings.TrimSpace(senderName); value != "" {
		builder = builder.SetSenderName(value)
	}
	if value := strings.TrimSpace(platformMessageID); value != "" {
		builder = builder.SetPlatformMessageID(value)
	}
	if platformTimestamp > 0 {
		builder = builder.SetPlatformTimestamp(platformTimestamp)
	}
	if relatedMessageID != uuid.Nil {
		builder = builder.SetRelatedMessageID(relatedMessageID)
	}

	item, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("save sent message failed: %w", err)
	}
	return item, nil
}

// GetMessageByID returns a channel message by UUID.
func (r *ChannelMessageRepo) GetMessageByID(ctx context.Context, id uuid.UUID) (*ent.ChannelMessage, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("channel repository not initialized")
	}
	// 呼叫端以 nil 表示「無可追溯父訊息」，這裡統一回傳 nil, nil。
	if id == uuid.Nil {
		return nil, nil
	}

	item, err := r.db.ChannelMessage.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get message by id failed: %w", err)
	}
	return item, nil
}

// FindMessageByPlatformMessageID returns the latest message in a channel with the given platform message id.
func (r *ChannelMessageRepo) FindMessageByPlatformMessageID(ctx context.Context, channelID uuid.UUID, platformMessageID string) (*ent.ChannelMessage, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("channel repository not initialized")
	}
	// channelID 為查詢邊界，避免不同群組/私聊訊息誤關聯。
	if channelID == uuid.Nil {
		return nil, nil
	}
	platformMessageID = strings.TrimSpace(platformMessageID)
	if platformMessageID == "" {
		return nil, nil
	}

	// 使用 channel_id + platform_message_id 作為查詢條件，
	// 讓平台層回覆 ID 只在同一頻道內解析父訊息。
	item, err := r.db.ChannelMessage.Query().
		Where(
			channelmessage.ChannelIDEQ(channelID),
			channelmessage.PlatformMessageIDEQ(platformMessageID),
		).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("query message by platform message id failed: %w", err)
	}
	return item, nil
}

// ResolveParentMessage 解析單則訊息的父節點。
// 優先序設計：
// 1) related_message_id：系統內部已建立的關聯，準確度最高。
// 2) reply_to_msg_id：平台層回覆 ID，在同 channel 內做 fallback 查詢。
//
// 這個方法的目標是把「父訊息解析策略」集中管理，
// 避免上層流程在多處重複寫 related/reply 的分支邏輯。
func (r *ChannelMessageRepo) ResolveParentMessage(ctx context.Context, message *ent.ChannelMessage) (*ent.ChannelMessage, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("channel repository not initialized")
	}
	if message == nil {
		return nil, nil
	}

	// 先走內部關聯：當 related_message_id 存在時，代表已完成資料映射。
	if message.RelatedMessageID != nil && *message.RelatedMessageID != uuid.Nil {
		return r.GetMessageByID(ctx, *message.RelatedMessageID)
	}
	// 若尚未建立 related 關聯，再退回平台回覆 ID 查找父訊息。
	if replyToMsgID := strings.TrimSpace(message.ReplyToMsgID); replyToMsgID != "" {
		return r.FindMessageByPlatformMessageID(ctx, message.ChannelID, replyToMsgID)
	}
	return nil, nil
}

// FindLatestActionOperationByMessageID 取得某則訊息最新的 action api_operation。
//
// 為什麼取「最新」：
// - 同一 message 在極端情況下可能被重試/覆寫 action_results。
// - 以 updated_at 由新到舊排序，能讓上層拿到最終狀態對應的 operation。
//
// 回傳空字串代表「目前沒有可重用的既有指令」，不視為錯誤。
func (r *ChannelMessageRepo) FindLatestActionOperationByMessageID(ctx context.Context, messageID uuid.UUID) (string, error) {
	if r == nil || r.db == nil {
		return "", fmt.Errorf("channel repository not initialized")
	}
	if messageID == uuid.Nil {
		return "", nil
	}

	item, err := r.db.ActionResult.Query().
		Where(actionresult.ChannelMessageIDEQ(messageID)).
		// 以 updated_at 排序，確保回傳的是最新一筆結果。
		Order(ent.Desc(actionresult.FieldUpdatedAt)).
		WithAction().
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("query action result by message id failed: %w", err)
	}
	// action edge 未載入或不存在時，視為沒有可用 operation。
	if item == nil || item.Edges.Action == nil {
		return "", nil
	}
	return strings.TrimSpace(item.Edges.Action.APIOperation), nil
}

// FindLatestActionResultByMessageID 取得某則訊息最新的 action_result 詳細上下文。
//
// 回傳值包含：
// - action (api_operation)
// - status (success/missing_parameter/failed)
// - result_message（例如 missing_parameters、reason）
//
// 若查無資料則回傳 nil, nil，讓呼叫端自行決定 fallback 策略。
func (r *ChannelMessageRepo) FindLatestActionResultByMessageID(ctx context.Context, messageID uuid.UUID) (*ent.ActionResult, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("channel repository not initialized")
	}
	if messageID == uuid.Nil {
		return nil, nil
	}

	item, err := r.db.ActionResult.Query().
		Where(actionresult.ChannelMessageIDEQ(messageID)).
		Order(ent.Desc(actionresult.FieldUpdatedAt)).
		WithAction().
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("query action result by message id failed: %w", err)
	}
	return item, nil
}

// ResolveSkillCodeByAPIOperation 由 api_operation 反查 skill_code。
//
// 用途：當流程直接重用既有指令（略過重新解析）時，
// 仍需要 skill_code 給 post-action handler 做嚴格校驗與落庫關聯。
func (r *ChannelMessageRepo) ResolveSkillCodeByAPIOperation(ctx context.Context, apiOperation string) (string, error) {
	if r == nil || r.db == nil {
		return "", fmt.Errorf("channel repository not initialized")
	}
	operation := strings.TrimSpace(apiOperation)
	if operation == "" {
		return "", nil
	}

	actionItem, err := r.db.Action.Query().
		Where(action.APIOperationEQ(operation)).
		WithSkill().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("query action by api operation failed: %w", err)
	}
	// 查無 skill 關聯時回空值，讓上層可選擇保守降級而非直接 panic。
	if actionItem == nil || actionItem.Edges.Skill == nil {
		return "", nil
	}
	return strings.TrimSpace(actionItem.Edges.Skill.SkillCode), nil
}

// ResolveActionPromptByAPIOperation 由 api_operation 反查 action.command_purpose。
//
// 用途：
// - 在指令鍊補參數模式下，把 seed 動態規則重新帶回 LLM prompt。
// - 避免鏈路固定 operation 時遺失 operation 專屬指引。
//
// 回傳空字串代表查無 prompt 或未配置，不視為錯誤。
func (r *ChannelMessageRepo) ResolveActionPromptByAPIOperation(ctx context.Context, apiOperation string) (string, error) {
	if r == nil || r.db == nil {
		return "", fmt.Errorf("channel repository not initialized")
	}
	operation := strings.TrimSpace(apiOperation)
	if operation == "" {
		return "", nil
	}

	actionItem, err := r.db.Action.Query().
		Where(action.APIOperationEQ(operation)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("query action by api operation failed: %w", err)
	}
	if actionItem == nil || actionItem.CommandPurpose == nil {
		return "", nil
	}
	return strings.TrimSpace(*actionItem.CommandPurpose), nil
}

// LinkRelatedMessageByReply links related_message_id from reply_to_msg_id when target message exists.
func (r *ChannelMessageRepo) LinkRelatedMessageByReply(ctx context.Context, message *ent.ChannelMessage) (*ent.ChannelMessage, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("channel repository not initialized")
	}
	// message 為 nil 代表上游未成功落庫，這裡不再進一步處理。
	if message == nil {
		return nil, nil
	}
	// 已有 related_message_id 時不重算，避免覆蓋既有人工或上游關聯。
	if message.RelatedMessageID != nil && *message.RelatedMessageID != uuid.Nil {
		return message, nil
	}

	replyToMsgID := strings.TrimSpace(message.ReplyToMsgID)
	// 沒有平台回覆目標時，保持原樣回傳。
	if replyToMsgID == "" {
		return message, nil
	}

	target, err := r.FindMessageByPlatformMessageID(ctx, message.ChannelID, replyToMsgID)
	if err != nil {
		return nil, err
	}
	// 找不到父訊息，或父子同一筆（異常資料）時，不建立關聯。
	if target == nil || target.ID == uuid.Nil || target.ID == message.ID {
		return message, nil
	}

	// 這一步把平台回覆關係映射成資料庫可遞迴追溯的 related_message_id。
	updated, err := r.db.ChannelMessage.UpdateOneID(message.ID).SetRelatedMessageID(target.ID).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("link related message failed: %w", err)
	}
	return updated, nil
}

// UpsertActionResult records command execution result for action-message pair.
func (r *ChannelMessageRepo) UpsertActionResult(ctx context.Context, apiOperation string, messageID uuid.UUID, status string, resultMessage string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("channel repository not initialized")
	}
	operation := strings.TrimSpace(apiOperation)
	if operation == "" {
		return fmt.Errorf("api operation is required")
	}
	if messageID == uuid.Nil {
		return fmt.Errorf("message id is required")
	}
	status = strings.ToLower(strings.TrimSpace(status))
	resultMessage = strings.TrimSpace(resultMessage)
	var statusValue actionresult.Status
	switch status {
	case string(actionresult.StatusSuccess):
		statusValue = actionresult.StatusSuccess
	case string(actionresult.StatusMissingParameter):
		statusValue = actionresult.StatusMissingParameter
	case string(actionresult.StatusFailed):
		statusValue = actionresult.StatusFailed
	default:
		return fmt.Errorf("invalid action result status: %s", status)
	}

	actionItem, err := r.db.Action.Query().Where(action.APIOperationEQ(operation)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("action not found by api operation: %s", operation)
		}
		return fmt.Errorf("query action by api operation failed: %w", err)
	}

	item, err := r.db.ActionResult.Query().
		Where(
			actionresult.ActionIDEQ(actionItem.ID),
			actionresult.ChannelMessageIDEQ(messageID),
		).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("query action result failed: %w", err)
	}

	if item != nil {
		builder := r.db.ActionResult.UpdateOneID(item.ID).SetStatus(statusValue)
		if resultMessage != "" {
			builder = builder.SetResultMessage(resultMessage)
		} else {
			builder = builder.ClearResultMessage()
		}
		if _, err := builder.Save(ctx); err != nil {
			return fmt.Errorf("update action result failed: %w", err)
		}
		return nil
	}

	builder := r.db.ActionResult.Create().
		SetActionID(actionItem.ID).
		SetChannelMessageID(messageID).
		SetStatus(statusValue)
	if resultMessage != "" {
		builder = builder.SetResultMessage(resultMessage)
	}
	if _, err := builder.Save(ctx); err != nil {
		return fmt.Errorf("create action result failed: %w", err)
	}
	return nil
}

// AddServiceMemberToChannel adds a user into channel_service_members.
// The operation is idempotent and ignored if (channel_id, user_id, skill_id) already exists.
//
// 注意：這個寫入點目前只建立 channel / user / skill 的關聯，並不會同步填入
// platform_user_id。也就是說，若上層流程要依 platform_user_id 做共用查詢，
// 仍需確認該欄位是否已由其他寫入流程補齊，否則無法直接拿這個方法當作
// 跨平台識別資料的唯一來源。
func (r *ChannelMessageRepo) AddServiceMemberToChannel(ctx context.Context, channelID uuid.UUID, ownerID uuid.UUID, skillID uuid.UUID) error {
	if channelID == uuid.Nil {
		return fmt.Errorf("channel id is required")
	}
	if ownerID == uuid.Nil {
		return fmt.Errorf("owner id is required")
	}
	if skillID == uuid.Nil {
		return fmt.Errorf("skill id is required")
	}

	exists, err := r.db.ChannelServiceMember.Query().
		Where(
			channelservicemember.ChannelIDEQ(channelID),
			channelservicemember.UserIDEQ(ownerID),
			channelservicemember.SkillIDEQ(skillID),
		).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("failed to query service member: %w", err)
	}
	if exists {
		return nil
	}

	if _, err := r.db.ChannelServiceMember.Create().
		SetChannelID(channelID).
		SetUserID(ownerID).
		SetSkillID(skillID).
		Save(ctx); err != nil {
		return fmt.Errorf("failed to add service member to channel: %w", err)
	}
	return nil
}

// HasChannelServiceMember 回傳某使用者是否在某頻道啟用了指定技能。
//
// 用途：
// - 即時服務（例如翻譯）在執行前做精確 gating
// - 避免「同頻道中未啟用服務的成員」被誤套用 side-effect
//
// 參數要求：
// - channelID / ownerID / skillID 皆不可為 uuid.Nil
//
// 回傳語意：
// - (true, nil): 存在啟用記錄
// - (false, nil): 不存在啟用記錄（非錯誤）
// - (false, err): 查詢或初始化失敗
func (r *ChannelMessageRepo) HasChannelServiceMember(ctx context.Context, channelID uuid.UUID, ownerID uuid.UUID, skillID uuid.UUID) (bool, error) {
	if r == nil || r.db == nil {
		return false, fmt.Errorf("channel repository not initialized")
	}
	if channelID == uuid.Nil {
		return false, fmt.Errorf("channel id is required")
	}
	if ownerID == uuid.Nil {
		return false, fmt.Errorf("owner id is required")
	}
	if skillID == uuid.Nil {
		return false, fmt.Errorf("skill id is required")
	}

	exists, err := r.db.ChannelServiceMember.Query().
		Where(
			channelservicemember.ChannelIDEQ(channelID),
			channelservicemember.UserIDEQ(ownerID),
			channelservicemember.SkillIDEQ(skillID),
		).
		Exist(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to query service member: %w", err)
	}
	return exists, nil
}

// AddTranslationLocaleToChannel records a translation target locale with owner under a channel and skill.
// The operation is idempotent and ignored if (channel_id, target_locale) already exists.
func (r *ChannelMessageRepo) AddTranslationLocaleToChannel(ctx context.Context, channelID uuid.UUID, skillID uuid.UUID, ownerUserID uuid.UUID, targetLocale string) error {
	if channelID == uuid.Nil {
		return fmt.Errorf("channel id is required")
	}
	if skillID == uuid.Nil {
		return fmt.Errorf("skill id is required")
	}
	if ownerUserID == uuid.Nil {
		return fmt.Errorf("owner user id is required")
	}
	targetLocale = strings.TrimSpace(targetLocale)
	if targetLocale == "" {
		return fmt.Errorf("target locale is required")
	}

	exists, err := r.db.TranslationLocale.Query().
		Where(
			translationlocale.ChannelIDEQ(channelID),
			translationlocale.TargetLocaleEQ(targetLocale),
		).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("failed to query translation locale: %w", err)
	}
	if exists {
		return nil
	}

	if _, err := r.db.TranslationLocale.Create().
		SetChannelID(channelID).
		SetSkillID(skillID).
		SetOwnerUserID(ownerUserID).
		SetTargetLocale(targetLocale).
		Save(ctx); err != nil {
		return fmt.Errorf("failed to add translation locale: %w", err)
	}

	return nil
}

// ListChannelSkillTargetLocales returns configured translation target locales by channel and skill.
func (r *ChannelMessageRepo) ListChannelSkillTargetLocales(ctx context.Context, channelID uuid.UUID, skillID uuid.UUID) ([]string, error) {
	if channelID == uuid.Nil {
		return nil, fmt.Errorf("channel id is required")
	}
	if skillID == uuid.Nil {
		return nil, fmt.Errorf("skill id is required")
	}

	rows, err := r.db.TranslationLocale.Query().
		Where(
			translationlocale.ChannelIDEQ(channelID),
			translationlocale.SkillIDEQ(skillID),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query translation locales: %w", err)
	}

	seen := make(map[string]struct{}, len(rows))
	locales := make([]string, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		locale := strings.TrimSpace(row.TargetLocale)
		if locale == "" {
			continue
		}
		if _, ok := seen[locale]; ok {
			continue
		}
		seen[locale] = struct{}{}
		locales = append(locales, locale)
	}

	sort.Strings(locales)
	return locales, nil
}
