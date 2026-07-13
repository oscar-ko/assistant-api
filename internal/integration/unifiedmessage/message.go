package unifiedmessage

import "strings"

// Mention 表示訊息中被提及的對象。
type Mention struct {
	UserID string
}

// Message 是跨通訊平台統一後的訊息格式。
// 後續 AI 分析、規則判斷、訊息鏈查詢都應以這個格式為輸入。
type Message struct {
	Platform                 string
	SourceType               string
	ChannelID                string
	ChannelType              string
	SenderID                 string
	SenderName               string
	PlatformMessageID        string
	ReplyToPlatformMessageID string
	MessageType              string
	Text                     string
	Mentions                 []Mention
	PlatformTimestamp        int64
}

// IsText 回傳是否為文字訊息。
func (m Message) IsText() bool {
	return strings.EqualFold(strings.TrimSpace(m.MessageType), "text")
}

// MentionsUser 回傳訊息是否提及指定使用者。
func (m Message) MentionsUser(userID string) bool {
	target := strings.TrimSpace(userID)
	if target == "" {
		return false
	}
	for _, mention := range m.Mentions {
		if strings.TrimSpace(mention.UserID) == target {
			return true
		}
	}
	return false
}
