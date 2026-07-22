package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"assistant-api/internal/ent"
	"assistant-api/internal/ent/action"
	"assistant-api/internal/ent/actionresult"
	"assistant-api/internal/ent/channel"
	"assistant-api/internal/ent/channelmessage"
	"assistant-api/internal/ent/channelmessagemention"
	"assistant-api/internal/ent/channelservicemember"
	"assistant-api/internal/ent/line"
	"assistant-api/internal/ent/predicate"
	"assistant-api/internal/ent/skill"
	"assistant-api/internal/ent/slack"
	"assistant-api/internal/ent/todo"
	"assistant-api/internal/ent/todocandidate"
	"assistant-api/internal/ent/todocandidateassignee"
	"assistant-api/internal/ent/todocandidateevidencemessage"
	"assistant-api/internal/ent/todoevent"
	"assistant-api/internal/ent/todoupdatecandidate"
	"assistant-api/internal/ent/translationlocale"
	"assistant-api/internal/ent/user"
	"assistant-api/internal/integration/unifiedmessage"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ChannelMessageRepo handles channel and inbound message persistence.
type ChannelMessageRepo struct {
	db *ent.Client
}

// SaveTodoCandidateInput 是 Todo Reminder structured analyzer 落庫的最小輸入契約。
// repository 不重新判斷語意，只負責把 usecase 已驗證過的 decision/status 寫成可追蹤資料。
type SaveTodoCandidateInput struct {
	ChannelID       uuid.UUID
	MessageID       uuid.UUID
	LinkedMessageID uuid.UUID
	Decision        string
	Summary         string
	Assignees       []string
	DueText         string
	DueAt           *time.Time
	DueTimezone     string
	DuePrecision    string
	DueDecision     string
	DueConfidence   float64
	DueReason       string
	MissingFields   []string
	Confidence      float64
	Reason          string
}

func NewChannelMessageRepo(db *ent.Client) *ChannelMessageRepo {
	return &ChannelMessageRepo{db: db}
}

// SaveChannelMessageMentions 保存訊息中的 structured mention 清單。
// mention 是訊息層事實：@Jarvis、@Amy、多人 mention 都會保存；Todo assignee resolver 之後再決定哪些 mention 可成為待辦 owner。
func (r *ChannelMessageRepo) SaveChannelMessageMentions(ctx context.Context, channelMessageID uuid.UUID, platform string, platformTenantID string, mentions []unifiedmessage.Mention) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("channel repository not initialized")
	}
	if channelMessageID == uuid.Nil {
		return fmt.Errorf("channel message id is required")
	}
	if len(mentions) == 0 {
		return nil
	}

	platformValue, err := normalizeMentionPlatform(platform)
	if err != nil {
		return err
	}
	creates := make([]*ent.ChannelMessageMentionCreate, 0, len(mentions))
	for _, mention := range mentions {
		platformUserID := strings.TrimSpace(mention.UserID)
		mentionType := strings.TrimSpace(mention.Type)
		if mentionType == "" {
			mentionType = "user"
		}
		if platformUserID == "" && mentionType == "user" {
			continue
		}

		identityKind := normalizeMentionIdentityKind(mention.IdentityKind, mention.IsBot)
		resolvedUserID := uuid.Nil
		if platformUserID != "" {
			// 這裡只做平台 ID -> internal user 的 deterministic binding 查詢。
			// 找不到綁定時仍保存 mention 事實，讓使用者後續補綁定後可重新解析。
			resolved, resolveErr := r.ResolveBoundUserIDByPlatformIdentity(ctx, platform, platformTenantID, platformUserID)
			if resolveErr != nil {
				return fmt.Errorf("resolve mention user id failed: %w", resolveErr)
			}
			resolvedUserID = resolved
		}

		resolutionStatus := channelmessagemention.ResolutionStatusUnresolved
		if resolvedUserID != uuid.Nil {
			resolutionStatus = channelmessagemention.ResolutionStatusResolved
		} else if identityKind == channelmessagemention.IdentityKindUnknown || !strings.EqualFold(mentionType, "user") {
			resolutionStatus = channelmessagemention.ResolutionStatusUnsupported
		}

		create := r.db.ChannelMessageMention.Create().
			SetChannelMessageID(channelMessageID).
			SetPlatform(platformValue).
			SetMentionType(mentionType).
			SetIdentityKind(identityKind).
			SetIsBot(mention.IsBot || identityKind == channelmessagemention.IdentityKindBot).
			SetResolutionStatus(resolutionStatus)
		if platformUserID != "" {
			create.SetPlatformUserID(platformUserID)
		}
		if resolvedUserID != uuid.Nil {
			create.SetUserID(resolvedUserID)
		}
		if displayText := strings.TrimSpace(mention.DisplayText); displayText != "" {
			create.SetDisplayText(displayText)
		}
		if mention.Index != nil && *mention.Index >= 0 {
			create.SetMentionIndex(*mention.Index)
		}
		if mention.Length != nil && *mention.Length > 0 {
			create.SetMentionLength(*mention.Length)
		}
		if raw := strings.TrimSpace(mention.Raw); raw != "" {
			create.SetRaw(raw)
		}
		creates = append(creates, create)
	}
	if len(creates) == 0 {
		return nil
	}
	if err := r.db.ChannelMessageMention.CreateBulk(creates...).Exec(ctx); err != nil {
		return fmt.Errorf("save channel message mentions failed: %w", err)
	}
	return nil
}

func normalizeMentionPlatform(value string) (channelmessagemention.Platform, error) {
	switch channelmessagemention.Platform(strings.ToLower(strings.TrimSpace(value))) {
	case channelmessagemention.PlatformLine:
		return channelmessagemention.PlatformLine, nil
	case channelmessagemention.PlatformWhatsapp:
		return channelmessagemention.PlatformWhatsapp, nil
	case channelmessagemention.PlatformSlack:
		return channelmessagemention.PlatformSlack, nil
	case channelmessagemention.PlatformTelegram:
		return channelmessagemention.PlatformTelegram, nil
	default:
		return "", fmt.Errorf("invalid mention platform: %s", value)
	}
}

func normalizeMentionIdentityKind(value string, isBot bool) channelmessagemention.IdentityKind {
	if isBot {
		return channelmessagemention.IdentityKindBot
	}
	switch channelmessagemention.IdentityKind(strings.ToLower(strings.TrimSpace(value))) {
	case channelmessagemention.IdentityKindBot:
		return channelmessagemention.IdentityKindBot
	case channelmessagemention.IdentityKindUnknown:
		return channelmessagemention.IdentityKindUnknown
	default:
		return channelmessagemention.IdentityKindUser
	}
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
		Where(line.PlatformUserIDEQ(lineUserID)).
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
		Where(user.HasLineWith(line.PlatformUserIDEQ(lineUserID))).
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

// ResolveBoundUserIDByPlatformIdentity resolves a platform account binding to the system user UUID.
func (r *ChannelMessageRepo) ResolveBoundUserIDByPlatformIdentity(ctx context.Context, platform string, platformTenantID string, platformUserID string) (uuid.UUID, error) {
	if r == nil || r.db == nil {
		return uuid.Nil, fmt.Errorf("channel repository not initialized")
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	platformTenantID = strings.TrimSpace(platformTenantID)
	platformUserID = strings.TrimSpace(platformUserID)
	if platformUserID == "" {
		return uuid.Nil, nil
	}

	switch channel.Platform(platform) {
	case channel.PlatformLine:
		return r.ResolveUserIDByLineUserID(ctx, platformUserID)
	case channel.PlatformSlack:
		if platformTenantID == "" {
			return uuid.Nil, nil
		}
		boundUser, err := r.db.User.Query().
			Where(user.HasSlackWith(slack.PlatformTeamIDEQ(platformTenantID), slack.PlatformUserIDEQ(platformUserID))).
			Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return uuid.Nil, nil
			}
			return uuid.Nil, fmt.Errorf("query user by slack identity failed: %w", err)
		}
		if boundUser == nil {
			return uuid.Nil, nil
		}
		return boundUser.ID, nil
	default:
		return uuid.Nil, nil
	}
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
	channelName string,
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

	channelName = strings.TrimSpace(channelName)
	if channelName == "" {
		return nil, fmt.Errorf("channel name is required")
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
		// 命名規則變更時，既有資料需同步對齊，避免名稱長期停留舊值。
		if ch.Type != typeValue || strings.TrimSpace(ch.Name) != channelName {
			updated, updateErr := r.db.Channel.UpdateOneID(ch.ID).
				SetType(typeValue).
				SetName(channelName).
				Save(ctx)
			if updateErr != nil {
				return nil, fmt.Errorf("update channel failed: %w", updateErr)
			}
			ch = updated
		}
		return ch, nil
	}
	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("query channel failed: %w", err)
	}

	return r.db.Channel.Create().
		SetName(channelName).
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

