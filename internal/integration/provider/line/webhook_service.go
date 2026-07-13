package line

import (
	"context"
	"encoding/json"
	"strings"

	"assistant-api/internal/config"
	"assistant-api/internal/integration/provider/messageintent"
	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"

	"go.uber.org/zap"
)

// WebhookService 定義 LINE webhook 的處理介面，方便注入不同實作。
type WebhookService interface {
	// ProcessIncoming 接收原始 webhook body 與簽章字串，執行後續處理。
	// 目前預設實作只做解析與 console 輸出，未做簽章驗證與持久化。
	ProcessIncoming(body []byte, signature string)
}

// consoleWebhookService 是最小可用的預設實作：
// 僅解析事件並輸出到 console，便於開發階段觀察 webhook 是否正常進站。
type consoleWebhookService struct {
	repo       *repository.ChannelMessageRepo
	classifier messageintent.Classifier
}

// NewWebhookService 建立預設 webhook service
func NewWebhookService(repo *repository.ChannelMessageRepo, classifier messageintent.Classifier) WebhookService {
	return consoleWebhookService{repo: repo, classifier: classifier}
}

// webhookRequest 對應 LINE webhook 最上層 payload。
type webhookRequest struct {
	// Events 為 LINE 一次 webhook payload 內包含的事件陣列。
	Events []webhookEvent `json:"events"`
}

// webhookEvent 對應 events[] 內單一事件。
type webhookEvent struct {
	// Type 事件類型，例如 message、follow、unfollow 等。
	Type string `json:"type"`
	// Source 訊息來源（個人、群組、聊天室）資訊。
	Source webhookEventSource `json:"source"`
	// Message 僅在 message 事件時有意義；其他事件可能為零值。
	Message webhookMessage `json:"message"`
	// Timestamp 為 LINE 事件時間戳 (unix milliseconds)。
	Timestamp int64 `json:"timestamp"`
}

// webhookEventSource 描述事件來源身分（私聊/群組/聊天室）。
type webhookEventSource struct {
	// Type 來源型別：user/group/room。
	Type string `json:"type"`
	// UserID 為一對一聊天來源的使用者 ID。
	UserID string `json:"userId"`
	// GroupID 為群組聊天來源 ID。
	GroupID string `json:"groupId"`
	// RoomID 為多人聊天室來源 ID。
	RoomID string `json:"roomId"`
}

// webhookMessage 為 message 事件的訊息主體。
type webhookMessage struct {
	// ID 為 LINE message ID，可用於後續追蹤或回覆流程。
	ID string `json:"id"`
	// Type 訊息型別，例如 text、image、audio。
	Type string `json:"type"`
	// Text 僅在 text 訊息時有內容。
	Text string `json:"text"`
	// QuotedMessageID 為被引用訊息的 ID。
	QuotedMessageID string `json:"quotedMessageId"`
	// Mention 為 LINE mention 資訊。
	Mention *webhookMessageMention `json:"mention,omitempty"`
}

type webhookMessageMention struct {
	// Mentionees 為被 mention 的使用者清單。
	Mentionees []webhookMentionee `json:"mentionees"`
}

type webhookMentionee struct {
	// Index 為 mention 在文字中的位置。
	Index int `json:"index"`
	// Length 為 mention 片段長度。
	Length int `json:"length"`
	// UserID 為被 mention 的使用者 ID。
	UserID string `json:"userId"`
}

// ProcessIncoming 解析 webhook、輸出觀察日誌，並嘗試持久化入站訊息。
func (s consoleWebhookService) ProcessIncoming(body []byte, signature string) {
	var req webhookRequest
	// 第一段：解析 payload，解析失敗僅記錄並返回，避免阻塞 webhook ACK。
	if len(body) > 0 {
		// 目前採「解析失敗僅記錄」策略，避免阻塞 webhook ACK。
		if err := json.Unmarshal(body, &req); err != nil {
			zap.L().Error("line webhook parse failed",
				zap.Bool("signature_present", signature != ""),
				zap.Int("body_bytes", len(body)),
				zap.Error(err),
			)
			return
		}
	}

	// 第二段：逐筆輸出事件日誌，並將 message 事件寫入資料庫。
	for _, event := range req.Events {
		//Save message to database if it's a message event
		if message, ok := adaptLineEventToUnified(event); ok {
			zap.L().Info("line message received",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			)
			s.persistUnifiedMessage(message)
		}
	}

}

