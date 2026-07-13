package repository

import (
	"context"
	"fmt"
	"strings"

	"assistant-api/internal/ent"
	"assistant-api/internal/ent/channel"
	"assistant-api/internal/ent/channelmessage"
	"assistant-api/internal/ent/line"

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

	var relatedMessageID *uuid.UUID
	if replyTo := strings.TrimSpace(replyToMsgID); replyTo != "" {
		related, err := r.db.ChannelMessage.Query().
			Where(
				channelmessage.ChannelIDEQ(channelID),
				channelmessage.PlatformMessageIDEQ(replyTo),
			).
			Order(ent.Desc(channelmessage.FieldID)).
			First(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return nil, fmt.Errorf("resolve quoted message failed: %w", err)
		}
		if err == nil && related != nil {
			id := related.ID
			relatedMessageID = &id
		}
	}

	builder := r.db.ChannelMessage.Create().
		SetChannelID(channelID).
		SetSenderID(senderID).
		SetMessageType(messageType).
		SetContent(content)
	if relatedMessageID != nil {
		builder = builder.SetRelatedMessageID(*relatedMessageID)
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

	item, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("save received message failed: %w", err)
	}
	return item, nil
}
