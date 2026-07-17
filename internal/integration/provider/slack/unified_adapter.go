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
	if strings.TrimSpace(event.Channel) == "" {
		return nil, false, "channel is empty"
	}
	if strings.TrimSpace(event.User) == "" {
		return nil, false, "user is empty"
	}

	channelType := strings.ToLower(strings.TrimSpace(event.ChannelType))
	if channelType == "" {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(event.Channel)), "D") {
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
	for _, match := range slackMentionPattern.FindAllStringSubmatch(strings.TrimSpace(event.Text), -1) {
		if len(match) < 2 {
			continue
		}
		userID := strings.TrimSpace(match[1])
		if userID == "" {
			continue
		}
		mentions = append(mentions, unifiedmessage.Mention{UserID: userID})
	}

	msg := &unifiedmessage.Message{
		Platform:          "slack",
		SourceType:        strings.TrimSpace(channelType),
		ChannelID:         strings.TrimSpace(event.Channel),
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