// SetChannelActiveByPlatformGroupID updates channel active flag by (platform, group_id).
//
// 嚴格模式：
// - platform/group_id 必填
// - 若 channel 不存在，直接回錯（不做隱式建立）
func (r *ChannelMessageRepo) SetChannelActiveByPlatformGroupID(
	ctx context.Context,
	platform string,
	groupID string,
	isActive bool,
) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("channel repository not initialized")
	}

	platformValue := channel.Platform(strings.ToLower(strings.TrimSpace(platform)))
	switch platformValue {
	case channel.PlatformLine, channel.PlatformWhatsapp, channel.PlatformSlack, channel.PlatformTelegram:
	default:
		return fmt.Errorf("invalid channel platform: %s", platform)
	}

	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return fmt.Errorf("group id is required")
	}

	item, err := r.db.Channel.Query().
		Where(channel.PlatformEQ(platformValue), channel.GroupIDEQ(groupID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("channel not found")
		}
		return fmt.Errorf("query channel failed: %w", err)
	}

	update := r.db.Channel.UpdateOneID(item.ID).SetIsActive(isActive)
	if _, err := update.Save(ctx); err != nil {
		return fmt.Errorf("update channel active flag failed: %w", err)
	}
	return nil
}

// UpdateChannelDisplayNameByID updates channel.name by internal channel UUID.
//
// 嚴格模式：
// - channel id 必填
// - channel name 必填且會 trim
func (r *ChannelMessageRepo) UpdateChannelDisplayNameByID(ctx context.Context, channelID uuid.UUID, channelName string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("channel repository not initialized")
	}
	if channelID == uuid.Nil {
		return fmt.Errorf("channel id is required")
	}
	channelName = strings.TrimSpace(channelName)
	if channelName == "" {
		return fmt.Errorf("channel name is required")
	}

	_, err := r.db.Channel.UpdateOneID(channelID).SetName(channelName).Save(ctx)
	if err != nil {
		return fmt.Errorf("update channel display name failed: %w", err)
	}
	return nil
}

// SaveReceivedMessage stores an incoming channel message.
func (r *ChannelMessageRepo) SaveReceivedMessage(
	ctx context.Context,
	channelID uuid.UUID,
	platform string,
	platformTenantID string,
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
	if value := strings.TrimSpace(platformTenantID); value != "" {
		builder = builder.SetPlatformTenantID(value)
	}
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
	if ownerUserID, resolveErr := r.ResolveBoundUserIDByPlatformIdentity(ctx, platform, platformTenantID, senderID); resolveErr != nil {
		return nil, fmt.Errorf("resolve sender user id failed: %w", resolveErr)
	} else if ownerUserID != uuid.Nil {
		builder = builder.SetSenderUserID(ownerUserID)
	}

	item, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("save received message failed: %w", err)
	}
	return item, nil
}

// SaveSentMessage 儲存 assistant 送出的 outbound 訊息。
//
// replyToMsgID 與 triggeredMessageID 代表不同層級的關聯：
// - replyToMsgID：平台層的 reply 目標，只有實際用 reply API 成功送出時才寫入。
// - triggeredMessageID：系統層的觸發來源，用來表示這則 outbound 是由哪一則 inbound 產生。
// 保持兩者分離，避免把「平台回覆」誤當成「內部 command chain 觸發來源」。
func (r *ChannelMessageRepo) SaveSentMessage(
	ctx context.Context,
	channelID uuid.UUID,
	senderID string,
	senderName string,
	platformMessageID string,
	replyToMsgID string,
	content string,
	messageType string,
	platformTimestamp int64,
	triggeredMessageID uuid.UUID,
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
	// 只有平台真的建立 reply 關係時才保存 reply_to_msg_id；
	// push/direct 送出時保持空值，避免後續誤判為平台回覆鏈。
	if value := strings.TrimSpace(replyToMsgID); value != "" {
		builder = builder.SetReplyToMsgID(value)
	}
	if platformTimestamp > 0 {
		builder = builder.SetPlatformTimestamp(platformTimestamp)
	}
	// triggered_message_id 是內部觸發關係，與平台 reply 是否成功無關。
	// 即使最後改走 push，也仍可回溯 outbound 由哪則 inbound 觸發。
	if triggeredMessageID != uuid.Nil {
		builder = builder.SetTriggeredMessageID(triggeredMessageID)
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
// 1) triggered_message_id：系統訊息由某訊息觸發時寫入的內部關聯。
// 2) reply_to_msg_id：平台層 reply 目標 ID，在同 channel 內查詢被回覆訊息。
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

	// 先走內部關聯：當 triggered_message_id 存在時，代表這是系統觸發鏈路。
	if message.TriggeredMessageID != nil && *message.TriggeredMessageID != uuid.Nil {
		return r.GetMessageByID(ctx, *message.TriggeredMessageID)
	}
	// 一般使用者 reply 不會寫 triggered_message_id；需要父節點時直接用平台 reply id 查詢。
	if replyToMsgID := strings.TrimSpace(message.ReplyToMsgID); replyToMsgID != "" {
		return r.FindMessageByPlatformMessageID(ctx, message.ChannelID, replyToMsgID)
	}
	return nil, nil
}

// FindRecentMessagesBefore 取得同一 channel 內、指定訊息之前的近端訊息。
//
// 用途：
// - 支援 implicit reply linking：使用者沒有使用平台 reply，但短句語意上可能接續前面某個待辦候選。
// - 查詢只限制在同 channel，避免不同群組/私聊的訊息被拿來做上下文。
// - 回傳順序由舊到新，方便 prompt 以自然對話順序呈現。
func (r *ChannelMessageRepo) FindRecentMessagesBefore(ctx context.Context, message *ent.ChannelMessage, limit int) ([]*ent.ChannelMessage, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("channel repository not initialized")
	}
	if message == nil || message.ChannelID == uuid.Nil {
		return nil, nil
	}
	if limit <= 0 {
		return nil, nil
	}

	items, err := r.db.ChannelMessage.Query().
		Where(
			channelmessage.ChannelIDEQ(message.ChannelID),
			channelmessage.IDNEQ(message.ID),
			channelmessage.CreatedAtLTE(message.CreatedAt),
		).
		Order(ent.Desc(channelmessage.FieldCreatedAt)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query recent channel messages failed: %w", err)
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	return items, nil
}

// FindTodoCandidatesByMessageIDs 取得近端訊息已關聯的 TodoCandidate 結構化狀態。
//
// repository 只用 message id 做資料關聯，不判斷語意；是否承接、確認或更新仍交給 todo analyzer。
func (r *ChannelMessageRepo) FindTodoCandidatesByMessageIDs(ctx context.Context, channelID uuid.UUID, messageIDs []uuid.UUID) ([]*ent.TodoCandidate, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("channel repository not initialized")
	}
	if channelID == uuid.Nil || len(messageIDs) == 0 {
		return nil, nil
	}
	uniqueMessageIDs := make([]uuid.UUID, 0, len(messageIDs))
	seen := make(map[uuid.UUID]struct{}, len(messageIDs))
	for _, messageID := range messageIDs {
		if messageID == uuid.Nil {
			continue
		}
		if _, ok := seen[messageID]; ok {
			continue
		}
		seen[messageID] = struct{}{}
		uniqueMessageIDs = append(uniqueMessageIDs, messageID)
	}
	if len(uniqueMessageIDs) == 0 {
		return nil, nil
	}
	items, err := r.db.TodoCandidate.Query().
		Where(
			todocandidate.ChannelIDEQ(channelID),
			todocandidate.HasEvidenceMessagesWith(
				todocandidateevidencemessage.MessageIDIn(uniqueMessageIDs...),
			),
		).
		Order(ent.Asc(todocandidate.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query todo candidates by message ids failed: %w", err)
	}
	return items, nil
}

// FindActiveTodoCandidatesWithEvidence 依最近活躍 evidence 找出目前 channel 仍可追蹤的 Todo candidates。
//
// 這個查詢是長討論串上下文召回的入口：
// - 它不掃整個 channel 的所有歷史訊息，只看已被寫成 evidence anchor 的訊息。
// - acknowledged/cancelled candidate 不再作為 implicit context 候選，避免已結束任務被新訊息誤喚醒。
// - 排序以 candidate.updated_at 新到舊為主；語意是否承接仍交給 Todo analyzer，不在 repository 判斷。
func (r *ChannelMessageRepo) FindActiveTodoCandidatesWithEvidence(ctx context.Context, channelID uuid.UUID, limit int) ([]*ent.TodoCandidate, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("channel repository not initialized")
	}
	if channelID == uuid.Nil || limit <= 0 {
		return nil, nil
	}
	items, err := r.db.TodoCandidate.Query().
		Where(
			todocandidate.ChannelIDEQ(channelID),
			todocandidate.StatusIn(todocandidate.StatusCandidate, todocandidate.StatusNeedsMoreInfo),
			todocandidate.HasEvidenceMessagesWith(todocandidateevidencemessage.IsActiveEQ(true)),
		).
		Order(ent.Desc(todocandidate.FieldUpdatedAt), ent.Desc(todocandidate.FieldCreatedAt)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query active todo candidates with evidence failed: %w", err)
	}
	return items, nil
}

