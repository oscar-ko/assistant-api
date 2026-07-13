package line

import (
	"strings"

	"assistant-api/internal/integration/unifiedmessage"
)

// adaptLineEventToUnified 將 LINE webhook 事件轉成統一訊息格式。
func adaptLineEventToUnified(event webhookEvent) (*unifiedmessage.Message, bool) {
	if strings.TrimSpace(event.Type) != "message" {
		return nil, false
	}

	channelID, channelType := resolveChannelIdentity(event.Source)
	if channelID == "" {
		return nil, false
	}

	mentions := make([]unifiedmessage.Mention, 0)
	if event.Message.Mention != nil {
		mentions = make([]unifiedmessage.Mention, 0, len(event.Message.Mention.Mentionees))
		for _, mention := range event.Message.Mention.Mentionees {
			if userID := strings.TrimSpace(mention.UserID); userID != "" {
				mentions = append(mentions, unifiedmessage.Mention{UserID: userID})
			}
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

	return msg, true
}
