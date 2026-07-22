package slack

import (
	"regexp"
	"strconv"
	"strings"

	"assistant-api/internal/integration/unifiedmessage"
)

var slackMentionPattern = regexp.MustCompile(`<@([A-Z0-9]+)>`)

// adaptSlackEventToUnified 將 Slack event 轉成平台共用訊息格式。
func adaptSlackEventToUnified(event slackEvent) (*unifiedmessage.Message, bool, string) {
	if !strings.EqualFold(strings.TrimSpace(event.Type), "message") {
		return nil, false, "event type is not message"
	}
	// channel 可能已由 webhook service 解析自字串或物件 payload；
	// adapter 後續只依賴正規化後的 channel id，避免不同 Slack event 形狀滲入共用訊息模型。
	channelID := event.Channel.String()
	if channelID == "" {
		return nil, false, "channel is empty"
	}
	if strings.TrimSpace(event.User) == "" {
		return nil, false, "user is empty"
	}

	channelType := strings.ToLower(strings.TrimSpace(event.ChannelType))
	if channelType == "" {
		// Slack DM channel id 以 D 開頭；缺少 channel_type 的事件仍可用這個平台穩定規則分辨 private/group。
		if strings.HasPrefix(strings.ToUpper(channelID), "D") {
			channelType = "private"
		} else {
			channelType = "group"
		}
	} else if channelType == "im" {
		channelType = "private"
	} else {
		channelType = "group"
	}

	replyTo := ""
	threadTS := strings.TrimSpace(event.ThreadTS)
	ts := strings.TrimSpace(event.TS)
	if threadTS != "" && threadTS != ts {
		replyTo = threadTS
	}

	mentions := make([]unifiedmessage.Mention, 0)
	for _, match := range slackMentionPattern.FindAllStringSubmatchIndex(strings.TrimSpace(event.Text), -1) {
		if len(match) < 2 {
			continue
		}
		text := strings.TrimSpace(event.Text)
		userID := strings.TrimSpace(text[match[2]:match[3]])
		if userID == "" {
			continue
		}
		index := len([]rune(text[:match[0]]))
		length := len([]rune(text[match[0]:match[1]]))
		mentions = append(mentions, unifiedmessage.Mention{UserID: userID, DisplayText: text[match[0]:match[1]], Index: &index, Length: &length, Type: "user", IdentityKind: "user"})
	}

	msg := &unifiedmessage.Message{
		Platform:          "slack",
		SourceType:        strings.TrimSpace(channelType),
		ChannelID:         channelID,
		ChannelType:       channelType,
		SenderID:          strings.TrimSpace(event.User),
		PlatformMessageID: ts,
		ReplyToMsgID:      replyTo,
		MessageType:       "text",
		Text:              strings.TrimSpace(event.Text),
		Mentions:          mentions,
		PlatformTimestamp: parseSlackTimestampMillis(ts),
	}

	if msg.PlatformMessageID == "" {
		msg.PlatformMessageID = strings.TrimSpace(event.ClientMsgID)
	}
	if msg.SenderID == "" {
		msg.SenderID = "unknown"
	}
	return msg, true, ""
}

func parseSlackTimestampMillis(value string) int64 {
	ts := strings.TrimSpace(value)
	if ts == "" {
		return 0
	}
	seconds, err := strconv.ParseFloat(ts, 64)
	if err != nil {
		return 0
	}
	return int64(seconds * 1000)
}

func resolveSlackReplyRef(event slackEvent) string {
	threadTS := strings.TrimSpace(event.ThreadTS)
	if threadTS != "" {
		return threadTS
	}
	return strings.TrimSpace(event.TS)
}