// FindRecentTodoCandidateEvidenceMessages 取得某個 candidate 最近的活躍 evidence anchors。
//
// 回傳順序會轉成時間正序，讓上層組 prompt 時能保持對話閱讀順序；limit 只控制 anchor 數量，
// 每個 anchor 前後要取幾則訊息由 FindMessageWindowAround 與 usecase config 決定。
func (r *ChannelMessageRepo) FindRecentTodoCandidateEvidenceMessages(ctx context.Context, candidateID uuid.UUID, limit int) ([]*ent.TodoCandidateEvidenceMessage, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("channel repository not initialized")
	}
	if candidateID == uuid.Nil || limit <= 0 {
		return nil, nil
	}
	items, err := r.db.TodoCandidateEvidenceMessage.Query().
		Where(
			todocandidateevidencemessage.CandidateIDEQ(candidateID),
			todocandidateevidencemessage.IsActiveEQ(true),
		).
		WithMessage().
		Order(ent.Desc(todocandidateevidencemessage.FieldUpdatedAt), ent.Desc(todocandidateevidencemessage.FieldCreatedAt)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query todo candidate evidence messages failed: %w", err)
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	return items, nil
}

// FindMessageWindowAround 取得同一 channel 內、指定 anchor message 前後的訊息窗口。
//
// 用途：
// - 支援顯式 reply/quote context assembly：被引用訊息可能很舊，但它附近的對話仍可能補充 assignee、時間或取消條件。
// - anchor message 會固定包含在回傳結果中，讓上層可以把多個 window 去重後依時間組合。
// - beforeLimit/afterLimit 只控制 anchor 前後各自取幾則；小於等於 0 時代表該側不取。
func (r *ChannelMessageRepo) FindMessageWindowAround(ctx context.Context, message *ent.ChannelMessage, beforeLimit int, afterLimit int) ([]*ent.ChannelMessage, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("channel repository not initialized")
	}
	if message == nil || message.ChannelID == uuid.Nil || message.ID == uuid.Nil {
		return nil, nil
	}

	items := make([]*ent.ChannelMessage, 0, beforeLimit+afterLimit+1)
	if beforeLimit > 0 {
		beforeItems, err := r.db.ChannelMessage.Query().
			Where(
				channelmessage.ChannelIDEQ(message.ChannelID),
				channelmessage.IDNEQ(message.ID),
				channelmessage.CreatedAtLT(message.CreatedAt),
			).
			Order(ent.Desc(channelmessage.FieldCreatedAt)).
			Limit(beforeLimit).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("query channel messages before anchor failed: %w", err)
		}
		for left, right := 0, len(beforeItems)-1; left < right; left, right = left+1, right-1 {
			beforeItems[left], beforeItems[right] = beforeItems[right], beforeItems[left]
		}
		items = append(items, beforeItems...)
	}

	items = append(items, message)

	if afterLimit > 0 {
		afterItems, err := r.db.ChannelMessage.Query().
			Where(
				channelmessage.ChannelIDEQ(message.ChannelID),
				channelmessage.IDNEQ(message.ID),
				channelmessage.CreatedAtGT(message.CreatedAt),
			).
			Order(ent.Asc(channelmessage.FieldCreatedAt)).
			Limit(afterLimit).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("query channel messages after anchor failed: %w", err)
		}
		items = append(items, afterItems...)
	}
	return items, nil
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

// SaveTodoCandidateFromAnalysis 依 Todo analyzer 的結構化輸出建立或更新候選待辦。
//
// 寫入策略：
// - create_candidate / needs_more_info：以目前訊息建立新的 candidate。
// - update_candidate / acknowledge / cancel_candidate：用 linked_message_id 對應的 evidence anchor 在同 channel 找既有 candidate，找到就更新最新狀態。
// - 找不到 linked candidate 時回錯誤，讓上層 log 出資料連結缺口；不靜默建立錯誤候選。
func (r *ChannelMessageRepo) SaveTodoCandidateFromAnalysis(ctx context.Context, input SaveTodoCandidateInput) (*ent.TodoCandidate, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("channel repository not initialized")
	}
	if input.ChannelID == uuid.Nil {
		return nil, fmt.Errorf("todo candidate channel id is required")
	}
	if input.MessageID == uuid.Nil {
		return nil, fmt.Errorf("todo candidate message id is required")
	}

	decision := todocandidate.LastDecision(strings.TrimSpace(input.Decision))
	status, err := todoCandidateStatusFromInput(input, decision)
	if err != nil {
		return nil, err
	}
	if decision == todocandidate.LastDecisionCreateCandidate || decision == todocandidate.LastDecisionNeedsMoreInfo {
		item, err := r.createTodoCandidate(ctx, input, decision, status)
		if err != nil {
			return nil, err
		}
		return item, r.promoteTodoCandidate(ctx, item)
	}

	if input.LinkedMessageID == uuid.Nil {
		return nil, fmt.Errorf("todo candidate linked message id is required for decision %s", decision)
	}
	existing, err := r.findTodoCandidateByLinkedMessage(ctx, input.ChannelID, input.LinkedMessageID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("todo candidate linked message %s has no existing candidate", input.LinkedMessageID)
	}
	item, err := r.updateTodoCandidate(ctx, existing.ID, input, decision, status)
	if err != nil {
		return nil, err
	}
	if decision == todocandidate.LastDecisionAcknowledge {
		applied, err := r.applyPendingTodoUpdateCandidate(ctx, item)
		if err != nil || applied {
			return item, err
		}
	}
	return item, r.promoteTodoCandidate(ctx, item)
}

