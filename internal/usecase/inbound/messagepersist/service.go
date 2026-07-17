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
	GetChannelByPlatformGroupID(ctx context.Context, platform string, groupID string) (*ent.Channel, error)
	UpdateChannelDisplayNameByID(ctx context.Context, channelID uuid.UUID, channelName string) error
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
	// LinkRelatedMessageByReply 依平台 reply id 補上 related_message_id。
	// 目的：把「平台層回覆關係」轉成資料庫內可遞迴追蹤的訊息鍊關聯。
	LinkRelatedMessageByReply(ctx context.Context, message *ent.ChannelMessage) (*ent.ChannelMessage, error)
}

// SenderNameResolver 定義不同 provider 解析 sender 顯示名稱的擴充點。
// LINE/Slack/WhatsApp 都可以注入各自策略；未提供時會使用 Noop。
type SenderNameResolver interface {
	ResolveSenderName(ctx context.Context, platform string, channelID string, channelType string, senderID string) (string, error)
}

// SenderNameResolverFunc 讓函式可直接作為 resolver 使用。
type SenderNameResolverFunc func(ctx context.Context, platform string, channelID string, channelType string, senderID string) (string, error)

func (f SenderNameResolverFunc) ResolveSenderName(ctx context.Context, platform string, channelID string, channelType string, senderID string) (string, error) {
	if f == nil {
		return "", nil
	}
	return f(ctx, platform, channelID, channelType, senderID)
}

// NoopSenderNameResolver 是預設 resolver，不做任何名稱反查。
// 適合 Slack/WhatsApp 尚未接 profile 查詢前先共用持久化主流程。
type NoopSenderNameResolver struct{}

func (NoopSenderNameResolver) ResolveSenderName(ctx context.Context, platform string, channelID string, channelType string, senderID string) (string, error) {
	return "", nil
}

// Service 提供跨 provider 的統一訊息持久化流程。
type Service struct {
	store    ChannelMessageStore
	resolver SenderNameResolver
}

// NewService 建立入站訊息持久化服務。
//
// 設計說明：
//   - resolver 允許各平台自行補齊 sender 顯示名稱（例如 LINE profile、Slack profile）。
//   - 當 resolver 未提供時，回退到 Noop 以維持流程可執行；
//     這裡的回退僅限「顯示名稱附加資訊」，不影響綁定/權限等核心約束。
func NewService(store ChannelMessageStore, resolver SenderNameResolver) Service {
	if resolver == nil {
		resolver = NoopSenderNameResolver{}
	}
	return Service{store: store, resolver: resolver}
}

// PersistUnifiedMessage 將統一訊息格式寫入 channel 與 channel_message。
// AI 判讀、規則判斷等非持久化邏輯不應出現在這裡。
func (s Service) PersistUnifiedMessage(ctx context.Context, message *unifiedmessage.Message) *ent.ChannelMessage {
	// 第一層防呆：無訊息或未注入 store 時，直接略過。
	if message == nil || s.store == nil {
		return nil
	}

	// ChannelID 是訊息歸屬識別；缺少時無法正確落庫。
	channelID := strings.TrimSpace(message.ChannelID)
	if channelID == "" {
		return nil
	}

	// senderID 為必要欄位，缺值時統一為 unknown，避免 DB 約束失敗。
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

	// 名稱解析是附加資訊，不應影響主流程；失敗僅記警告。
	senderName, err := resolver.ResolveSenderName(ctx, platform, channelID, channelType, senderID)
	if err != nil {
		zap.L().Warn("resolve sender name failed",
			zap.String("platform", platform),
			zap.String("sender", senderID),
			zap.Error(err),
		)
	}

	// 嚴格模式：入站訊息只允許寫入既有 channel，不可在此自動建立。
	// channel 必須由綁定流程先建立，才能避免未綁定來源汙染資料。
	ch, err := s.store.GetChannelByPlatformGroupID(ctx, platform, channelID)
	if err != nil {
		zap.L().Error("persist unified message channel failed",
			zap.String("platform", platform),
			zap.String("channel_id", channelID),
			zap.String("channel_type", channelType),
			zap.Error(err),
		)
		return nil
	}
	if ch == nil || ch.ID == uuid.Nil {
		// 找不到 channel 代表此來源尚未完成綁定初始化。
		// 依需求此處必須直接略過，不得偷建 channel。
		zap.L().Warn("persist unified message skipped: channel not bound",
			zap.String("platform", platform),
			zap.String("channel_id", channelID),
			zap.String("channel_type", channelType),
		)
		return nil
	}

	resolvedChannelName := strings.TrimSpace(message.ChannelName)
	if strings.EqualFold(channelType, "private") {
		// private channel 名稱以使用者顯示名稱為準。
		if senderNameTrimmed := strings.TrimSpace(senderName); senderNameTrimmed != "" {
			resolvedChannelName = senderNameTrimmed
		}
	}
	if resolvedChannelName != "" {
		if err := s.store.UpdateChannelDisplayNameByID(ctx, ch.ID, resolvedChannelName); err != nil {
			zap.L().Error("persist unified message channel name update failed",
				zap.String("platform", platform),
				zap.String("channel_id", channelID),
				zap.String("channel_name", resolvedChannelName),
				zap.Error(err),
			)
			return nil
		}
	}

	// 寫入原始訊息資料。這一步只處理「訊息本體」，不做語意或命令判斷。
	item, err := s.store.SaveReceivedMessage(
		ctx,
		ch.ID,
		senderID,
		strings.TrimSpace(senderName),
		strings.TrimSpace(message.PlatformMessageID),
		strings.TrimSpace(message.ReplyToMsgID),
		message.Text,
		strings.TrimSpace(message.MessageType),
		message.PlatformTimestamp,
	)
	if err != nil {
		zap.L().Error("persist unified message failed",
			zap.String("platform", platform),
			zap.String("channel_id", ch.ID.String()),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.Error(err),
		)
		return nil
	}

	// 第二階段補關聯：若此訊息是回覆，嘗試把 related_message_id 連到父訊息。
	// 這讓後續「是否在指令鍊上」可以用結構化關聯遞迴判斷。
	// 注意：關聯失敗不回滾主訊息，避免因補鏈失敗導致訊息遺失。
	if linkedItem, linkErr := s.store.LinkRelatedMessageByReply(ctx, item); linkErr != nil {
		zap.L().Warn("link related message failed",
			zap.String("platform", platform),
			zap.String("channel_id", ch.ID.String()),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.Error(linkErr),
		)
		return item
	} else if linkedItem != nil {
		item = linkedItem
	}

	return item
}