// persistUnifiedMessage 將統一訊息格式轉成 channel/channel_message 資料並落庫。
func (s consoleWebhookService) persistUnifiedMessage(message *unifiedmessage.Message) {
	if message == nil {
		return
	}
	if strings.TrimSpace(message.ChannelID) == "" {
		return
	}

	ctx := context.Background()
	mentionedBot := message.MentionsUser(config.Line.BotUserID)
	text := strings.TrimSpace(message.Text)
	if s.classifier != nil && message.IsText() && text != "" {
		_, err := s.classifier.Classify(ctx, messageintent.DefaultPrompt(mentionedBot), text)
		if err != nil {
			zap.L().Debug("webhook classify skipped",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.Bool("mentioned_bot", mentionedBot),
				zap.Error(err),
			)
		}
	}

	// 沒有注入 repository 時，維持純 console 模式；AI 結果已在上方輸出。
	if s.repo == nil {
		return
	}

	senderID := strings.TrimSpace(message.SenderID)
	if senderID == "" {
		senderID = "unknown"
	}

	// sender_name 透過既有 LINE 綁定資料反查 display_name。
	senderName, err := s.repo.ResolveLineDisplayNameByLineUserID(ctx, senderID)
	if err != nil {
		zap.L().Warn("line webhook resolve sender name failed",
			zap.String("sender", senderID),
			zap.Error(err),
		)
	}

	// channel 不存在就建立，存在就沿用。
	ch, err := s.repo.GetOrCreateChannel(ctx, strings.TrimSpace(message.Platform), strings.TrimSpace(message.ChannelID), strings.TrimSpace(message.ChannelType))
	if err != nil {
		zap.L().Error("line webhook persist channel failed",
			zap.String("group_id", message.ChannelID),
			zap.String("type", message.ChannelType),
			zap.Error(err),
		)
		return
	}

	// 寫入 channel_messages，不阻塞 webhook 主流程。
	if _, err := s.repo.SaveReceivedMessage(
		ctx,
		ch.ID,
		senderID,
		senderName,
		message.PlatformMessageID,
		message.ReplyToMsgID,
		message.Text,
		message.MessageType,
		message.PlatformTimestamp,
	); err != nil {
		zap.L().Error("line webhook persist message failed",
			zap.String("channel_id", ch.ID.String()),
			zap.String("message_id", message.PlatformMessageID),
			zap.Error(err),
		)
	}

}

// resolveSender 依優先序挑選可識別的來源 ID。
// 優先 userId，再 fallback 到 groupId、roomId。
func resolveSender(source webhookEventSource) string {
	sender := strings.TrimSpace(source.UserID)
	if sender == "" {
		sender = strings.TrimSpace(source.GroupID)
	}
	if sender == "" {
		sender = strings.TrimSpace(source.RoomID)
	}
	if sender == "" {
		return "unknown"
	}
	return sender
}

// resolveChannelIdentity 根據 source 類型推導 channel key 與 channel type。
func resolveChannelIdentity(source webhookEventSource) (groupID string, channelType string) {
	sourceType := strings.TrimSpace(strings.ToLower(source.Type))
	switch sourceType {
	case "user":
		if userID := strings.TrimSpace(source.UserID); userID != "" {
			return userID, "private"
		}
	case "group":
		if groupID := strings.TrimSpace(source.GroupID); groupID != "" {
			return groupID, "group"
		}
	case "room":
		if roomID := strings.TrimSpace(source.RoomID); roomID != "" {
			return roomID, "group"
		}
	}

	if sender := resolveSender(source); sender != "unknown" {
		return sender, "private"
	}
	return "", ""
}