func (r *ChannelMessageRepo) promoteTodoCandidate(ctx context.Context, candidate *ent.TodoCandidate) error {
	if r == nil || r.db == nil || candidate == nil {
		return nil
	}
	// promotion 是 Todo Reminder 的資料分層邊界：
	// TodoCandidate 保存模型抽取與對話 evidence，Todo 保存產品真正要提醒的目前狀態。
	// 因此這裡只在 candidate 已通過完整性 gate 時建立/銜接正式 Todo；
	// 尚未完整、尚未確認或取消中的資料，都留在 candidate/update candidate/event 層處理。
	if candidate.Status == todocandidate.StatusCancelled {
		// candidate 被取消時不刪正式 Todo，而是保留紀錄並轉 cancelled。
		// 取消原因、模型理由與追蹤資料留在 TodoCandidate；Todo 只維持產品狀態。
		return r.cancelTodoFromCandidate(ctx, candidate.ID)
	}
	if !isTodoCandidatePromotionReady(candidate) {
		zap.L().Info("todo candidate promotion skipped: candidate is not ready",
			zap.String("candidate_id", candidate.ID.String()),
			zap.String("status", string(candidate.Status)),
			zap.String("summary", strings.TrimSpace(candidate.Summary)),
			zap.Bool("has_due_at", candidate.DueAt != nil),
			zap.Strings("missing_fields", normalizeStringSlice(candidate.MissingFields)),
		)
		return nil
	}

	// promotion gate 只接受已正規化 due_at 且欄位完整的 candidate。
	// analyzer 仍負責語意抽取；repository 只為已解析成系統 User 的 assignee 建立「一人一筆」正式 Todo。
	ownerUserIDs, err := r.resolveTodoPromotionOwnerUserIDs(ctx, candidate.ID)
	if err != nil {
		return err
	}
	if len(ownerUserIDs) == 0 {
		zap.L().Info("todo candidate promotion skipped: no resolved owner",
			zap.String("candidate_id", candidate.ID.String()),
			zap.Strings("assignees", normalizeStringSlice(candidate.Assignees)),
		)
		return nil
	}
	for _, ownerUserID := range ownerUserIDs {
		// promotion 以 candidate+owner 作為 idempotency key：同一個 candidate 指派多個人會建立多筆 Todo，
		// 但重跑 analyzer 或 normalizer 時，同一個人的 Todo 只會被更新，不會重複建立。
		existing, err := r.db.Todo.Query().Where(
			todo.SourceCandidateIDEQ(candidate.ID),
			todo.OwnerUserIDEQ(ownerUserID),
		).Only(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return fmt.Errorf("query promoted todo failed: %w", err)
		}
		if existing == nil {
			// 第一次 promotion：正式 Todo 與 created event 必須一起產生。
			// created event 的 old_values 會是空物件，new_values 則保存目前可提醒的產品欄位快照。
			create := r.db.Todo.Create().
				SetChannelID(candidate.ChannelID).
				SetOwnerUserID(ownerUserID).
				SetSourceCandidateID(candidate.ID).
				SetStatus(todo.StatusActive).
				SetTitle(strings.TrimSpace(candidate.Summary)).
				SetDueAt(*candidate.DueAt).
				SetDueTimezone(strings.TrimSpace(candidate.DueTimezone)).
				SetDuePrecision(todo.DuePrecision(candidate.DuePrecision))
			created, err := create.Save(ctx)
			if err != nil {
				return fmt.Errorf("create promoted todo failed: %w", err)
			}
			if err := r.createTodoEvent(ctx, created, nil, candidate, todoevent.EventTypeCreated); err != nil {
				return err
			}
			continue
		}
		// 既有 Todo 再次被同一 candidate+owner 命中時，代表後續訊息提出了對正式 Todo 的變更。
		// 這裡不直接覆蓋 Todo，而是建立 requires_confirmation 更新候選，讓確認流程之後再決定是否 apply。
		if err := r.createTodoUpdateCandidate(ctx, existing, candidate, todoupdatecandidate.StatusRequiresConfirmation); err != nil {
			return err
		}
	}
	return nil
}

func (r *ChannelMessageRepo) createTodoUpdateCandidate(ctx context.Context, item *ent.Todo, candidate *ent.TodoCandidate, status todoupdatecandidate.Status) error {
	if r == nil || r.db == nil || item == nil || candidate == nil {
		return nil
	}
	// update candidate 是正式 Todo 的「建議變更」，不是已套用事件。
	// 因此 current_values 取正式 Todo 當下狀態，proposed_values 取 candidate 最新抽取結果；Todo 本身保持不變。
	create := r.db.TodoUpdateCandidate.Create().
		SetTodoID(item.ID).
		SetSourceCandidateID(candidate.ID).
		SetChangeType(todoupdatecandidate.ChangeTypeUpdated).
		SetStatus(status).
		SetCurrentValues(todoEventSnapshot(item)).
		SetProposedValues(todoCandidateProposedTodoValues(candidate)).
		SetConfidence(candidate.Confidence).
		SetReason(strings.TrimSpace(candidate.Reason))
	if candidate.LastMessageID != uuid.Nil {
		create.SetSourceMessageID(candidate.LastMessageID)
	}
	if _, err := create.Save(ctx); err != nil {
		return fmt.Errorf("create todo update candidate failed: %w", err)
	}
	return nil
}

func (r *ChannelMessageRepo) applyPendingTodoUpdateCandidate(ctx context.Context, candidate *ent.TodoCandidate) (bool, error) {
	if r == nil || r.db == nil || candidate == nil {
		return false, nil
	}
	// acknowledge 只會套用「同一個 source candidate 最新的一筆待確認更新」。
	// 這避免使用者連續改期時，較舊的 proposed_values 在晚到的確認訊息中被重新套用。
	// 若未找到 pending/requires_confirmation，呼叫端會回到一般 promotion 流程，讓新 candidate 仍可被處理。
	updateCandidate, err := r.db.TodoUpdateCandidate.Query().
		Where(
			todoupdatecandidate.SourceCandidateIDEQ(candidate.ID),
			todoupdatecandidate.StatusIn(todoupdatecandidate.StatusRequiresConfirmation, todoupdatecandidate.StatusPending),
		).
		Order(ent.Desc(todoupdatecandidate.FieldCreatedAt)).
		First(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			zap.L().Info("todo update candidate apply skipped: no pending update",
				zap.String("candidate_id", candidate.ID.String()),
			)
			return false, nil
		}
		return false, fmt.Errorf("query pending todo update candidate failed: %w", err)
	}
	item, err := r.db.Todo.Get(ctx, updateCandidate.TodoID)
	if err != nil {
		return false, fmt.Errorf("query todo for pending update apply failed: %w", err)
	}
	oldSnapshot := todoEventSnapshot(item)
	update := r.db.Todo.UpdateOneID(item.ID)
	if statusText := strings.TrimSpace(stringFromMap(updateCandidate.ProposedValues, "status")); statusText != "" {
		update.SetStatus(todo.Status(statusText))
	}
	if title := strings.TrimSpace(stringFromMap(updateCandidate.ProposedValues, "title")); title != "" {
		update.SetTitle(title)
	}
	if dueTimezone := strings.TrimSpace(stringFromMap(updateCandidate.ProposedValues, "due_timezone")); dueTimezone != "" {
		update.SetDueTimezone(dueTimezone)
	}
	if duePrecision := strings.TrimSpace(stringFromMap(updateCandidate.ProposedValues, "due_precision")); duePrecision != "" {
		update.SetDuePrecision(todo.DuePrecision(duePrecision))
	}
	dueAtText := strings.TrimSpace(stringFromMap(updateCandidate.ProposedValues, "due_at"))
	if dueAtText == "" {
		return false, fmt.Errorf("todo update candidate proposed due_at is required")
	}
	dueAt, err := time.Parse(time.RFC3339, dueAtText)
	if err != nil {
		return false, fmt.Errorf("parse todo update candidate proposed due_at failed: %w", err)
	}
	update.SetDueAt(dueAt)
	updated, err := update.Save(ctx)
	if err != nil {
		return false, fmt.Errorf("apply pending todo update failed: %w", err)
	}
	if _, err := r.db.TodoUpdateCandidate.UpdateOneID(updateCandidate.ID).
		SetStatus(todoupdatecandidate.StatusApplied).
		Save(ctx); err != nil {
		return false, fmt.Errorf("mark todo update candidate applied failed: %w", err)
	}
	if err := r.createTodoEvent(ctx, updated, oldSnapshot, candidate, todoevent.EventTypeUpdated); err != nil {
		return false, err
	}
	return true, nil
}

