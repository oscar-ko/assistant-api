package line

import (
	"encoding/json"
	"strings"

	"assistant-api/internal/integration/unifiedmessage"
)

// adaptLineEventToUnifiedForBot 將 LINE webhook 事件轉成統一訊息格式。
//
// botUserID 由呼叫端（WebhookService）注入，而不是直接讀全域設定：
// 多 bot 情境下，每個 webhook service 實例對應不同的 LINE bot，
// 若在此處直接讀全域 config.Line，會讓 mention 判斷永遠對到同一個 bot，造成多 bot 誤判。
func adaptLineEventToUnifiedForBot(event webhookEvent, botUserID string) (*unifiedmessage.Message, bool, string) {
	if strings.TrimSpace(event.Type) != "message" {
		return nil, false, "event type is not message"
	}

	channelID, channelType := resolveChannelIdentity(event.Source)
	if channelID == "" {
		return nil, false, "unable to resolve channel identity"
	}

	mentions := make([]unifiedmessage.Mention, 0)
	if event.Message.Mention != nil {
		mentions = make([]unifiedmessage.Mention, 0, len(event.Message.Mention.Mentionees))
		for _, mention := range event.Message.Mention.Mentionees {
			userID := strings.TrimSpace(mention.UserID)
			mentionType := strings.TrimSpace(mention.Type)
			if mentionType == "" {
				mentionType = "user"
			}
			if userID == "" && !strings.EqualFold(mentionType, "user") {
				continue
			}
			index := mention.Index
			length := mention.Length
			isBot := userID != "" && strings.TrimSpace(botUserID) != "" && userID == strings.TrimSpace(botUserID)
			identityKind := "user"
			if isBot {
				identityKind = "bot"
			} else if userID == "" {
				identityKind = "unknown"
			}
			raw, _ := json.Marshal(mention)
			mentions = append(mentions, unifiedmessage.Mention{
				UserID:       userID,
				DisplayText:  sliceMentionText(event.Message.Text, index, length),
				Index:        &index,
				Length:       &length,
				Type:         mentionType,
				IdentityKind: identityKind,
				IsBot:        isBot,
				Raw:          string(raw),
			})
		}
	}

	msg := &unifiedmessage.Message{
		Platform:          "line",
		SourceType:        strings.TrimSpace(strings.ToLower(event.Source.Type)),
		ChannelID:         channelID,
		ChannelType:       channelType,
		SenderID:          resolveSender(event.Source),
		PlatformMessageID: strings.TrimSpace(event.Message.ID),
		ReplyToMsgID:      strings.TrimSpace(event.Message.QuotedMessageID),
		MessageType:       strings.TrimSpace(event.Message.Type),
		Text:              strings.TrimSpace(event.Message.Text),
		Mentions:          mentions,
		PlatformTimestamp: event.Timestamp,
	}

	if msg.MessageType == "" {
		msg.MessageType = "text"
	}
	if msg.SenderID == "" {
		msg.SenderID = "unknown"
	}

	return msg, true, ""
}

func sliceMentionText(text string, index int, length int) string {
	if index < 0 || length <= 0 {
		return ""
	}
	runes := []rune(text)
	if index >= len(runes) {
		return ""
	}
	end := index + length
	if end > len(runes) {
		end = len(runes)
	}
	return strings.TrimSpace(string(runes[index:end]))
}
