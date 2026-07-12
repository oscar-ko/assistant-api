package line

import (
	"encoding/json"
	"log"
	"strings"
)

// WebhookService 定義 LINE webhook 的處理介面，方便注入不同實作。
type WebhookService interface {
	// ProcessIncoming 接收原始 webhook body 與簽章字串，執行後續處理。
	// 目前預設實作只做解析與 console 輸出，未做簽章驗證與持久化。
	ProcessIncoming(body []byte, signature string)
}

// consoleWebhookService 是最小可用的預設實作：
// 僅解析事件並輸出到 console，便於開發階段觀察 webhook 是否正常進站。
type consoleWebhookService struct{}

// NewWebhookService 建立預設 webhook service（目前為 console 輸出）。
func NewWebhookService() WebhookService {
	return consoleWebhookService{}
}

type webhookRequest struct {
	// Events 為 LINE 一次 webhook payload 內包含的事件陣列。
	Events []webhookEvent `json:"events"`
}

type webhookEvent struct {
	// Type 事件類型，例如 message、follow、unfollow 等。
	Type string `json:"type"`
	// Source 訊息來源（個人、群組、聊天室）資訊。
	Source webhookEventSource `json:"source"`
	// Message 僅在 message 事件時有意義；其他事件可能為零值。
	Message webhookMessage `json:"message"`
}

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

type webhookMessage struct {
	// ID 為 LINE message ID，可用於後續追蹤或回覆流程。
	ID string `json:"id"`
	// Type 訊息型別，例如 text、image、audio。
	Type string `json:"type"`
	// Text 僅在 text 訊息時有內容。
	Text string `json:"text"`
}

// ProcessIncoming 處理 LINE webhook payload，先以 console log 呈現收到的訊息。
func (consoleWebhookService) ProcessIncoming(body []byte, signature string) {
	eventCount := 0
	var req webhookRequest
	if len(body) > 0 {
		// 目前採「解析失敗僅記錄」策略，避免阻塞 webhook ACK。
		if err := json.Unmarshal(body, &req); err != nil {
			log.Printf("line webhook parse failed: signature_present=%t, body_bytes=%d, err=%v", signature != "", len(body), err)
			return
		}
		eventCount = len(req.Events)
	}

	for _, event := range req.Events {
		sender := resolveSender(event.Source)
		// 先聚焦文字訊息，方便驗證接收鏈路；其他事件先輸出基本資訊。
		if event.Type == "message" && event.Message.Type == "text" {
			log.Printf("line message: sender=%s, text=%s", sender, event.Message.Text)
			continue
		}
		log.Printf("line event: sender=%s, event_type=%s, message_type=%s", sender, event.Type, event.Message.Type)
	}

	// 摘要日誌：便於快速確認簽章是否帶入、事件量與 payload 大小。
	log.Printf("line webhook received: signature_present=%t, body_bytes=%d, events=%d", signature != "", len(body), eventCount)
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