func (r *ChannelMessageRepo) createTodoEvent(ctx context.Context, item *ent.Todo, oldValues map[string]any, candidate *ent.TodoCandidate, eventType todoevent.EventType) error {
	if r == nil || r.db == nil || item == nil {
		return nil
	}
	// TodoEvent 是 promotion/apply 的 audit trail：Todo 欄位保存最新產品狀態，
	// event 保存這次套用前後的差異與 AI candidate 來源，讓後續使用者追問「時間怎麼變了」時有資料可查。
	create := r.db.TodoEvent.Create().
		SetTodoID(item.ID).
		SetEventType(eventType).
		SetOldValues(normalizeTodoEventValues(oldValues)).
		SetNewValues(todoEventSnapshot(item))
	if candidate != nil {
		create.SetSourceCandidateID(candidate.ID)
		// create/update promotion 會帶完整 candidate，因此可寫入 last_message_id。
		// cancelTodoFromCandidate 目前只收到 candidateID，沒有額外查 candidate；此時 source_message_id 會保持空值，避免猜測來源訊息。
		if candidate.LastMessageID != uuid.Nil {
			create.SetSourceMessageID(candidate.LastMessageID)
		}
		create.SetConfidence(candidate.Confidence)
		create.SetReason(strings.TrimSpace(candidate.Reason))
	}
	if _, err := create.Save(ctx); err != nil {
		return fmt.Errorf("create todo event failed: %w", err)
	}
	return nil
}

func normalizeTodoEventValues(values map[string]any) map[string]any {
	// Ent JSON 欄位用空 object 表示「沒有前一版值」，比 nil 更利於 GraphQL/JSON client 穩定處理。
	if len(values) == 0 {
		return map[string]any{}
	}
	return values
}

func stringFromMap(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func todoEventSnapshot(item *ent.Todo) map[string]any {
	if item == nil {
		return map[string]any{}
	}
	// snapshot 只保存 Todo 的產品狀態欄位，不複製 AI debug 欄位；
	// AI 的 confidence/reason/source_candidate 會放在 TodoEvent 自己的欄位，避免 new_values 混入兩種語意。
	values := map[string]any{
		"status":        string(item.Status),
		"title":         strings.TrimSpace(item.Title),
		"due_timezone":  strings.TrimSpace(item.DueTimezone),
		"due_precision": string(item.DuePrecision),
	}
	if item.DueAt != nil {
		values["due_at"] = item.DueAt.Format(time.RFC3339)
	} else {
		values["due_at"] = ""
	}
	return values
}

func todoCandidateProposedTodoValues(candidate *ent.TodoCandidate) map[string]any {
	if candidate == nil {
		return map[string]any{}
	}
	// proposed snapshot 使用和 TodoEvent new_values 一樣的欄位名稱，
	// 讓之後 apply update candidate 時可以直接把 proposed_values 轉成 Todo 更新與 TodoEvent updated 記錄。
	values := map[string]any{
		"status":        string(todo.StatusActive),
		"title":         strings.TrimSpace(candidate.Summary),
		"due_timezone":  strings.TrimSpace(candidate.DueTimezone),
		"due_precision": string(candidate.DuePrecision),
	}
	if candidate.DueAt != nil {
		values["due_at"] = candidate.DueAt.Format(time.RFC3339)
	} else {
		values["due_at"] = ""
	}
	return values
}

func (r *ChannelMessageRepo) resolveTodoPromotionOwnerUserIDs(ctx context.Context, candidateID uuid.UUID) ([]uuid.UUID, error) {
	if candidateID == uuid.Nil {
		return nil, nil
	}
	// owner 只能來自已解析成系統 User 的 assignee evidence。
	// mention 來源會回頭讀 source_message_mention.user_id；analyzer 字面來源則使用 TodoCandidateAssignee.resolved_user_id。
	assignees, err := r.db.TodoCandidateAssignee.Query().
		Where(todocandidateassignee.CandidateIDEQ(candidateID)).
		WithSourceMessageMention().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query todo candidate assignees for promotion failed: %w", err)
	}
	seen := make(map[uuid.UUID]struct{}, len(assignees))
	ownerUserIDs := make([]uuid.UUID, 0, len(assignees))
	for _, assignee := range assignees {
		if assignee == nil {
			continue
		}
		ownerUserID := uuid.Nil
		// 非 mention 的 analyzer/sender/reply_context evidence 會直接寫 resolved_user_id。
		if assignee.ResolvedUserID != nil {
			ownerUserID = *assignee.ResolvedUserID
		} else if assignee.Edges.SourceMessageMention != nil && assignee.Edges.SourceMessageMention.UserID != nil {
			// mention evidence 不複製 user_id 到 TodoCandidateAssignee，避免訊息 mention 事實被重複保存。
			ownerUserID = *assignee.Edges.SourceMessageMention.UserID
		}
		if ownerUserID == uuid.Nil {
			continue
		}
		if _, ok := seen[ownerUserID]; ok {
			continue
		}
		seen[ownerUserID] = struct{}{}
		ownerUserIDs = append(ownerUserIDs, ownerUserID)
	}
	return ownerUserIDs, nil
}

func isTodoCandidatePromotionReady(candidate *ent.TodoCandidate) bool {
	// promotion gate 是 Candidate -> Todo 的唯一資料完整性檢查點：
	// 只允許狀態可用、摘要可顯示、due_at 已正規化且沒有 missing_fields 的 candidate 進正式 Todo。
	if candidate == nil {
		return false
	}
	if candidate.Status != todocandidate.StatusCandidate && candidate.Status != todocandidate.StatusAcknowledged {
		return false
	}
	if strings.TrimSpace(candidate.Summary) == "" || candidate.DueAt == nil {
		return false
	}
	if len(normalizeStringSlice(candidate.MissingFields)) > 0 {
		return false
	}
	return true
}

func (r *ChannelMessageRepo) cancelTodoFromCandidate(ctx context.Context, candidateID uuid.UUID) error {
	if candidateID == uuid.Nil {
		return nil
	}
	// 取消流程只處理已經被 promote 的 Todo；若 candidate 尚未形成正式 Todo，
	// NotFound 代表沒有正式事項要取消，保留 candidate 自身狀態即可。
	existing, err := r.db.Todo.Query().Where(todo.SourceCandidateIDEQ(candidateID)).All(ctx)
	if err != nil {
		return fmt.Errorf("query todo for cancellation failed: %w", err)
	}
	for _, item := range existing {
		if item == nil {
			continue
		}
		// cancel 不是刪除正式 Todo，而是把產品狀態轉成 cancelled，再寫一筆 cancelled event。
		// 這樣使用者列表能保留歷史，後續也能知道取消是由哪個 candidate 觸發。
		oldSnapshot := todoEventSnapshot(item)
		updated, err := r.db.Todo.UpdateOneID(item.ID).
			SetStatus(todo.StatusCancelled).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("cancel promoted todo failed: %w", err)
		}
		// 這裡只用 candidateID 建立最小來源物件，避免取消流程為了補 log 欄位再查一次 candidate。
		// 若之後需要取消事件也帶 reason/last_message，可把 cancelTodoFromCandidate 改成接完整 candidate。
		candidate := &ent.TodoCandidate{ID: candidateID}
		if err := r.createTodoEvent(ctx, updated, oldSnapshot, candidate, todoevent.EventTypeCancelled); err != nil {
			return err
		}
	}
	return nil
}

