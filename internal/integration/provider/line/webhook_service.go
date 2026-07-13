package line

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"assistant-api/internal/repository"
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
	repo *repository.ChannelMessageRepo
}

// NewWebhookService 建立預設 webhook service（目前為 console 輸出）。
func NewWebhookService(repo ...*repository.ChannelMessageRepo) WebhookService {
	if len(repo) > 0 {
		return consoleWebhookService{repo: repo[0]}
	}
	return consoleWebhookService{}
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
}

// ProcessIncoming 解析 webhook、輸出觀察日誌，並嘗試持久化入站訊息。
func (s consoleWebhookService) ProcessIncoming(body []byte, signature string) {
	eventCount := 0
	var req webhookRequest
	// 第一段：解析 payload，解析失敗僅記錄並返回，避免阻塞 webhook ACK。
	if len(body) > 0 {
		// 目前採「解析失敗僅記錄」策略，避免阻塞 webhook ACK。
		if err := json.Unmarshal(body, &req); err != nil {
			log.Printf("line webhook parse failed: signature_present=%t, body_bytes=%d, err=%v", signature != "", len(body), err)
			return
		}
		eventCount = len(req.Events)
	}

	// 第二段：逐筆輸出事件日誌，並將 message 事件寫入資料庫。
	for _, event := range req.Events {
		sender := resolveSender(event.Source)
		if event.Type == "message" && event.Message.Type == "text" {
			log.Printf("line message: sender=%s, text=%s", sender, event.Message.Text)
		} else {
			log.Printf("line event: sender=%s, event_type=%s, message_type=%s", sender, event.Type, event.Message.Type)
		}

		s.persistInboundMessage(event)
	}

	// 摘要日誌：便於快速確認簽章是否帶入、事件量與 payload 大小。
	log.Printf("line webhook received: signature_present=%t, body_bytes=%d, events=%d", signature != "", len(body), eventCount)
}

// persistInboundMessage 將單一 message 事件轉成 channel/channel_message 資料並落庫。
func (s consoleWebhookService) persistInboundMessage(event webhookEvent) {
	// 沒有注入 repository 時，維持純 console 模式。
	if s.repo == nil {
		return
	}
	// 目前僅持久化 message 類事件；其他事件只記錄日誌。
	if strings.TrimSpace(event.Type) != "message" {
		return
	}

	// 依 source 決定 channel 主鍵識別（groupId/roomId/userId）與 channel type。
	groupID, channelType := resolveChannelIdentity(event.Source)
	if groupID == "" {
		return
	}

	ctx := context.Background()
	senderID := resolveSender(event.Source)
	// sender_name 透過既有 LINE 綁定資料反查 display_name。
	senderName, err := s.repo.ResolveLineDisplayNameByLineUserID(ctx, senderID)
	if err != nil {
		log.Printf("line webhook resolve sender name failed: sender=%s, err=%v", senderID, err)
	}

	// channel 不存在就建立，存在就沿用。
	ch, err := s.repo.GetOrCreateChannel(ctx, "line", groupID, channelType)
	if err != nil {
		log.Printf("line webhook persist channel failed: group_id=%s, type=%s, err=%v", groupID, channelType, err)
		return
	}

	// 寫入 channel_messages，不阻塞 webhook 主流程。
	if _, err := s.repo.SaveReceivedMessage(
		ctx,
		ch.ID,
		senderID,
		senderName,
		event.Message.ID,
		event.Message.QuotedMessageID,
		event.Message.Text,
		event.Message.Type,
		event.Timestamp,
	); err != nil {
		log.Printf("line webhook persist message failed: channel_id=%s, message_id=%s, err=%v", ch.ID, event.Message.ID, err)
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
