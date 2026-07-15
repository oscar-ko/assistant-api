package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"assistant-api/internal/ent"
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

// ResolveUserIDByLineUserID resolves internal user UUID from LINE user id binding.
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

// GetOrCreateChannel finds existing channel by platform/group_id or creates one.
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

// AddServiceMemberToChannel adds a user into channel_service_members.
// The operation is idempotent and ignored if (channel_id, user_id, skill_id) already exists.
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