func (r *ChannelMessageRepo) createTodoCandidate(ctx context.Context, input SaveTodoCandidateInput, decision todocandidate.LastDecision, status todocandidate.Status) (*ent.TodoCandidate, error) {
	create := r.db.TodoCandidate.Create().
		SetChannelID(input.ChannelID).
		SetSourceMessageID(input.MessageID).
		SetLastMessageID(input.MessageID).
		SetStatus(status).
		SetLastDecision(decision).
		SetSummary(strings.TrimSpace(input.Summary)).
		SetAssignees(normalizeStringSlice(input.Assignees)).
		SetDueText(strings.TrimSpace(input.DueText)).
		SetDueTimezone(strings.TrimSpace(input.DueTimezone)).
		SetDuePrecision(normalizeTodoDuePrecision(input.DuePrecision)).
		SetDueConfidence(input.DueConfidence).
		SetDueReason(strings.TrimSpace(input.DueReason)).
		SetMissingFields(normalizeStringSlice(input.MissingFields)).
		SetConfidence(input.Confidence).
		SetReason(strings.TrimSpace(input.Reason))
	if input.DueAt != nil {
		create.SetDueAt(*input.DueAt)
	}
	if dueDecision := normalizeTodoDueDecision(input.DueDecision); dueDecision != "" {
		create.SetDueNormalizeDecision(dueDecision)
	}
	item, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create todo candidate failed: %w", err)
	}
	if err := r.syncTodoCandidateMentionAssignees(ctx, item.ID, input.MessageID); err != nil {
		return nil, err
	}
	if err := r.syncTodoCandidateAnalyzerAssignees(ctx, item.ID, input.ChannelID, input.Assignees); err != nil {
		return nil, err
	}
	if err := r.upsertTodoCandidateEvidenceMessage(ctx, item.ID, input, todoCandidateEvidenceRelationFromDecision(decision), todocandidateevidencemessage.SourceAnalyzer); err != nil {
		return nil, err
	}
	return item, nil
}

func (r *ChannelMessageRepo) updateTodoCandidate(ctx context.Context, candidateID uuid.UUID, input SaveTodoCandidateInput, decision todocandidate.LastDecision, status todocandidate.Status) (*ent.TodoCandidate, error) {
	update := r.db.TodoCandidate.UpdateOneID(candidateID).
		SetLastMessageID(input.MessageID).
		SetStatus(status).
		SetLastDecision(decision).
		SetSummary(strings.TrimSpace(input.Summary)).
		SetAssignees(normalizeStringSlice(input.Assignees)).
		SetDueText(strings.TrimSpace(input.DueText)).
		SetDueTimezone(strings.TrimSpace(input.DueTimezone)).
		SetDuePrecision(normalizeTodoDuePrecision(input.DuePrecision)).
		SetDueConfidence(input.DueConfidence).
		SetDueReason(strings.TrimSpace(input.DueReason)).
		SetMissingFields(normalizeStringSlice(input.MissingFields)).
		SetConfidence(input.Confidence).
		SetReason(strings.TrimSpace(input.Reason))
	if input.DueAt != nil {
		update.SetDueAt(*input.DueAt)
	} else {
		update.ClearDueAt()
	}
	if dueDecision := normalizeTodoDueDecision(input.DueDecision); dueDecision != "" {
		update.SetDueNormalizeDecision(dueDecision)
	} else {
		update.ClearDueNormalizeDecision()
	}
	item, err := update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update todo candidate failed: %w", err)
	}
	if err := r.syncTodoCandidateMentionAssignees(ctx, item.ID, input.MessageID); err != nil {
		return nil, err
	}
	if err := r.syncTodoCandidateAnalyzerAssignees(ctx, item.ID, input.ChannelID, input.Assignees); err != nil {
		return nil, err
	}
	if err := r.upsertTodoCandidateEvidenceMessage(ctx, item.ID, input, todoCandidateEvidenceRelationFromDecision(decision), todocandidateevidencemessage.SourceAnalyzer); err != nil {
		return nil, err
	}
	return item, nil
}

// upsertTodoCandidateEvidenceMessage 把「本次 analyzer 決策使用到的訊息」寫成 candidate evidence anchor。
//
// 這裡刻意用 candidate_id + message_id + relation_type 做唯一語意單位：同一則訊息可能同時是建立來源、
// 後續更新或確認訊息，但每一種關係都只保留最新 confidence/reason。這樣 candidate 本身只保存目前狀態，
// 長討論串的訊息軌跡則集中在 evidence table，之後召回 prompt 時可以穩定地從 anchor 展開小訊息窗。
func (r *ChannelMessageRepo) upsertTodoCandidateEvidenceMessage(ctx context.Context, candidateID uuid.UUID, input SaveTodoCandidateInput, relationType todocandidateevidencemessage.RelationType, source todocandidateevidencemessage.Source) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("channel repository not initialized")
	}
	if candidateID == uuid.Nil || input.ChannelID == uuid.Nil || input.MessageID == uuid.Nil {
		return nil
	}
	existing, err := r.db.TodoCandidateEvidenceMessage.Query().
		Where(
			todocandidateevidencemessage.CandidateIDEQ(candidateID),
			todocandidateevidencemessage.MessageIDEQ(input.MessageID),
			todocandidateevidencemessage.RelationTypeEQ(relationType),
		).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("query todo candidate evidence message failed: %w", err)
	}
	if existing != nil {
		if _, err := r.db.TodoCandidateEvidenceMessage.UpdateOneID(existing.ID).
			SetSource(source).
			SetConfidence(input.Confidence).
			SetReason(strings.TrimSpace(input.Reason)).
			SetIsActive(true).
			Save(ctx); err != nil {
			return fmt.Errorf("update todo candidate evidence message failed: %w", err)
		}
		return nil
	}
	if _, err := r.db.TodoCandidateEvidenceMessage.Create().
		SetChannelID(input.ChannelID).
		SetCandidateID(candidateID).
		SetMessageID(input.MessageID).
		SetRelationType(relationType).
		SetSource(source).
		SetConfidence(input.Confidence).
		SetReason(strings.TrimSpace(input.Reason)).
		SetIsActive(true).
		Save(ctx); err != nil {
		return fmt.Errorf("create todo candidate evidence message failed: %w", err)
	}
	return nil
}

// todoCandidateEvidenceRelationFromDecision 將 Todo analyzer 的狀態機 decision 轉成 evidence 的訊息關係。
//
// decision 描述「candidate 接下來變成什麼狀態」，relation_type 描述「這則訊息在 candidate 歷史裡扮演什麼角色」。
// 兩者分開後，查詢端可以用 relation_type 還原上下文，而不必反推 candidate 的最新狀態欄位。
func todoCandidateEvidenceRelationFromDecision(decision todocandidate.LastDecision) todocandidateevidencemessage.RelationType {
	switch decision {
	case todocandidate.LastDecisionCreateCandidate:
		return todocandidateevidencemessage.RelationTypeSource
	case todocandidate.LastDecisionUpdateCandidate:
		return todocandidateevidencemessage.RelationTypeUpdate
	case todocandidate.LastDecisionAcknowledge:
		return todocandidateevidencemessage.RelationTypeAcknowledgement
	case todocandidate.LastDecisionCancelCandidate:
		return todocandidateevidencemessage.RelationTypeCancellation
	case todocandidate.LastDecisionNeedsMoreInfo:
		return todocandidateevidencemessage.RelationTypeClarification
	default:
		return todocandidateevidencemessage.RelationTypeRelatedContext
	}
}

