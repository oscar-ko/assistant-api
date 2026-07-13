package line

import (
	"context"
	"encoding/json"
	"strings"

	"assistant-api/internal/config"
	"assistant-api/internal/integration/messageintent"
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
	repo          *repository.ChannelMessageRepo
	intentService messageintent.Service
}

// NewWebhookService 建立預設 webhook service
func NewWebhookService(repo *repository.ChannelMessageRepo, intentService messageintent.Service) WebhookService {
	return consoleWebhookService{repo: repo, intentService: intentService}
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

// ProcessIncoming 負責 LINE webhook 的整體流程編排。
// 它會先解析原始 payload，再對每則 message 事件做三件事：
// 1. 先把訊息本體印到 console，方便除錯與觀察。
// 2. 交給 AI classifier 取得意圖判讀結果。
// 3. 將訊息寫入資料庫，讓後續查詢與處理可以使用。
func (s consoleWebhookService) ProcessIncoming(body []byte, signature string) {
	var req webhookRequest
	// 第一段：先將 webhook body 轉成結構化資料。
	// 如果解析失敗，只記錄錯誤並直接返回，避免 webhook ACK 被卡住。
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			zap.L().Error("line webhook parse failed",
				zap.Bool("signature_present", signature != ""),
				zap.Int("body_bytes", len(body)),
				zap.Error(err),
			)
			return
		}
	}

	// 第二段：逐筆掃描事件陣列，只處理 message 事件。
	for _, event := range req.Events {
		// 非 message 事件直接略過；只有文字/圖片等訊息才需要進一步處理。
		if message, ok := adaptLineEventToUnified(event); ok {
			// 先把原始訊息資訊印出來，方便在 console 直接看到來了什麼內容。
			zap.L().Info("line message received",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("text", strings.TrimSpace(message.Text)),
			)

			mentionedBot := message.MentionsUser(config.Line.BotUserID)

			// 再交給共用 message intent service 做意圖判讀。
			var classification *messageintent.Classification
			var err error
			if s.intentService != nil {
				classification, err = s.intentService.ClassifyMessage(context.Background(), message, mentionedBot)
			}
			if err != nil {
				// AI 服務失敗時只記 debug，不阻斷 webhook 主流程。
				zap.L().Debug("webhook classify skipped",
					zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
					zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
					zap.Bool("mentioned_bot", mentionedBot),
					zap.Error(err),
				)
			} else if classification != nil {
				// AI 有正常回傳時，把判讀結果印到 console，方便觀察模型輸出。
				zap.L().Info("webhook classified",
					zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
					zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
					zap.Bool("mentioned_bot", mentionedBot),
					zap.String("intent_label", classification.IntentLabel),
					zap.Float64("confidence", classification.Confidence),
					zap.String("reason", strings.TrimSpace(classification.Reason)),
				)
			}

			// 最後才把訊息落庫，確保即使 AI 失敗，訊息本身仍然會被保存。
			s.persistUnifiedMessage(message)
		}
	}

}

// persistUnifiedMessage 負責把統一訊息格式寫入 channel 與 channel_message。
// 這個函式只做資料持久化，不應再混入 AI 判讀或其他額外業務邏輯。
func (s consoleWebhookService) persistUnifiedMessage(message *unifiedmessage.Message) {
	// 沒有訊息或 channel id 時直接返回，避免寫入無效資料。
	if message == nil {
		return
	}
	if strings.TrimSpace(message.ChannelID) == "" {
		return
	}

	// 沒有注入 repository 時，維持純 console 模式；這時只保留上方的 console 輸出。
	if s.repo == nil {
		return
	}

	// 這裡開始進入資料庫寫入流程，先準備共用 context。
	ctx := context.Background()
	// senderID 以 sender 優先，若沒有則使用 unknown，確保欄位有值可追蹤。
	senderID := strings.TrimSpace(message.SenderID)
	if senderID == "" {
		senderID = "unknown"
	}

	// sender_name 透過既有 LINE 綁定資料反查 display_name，讓資料庫內有可讀名稱。
	senderName, err := s.repo.ResolveLineDisplayNameByLineUserID(ctx, senderID)
	if err != nil {
		zap.L().Warn("line webhook resolve sender name failed",
			zap.String("sender", senderID),
			zap.Error(err),
		)
	}

	// channel 不存在就建立，存在就沿用，確保訊息有對應的 channel 可掛載。
	ch, err := s.repo.GetOrCreateChannel(ctx, strings.TrimSpace(message.Platform), strings.TrimSpace(message.ChannelID), strings.TrimSpace(message.ChannelType))
	if err != nil {
		zap.L().Error("line webhook persist channel failed",
			zap.String("group_id", message.ChannelID),
			zap.String("type", message.ChannelType),
			zap.Error(err),
		)
		return
	}

	// 將訊息寫入 channel_messages；若失敗只記錄錯誤，不阻塞 webhook 主流程。
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
// 優先 userId，再 fallback 到 groupId、roomId，最後才回傳 unknown。
func resolveSender(source webhookEventSource) string {
	// 先嘗試最精準的一對一使用者 ID。
	sender := strings.TrimSpace(source.UserID)
	if sender == "" {
		// 沒有 userId 時，退回 groupId。
		sender = strings.TrimSpace(source.GroupID)
	}
	if sender == "" {
		// 再不行就退回 roomId。
		sender = strings.TrimSpace(source.RoomID)
	}
	if sender == "" {
		// 三種來源都沒有時，統一標成 unknown。
		return "unknown"
	}
	return sender
}

// resolveChannelIdentity 根據 LINE event source 類型推導 channel key 與 channel type。
// 這裡的回傳值會拿去建立或查找對應的 channel。
func resolveChannelIdentity(source webhookEventSource) (groupID string, channelType string) {
	// 先把 source type 正規化，避免大小寫或空白影響判斷。
	sourceType := strings.TrimSpace(strings.ToLower(source.Type))
	switch sourceType {
	case "user":
		// 私聊情境直接用 userId 當 channel key。
		if userID := strings.TrimSpace(source.UserID); userID != "" {
			return userID, "private"
		}
	case "group":
		// 群組情境直接用 groupId 當 channel key。
		if groupID := strings.TrimSpace(source.GroupID); groupID != "" {
			return groupID, "group"
		}
	case "room":
		// 房間情境也視為 group 類型來處理。
		if roomID := strings.TrimSpace(source.RoomID); roomID != "" {
			return roomID, "group"
		}
	}

	// 如果 source type 不完整，就退回使用可辨識的 sender 當 channel key。
	if sender := resolveSender(source); sender != "unknown" {
		return sender, "private"
	}
	// 真的都找不到時，回傳空值讓呼叫端決定略過。
	return "", ""
}
