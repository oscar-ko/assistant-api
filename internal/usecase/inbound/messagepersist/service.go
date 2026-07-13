package messagepersist

import (
	"context"
	"strings"

	"assistant-api/internal/ent"
	"assistant-api/internal/integration/unifiedmessage"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ChannelMessageStore 定義持久化統一訊息所需的最小資料存取能力。
// 只保留跨 provider 共用的 channel/message upsert 行為。
type ChannelMessageStore interface {
	GetOrCreateChannel(ctx context.Context, platform string, groupID string, channelType string) (*ent.Channel, error)
	SaveReceivedMessage(
		ctx context.Context,
		channelID uuid.UUID,
		senderID string,
		senderName string,
		platformMessageID string,
		replyToMsgID string,
		content string,
		messageType string,
		platformTimestamp int64,
	) (*ent.ChannelMessage, error)
}

// SenderNameResolver 定義不同 provider 解析 sender 顯示名稱的擴充點。
// LINE/Slack/WhatsApp 都可以注入各自策略；未提供時會使用 Noop。
type SenderNameResolver interface {
	ResolveSenderName(ctx context.Context, platform string, senderID string) (string, error)
}

// SenderNameResolverFunc 讓函式可直接作為 resolver 使用。
type SenderNameResolverFunc func(ctx context.Context, platform string, senderID string) (string, error)

func (f SenderNameResolverFunc) ResolveSenderName(ctx context.Context, platform string, senderID string) (string, error) {
	if f == nil {
		return "", nil
	}
	return f(ctx, platform, senderID)
}

// NoopSenderNameResolver 是預設 resolver，不做任何名稱反查。
// 適合 Slack/WhatsApp 尚未接 profile 查詢前先共用持久化主流程。
type NoopSenderNameResolver struct{}

func (NoopSenderNameResolver) ResolveSenderName(ctx context.Context, platform string, senderID string) (string, error) {
	return "", nil
}

// Service 提供跨 provider 的統一訊息持久化流程。
type Service struct {
	store    ChannelMessageStore
	resolver SenderNameResolver
}

func NewService(store ChannelMessageStore, resolver SenderNameResolver) Service {
	if resolver == nil {
		resolver = NoopSenderNameResolver{}
	}
	return Service{store: store, resolver: resolver}
}

// PersistUnifiedMessage 將統一訊息格式寫入 channel 與 channel_message。
// AI 判讀、規則判斷等非持久化邏輯不應出現在這裡。
func (s Service) PersistUnifiedMessage(ctx context.Context, message *unifiedmessage.Message) {
	if message == nil || s.store == nil {
		return
	}

	channelID := strings.TrimSpace(message.ChannelID)
	if channelID == "" {
		return
	}

	senderID := strings.TrimSpace(message.SenderID)
	if senderID == "" {
		senderID = "unknown"
	}

	platform := strings.TrimSpace(message.Platform)
	channelType := strings.TrimSpace(message.ChannelType)

	resolver := s.resolver
	if resolver == nil {
		resolver = NoopSenderNameResolver{}
	}

	senderName, err := resolver.ResolveSenderName(ctx, platform, senderID)
	if err != nil {
		zap.L().Warn("resolve sender name failed",
			zap.String("platform", platform),
			zap.String("sender", senderID),
			zap.Error(err),
		)
	}

	ch, err := s.store.GetOrCreateChannel(ctx, platform, channelID, channelType)
	if err != nil {
		zap.L().Error("persist unified message channel failed",
			zap.String("platform", platform),
			zap.String("channel_id", channelID),
			zap.String("channel_type", channelType),
			zap.Error(err),
		)
		return
	}

	if _, err := s.store.SaveReceivedMessage(
		ctx,
		ch.ID,
		senderID,
		strings.TrimSpace(senderName),
		strings.TrimSpace(message.PlatformMessageID),
		strings.TrimSpace(message.ReplyToMsgID),
		message.Text,
		strings.TrimSpace(message.MessageType),
		message.PlatformTimestamp,
	); err != nil {
		zap.L().Error("persist unified message failed",
			zap.String("platform", platform),
			zap.String("channel_id", ch.ID.String()),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.Error(err),
		)
	}
}