func (r *ChannelMessageRepo) syncTodoCandidateMentionAssignees(ctx context.Context, candidateID uuid.UUID, messageID uuid.UUID) error {
	if candidateID == uuid.Nil || messageID == uuid.Nil {
		return nil
	}
	mentions, err := r.db.ChannelMessageMention.Query().
		Where(channelmessagemention.ChannelMessageIDEQ(messageID)).
		Order(ent.Asc(channelmessagemention.FieldMentionIndex), ent.Asc(channelmessagemention.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("query todo candidate source mentions failed: %w", err)
	}
	if len(mentions) == 0 {
		// follow-up 訊息可能只是「我晚點補」而沒有 mention；此時保留既有 mention assignee 快照，避免把原始指派資訊清掉。
		return nil
	}
	if _, err := r.db.TodoCandidateAssignee.Delete().
		Where(
			todocandidateassignee.CandidateIDEQ(candidateID),
			todocandidateassignee.SourceEQ(todocandidateassignee.SourceMention),
		).
		Exec(ctx); err != nil {
		return fmt.Errorf("clear todo candidate mention assignees failed: %w", err)
	}

	creates := make([]*ent.TodoCandidateAssigneeCreate, 0, len(mentions))
	for _, mention := range mentions {
		if mention == nil {
			continue
		}
		create := r.db.TodoCandidateAssignee.Create().
			SetCandidateID(candidateID).
			SetSourceMessageMentionID(mention.ID).
			SetSource(todocandidateassignee.SourceMention).
			SetReason("linked to structured message mention")
		creates = append(creates, create)
	}
	if len(creates) == 0 {
		return nil
	}
	if err := r.db.TodoCandidateAssignee.CreateBulk(creates...).Exec(ctx); err != nil {
		return fmt.Errorf("save todo candidate mention assignees failed: %w", err)
	}
	return nil
}

func (r *ChannelMessageRepo) syncTodoCandidateAnalyzerAssignees(ctx context.Context, candidateID uuid.UUID, channelID uuid.UUID, assignees []string) error {
	if candidateID == uuid.Nil || channelID == uuid.Nil {
		return nil
	}
	normalizedAssignees := normalizeStringSlice(assignees)
	if _, err := r.db.TodoCandidateAssignee.Delete().
		Where(
			todocandidateassignee.CandidateIDEQ(candidateID),
			todocandidateassignee.SourceEQ(todocandidateassignee.SourceAnalyzer),
		).
		Exec(ctx); err != nil {
		return fmt.Errorf("clear todo candidate analyzer assignees failed: %w", err)
	}
	if len(normalizedAssignees) == 0 {
		return nil
	}

	creates := make([]*ent.TodoCandidateAssigneeCreate, 0, len(normalizedAssignees))
	for _, assignee := range normalizedAssignees {
		matchedUserID, status, reason, err := r.resolveAnalyzerAssigneeByChannelSenderName(ctx, channelID, assignee)
		if err != nil {
			return err
		}
		create := r.db.TodoCandidateAssignee.Create().
			SetCandidateID(candidateID).
			SetSource(todocandidateassignee.SourceAnalyzer).
			SetAssigneeText(assignee).
			SetResolutionStatus(status).
			SetReason(reason)
		if matchedUserID != uuid.Nil {
			create.SetResolvedUserID(matchedUserID)
		}
		creates = append(creates, create)
	}
	if len(creates) == 0 {
		return nil
	}
	if err := r.db.TodoCandidateAssignee.CreateBulk(creates...).Exec(ctx); err != nil {
		return fmt.Errorf("save todo candidate analyzer assignees failed: %w", err)
	}
	return nil
}

func (r *ChannelMessageRepo) resolveAnalyzerAssigneeByChannelSenderName(ctx context.Context, channelID uuid.UUID, assignee string) (uuid.UUID, todocandidateassignee.ResolutionStatus, string, error) {
	assignee = strings.TrimSpace(assignee)
	if assignee == "" {
		return uuid.Nil, todocandidateassignee.ResolutionStatusUnsupported, "empty analyzer assignee", nil
	}
	items, err := r.db.ChannelMessage.Query().
		Where(
			channelmessage.ChannelIDEQ(channelID),
			channelmessage.SenderNameEqualFold(assignee),
			channelmessage.SenderUserIDNotNil(),
		).
		Order(ent.Desc(channelmessage.FieldCreatedAt)).
		Limit(50).
		All(ctx)
	if err != nil {
		return uuid.Nil, "", "", fmt.Errorf("query analyzer assignee sender matches failed: %w", err)
	}
	uniqueUserIDs := make(map[uuid.UUID]struct{}, len(items))
	for _, item := range items {
		if item == nil || item.SenderUserID == nil || *item.SenderUserID == uuid.Nil {
			continue
		}
		uniqueUserIDs[*item.SenderUserID] = struct{}{}
	}
	switch len(uniqueUserIDs) {
	case 0:
		return uuid.Nil, todocandidateassignee.ResolutionStatusUnresolved, "no channel sender matched analyzer assignee display text", nil
	case 1:
		for userID := range uniqueUserIDs {
			return userID, todocandidateassignee.ResolutionStatusResolved, "matched unique channel sender display name", nil
		}
	}
	return uuid.Nil, todocandidateassignee.ResolutionStatusAmbiguous, "multiple channel senders matched analyzer assignee display text", nil
}

// findTodoCandidateByLinkedMessage 透過 evidence anchor 解析 LLM 回傳的 linked_message_id。
//
// linked_message_id 是模型在 bounded context 中選出的「被承接訊息 ID」，不是 candidate row id。
// repository 因此只允許在同 channel、仍活躍的 evidence 裡查找 candidate；找不到就 fail-fast 回到上層，
// 避免把訊息 ID 誤當資料列 ID 或跨 channel 串錯待辦。
func (r *ChannelMessageRepo) findTodoCandidateByLinkedMessage(ctx context.Context, channelID uuid.UUID, linkedMessageID uuid.UUID) (*ent.TodoCandidate, error) {
	item, err := r.db.TodoCandidateEvidenceMessage.Query().
		Where(
			todocandidateevidencemessage.ChannelIDEQ(channelID),
			todocandidateevidencemessage.MessageIDEQ(linkedMessageID),
			todocandidateevidencemessage.IsActiveEQ(true),
		).
		Order(ent.Desc(todocandidateevidencemessage.FieldUpdatedAt), ent.Desc(todocandidateevidencemessage.FieldCreatedAt)).
		WithCandidate().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("query todo candidate evidence by linked message failed: %w", err)
	}
	candidate, err := item.Edges.CandidateOrErr()
	if err != nil {
		return nil, fmt.Errorf("load todo candidate from evidence failed: %w", err)
	}
	return candidate, nil
}

func todoCandidateStatusFromDecision(decision todocandidate.LastDecision) (todocandidate.Status, error) {
	switch decision {
	case todocandidate.LastDecisionCreateCandidate, todocandidate.LastDecisionUpdateCandidate:
		return todocandidate.StatusCandidate, nil
	case todocandidate.LastDecisionNeedsMoreInfo:
		return todocandidate.StatusNeedsMoreInfo, nil
	case todocandidate.LastDecisionAcknowledge:
		return todocandidate.StatusAcknowledged, nil
	case todocandidate.LastDecisionCancelCandidate:
		return todocandidate.StatusCancelled, nil
	default:
		return "", fmt.Errorf("todo candidate decision %q is not persistable", decision)
	}
}

func todoCandidateStatusFromInput(input SaveTodoCandidateInput, decision todocandidate.LastDecision) (todocandidate.Status, error) {
	status, err := todoCandidateStatusFromDecision(decision)
	if err != nil {
		return "", err
	}
	if status == todocandidate.StatusCandidate && strings.TrimSpace(input.DueDecision) == "needs_more_info" {
		return todocandidate.StatusNeedsMoreInfo, nil
	}
	return status, nil
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
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
	return out
}

func normalizeTodoDuePrecision(value string) todocandidate.DuePrecision {
	switch todocandidate.DuePrecision(strings.TrimSpace(value)) {
	case todocandidate.DuePrecisionDatetime, todocandidate.DuePrecisionDate, todocandidate.DuePrecisionRelativeWindow:
		return todocandidate.DuePrecision(strings.TrimSpace(value))
	default:
		return todocandidate.DuePrecisionUnknown
	}
}

func normalizeTodoDueDecision(value string) todocandidate.DueNormalizeDecision {
	switch todocandidate.DueNormalizeDecision(strings.TrimSpace(value)) {
	case todocandidate.DueNormalizeDecisionNormalized, todocandidate.DueNormalizeDecisionNeedsMoreInfo, todocandidate.DueNormalizeDecisionNoDueTime:
		return todocandidate.DueNormalizeDecision(strings.TrimSpace(value))
	default:
		return ""
	}
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

// HasChannelRealtimeTextScanService 回傳 channel 是否存在任何「已被使用者啟用、且需要文字掃描」的即時服務。
//
// 這個查詢用在非指令訊息進 classifier 前的 coarse gate。
// 它刻意從 channel_service_members 出發，而不是只查 skills：
// - skill.is_realtime / skill.requires_text_scan 只代表「這種服務具備什麼能力」。
// - channel_service_members 才代表「這個 channel 裡有人真的開啟了這項服務」。
//
// 因此只有同時符合下列條件時才回傳 true：
// 1) channel_service_members.channel_id 等於目前訊息所在 channel。
// 2) 該啟用紀錄關聯的 skill 是 realtime skill。
// 3) 該 skill 需要對非指令文字做分類掃描。
//
// 若 channel 裡沒有任何使用者啟用這類服務，就算系統內存在 requires_text_scan 的 skill，
// 也不應呼叫 classifier，避免每則普通訊息都產生不必要的 DB/模型成本。
// 查詢使用 EXISTS，搭配 channel_service_members(channel_id, skill_id) 索引即可快速回答「有沒有」；
// 不需要載入完整 member 清單，也不需要逐一檢查每個 service。
func (r *ChannelMessageRepo) HasChannelRealtimeTextScanService(ctx context.Context, channelID uuid.UUID) (bool, error) {
	if r == nil || r.db == nil {
		return false, fmt.Errorf("channel repository not initialized")
	}
	if channelID == uuid.Nil {
		return false, fmt.Errorf("channel id is required")
	}

	exists, err := r.db.ChannelServiceMember.Query().
		Where(
			channelservicemember.ChannelIDEQ(channelID),
			channelservicemember.HasSkillWith(
				skill.IsRealtimeEQ(true),
				skill.RequiresTextScanEQ(true),
			),
		).
		Exist(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to query realtime text scan service: %w", err)
	}
	return exists, nil
}

// RemoveServiceMemberFromChannel 移除某使用者在某頻道啟用的指定技能。
//
// 使用場景：
// - 關閉翻譯時，將下指令者從 channel_service_member 的翻譯 skill 關聯中移除。
// - 此方法只刪除 (channel_id, user_id, skill_id) 精準命中的那一筆，不影響同頻道其他使用者。
//
// 回傳值為實際刪除筆數；找不到資料時回傳 0, nil，讓停用指令具備冪等性。
func (r *ChannelMessageRepo) RemoveServiceMemberFromChannel(ctx context.Context, channelID uuid.UUID, ownerID uuid.UUID, skillID uuid.UUID) (int, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("channel repository not initialized")
	}
	if channelID == uuid.Nil {
		return 0, fmt.Errorf("channel id is required")
	}
	if ownerID == uuid.Nil {
		return 0, fmt.Errorf("owner id is required")
	}
	if skillID == uuid.Nil {
		return 0, fmt.Errorf("skill id is required")
	}

	deleted, err := r.db.ChannelServiceMember.Delete().
		Where(
			channelservicemember.ChannelIDEQ(channelID),
			channelservicemember.UserIDEQ(ownerID),
			channelservicemember.SkillIDEQ(skillID),
		).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to remove service member from channel: %w", err)
	}
	return deleted, nil
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

// RemoveTranslationLocalesFromChannel 移除某使用者在某頻道建立的翻譯語系。
//
// 刪除範圍固定包含：
// - channel_id：只處理目前下指令所在 channel。
// - skill_id：只處理翻譯 skill，不影響其他技能的設定。
// - owner_user_id：只刪除下指令者建立的 locales，不刪同 channel 其他人的翻譯設定。
//
// targetLocales 為空時代表 stop_translation_all，會刪除該 owner 的全部翻譯語系；
// targetLocales 有值時代表 stop_translation_locale，只刪指定語系。
func (r *ChannelMessageRepo) RemoveTranslationLocalesFromChannel(ctx context.Context, channelID uuid.UUID, skillID uuid.UUID, ownerUserID uuid.UUID, targetLocales []string) (int, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("channel repository not initialized")
	}
	if channelID == uuid.Nil {
		return 0, fmt.Errorf("channel id is required")
	}
	if skillID == uuid.Nil {
		return 0, fmt.Errorf("skill id is required")
	}
	if ownerUserID == uuid.Nil {
		return 0, fmt.Errorf("owner user id is required")
	}

	predicates := []predicate.TranslationLocale{
		translationlocale.ChannelIDEQ(channelID),
		translationlocale.SkillIDEQ(skillID),
		translationlocale.OwnerUserIDEQ(ownerUserID),
	}
	locales := normalizeLocaleFilter(targetLocales)
	if len(locales) > 0 {
		// 指定語系關閉時才加 target_locale 條件；整體關閉時不可加空 IN 條件，
		// 否則會變成刪不到任何資料。
		predicates = append(predicates, translationlocale.TargetLocaleIn(locales...))
	}

	deleted, err := r.db.TranslationLocale.Delete().Where(predicates...).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to remove translation locales: %w", err)
	}
	return deleted, nil
}

// CountOwnerTranslationLocales 統計某使用者在 channel 中仍保留的翻譯語系數量。
//
// stop_translation_locale 會用這個數字判斷是否還需要保留 channel_service_member：
// - count > 0：仍有其他語系啟用，保留 service member。
// - count == 0：已無翻譯語系，移除 service member，表示此使用者已關閉翻譯 skill。
func (r *ChannelMessageRepo) CountOwnerTranslationLocales(ctx context.Context, channelID uuid.UUID, skillID uuid.UUID, ownerUserID uuid.UUID) (int, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("channel repository not initialized")
	}
	if channelID == uuid.Nil {
		return 0, fmt.Errorf("channel id is required")
	}
	if skillID == uuid.Nil {
		return 0, fmt.Errorf("skill id is required")
	}
	if ownerUserID == uuid.Nil {
		return 0, fmt.Errorf("owner user id is required")
	}

	count, err := r.db.TranslationLocale.Query().
		Where(
			translationlocale.ChannelIDEQ(channelID),
			translationlocale.SkillIDEQ(skillID),
			translationlocale.OwnerUserIDEQ(ownerUserID),
		).
		Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to count translation locales: %w", err)
	}
	return count, nil
}

func normalizeLocaleFilter(targetLocales []string) []string {
	if len(targetLocales) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(targetLocales))
	locales := make([]string, 0, len(targetLocales))
	for _, targetLocale := range targetLocales {
		locale := strings.TrimSpace(targetLocale)
		if locale == "" {
			continue
		}
		// DB 目前保存的是原始 locale 字面；這裡只用 lower key 去重，
		// 實際刪除時仍保留第一個輸入值，避免在 repository 層做額外語系轉換。
		key := strings.ToLower(locale)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		locales = append(locales, locale)
	}
	return locales
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
