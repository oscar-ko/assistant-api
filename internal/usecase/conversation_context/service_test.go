package conversationcontext

import (
	"strings"
	"testing"
	"time"

	"assistant-api/internal/ent"

	"github.com/google/uuid"
)

func TestBuildPromptTextUsesConversationOnly(t *testing.T) {
	messages := []Message{
		{Index: 1, SenderName: "Amy", Text: "我要紅茶 30"},
		{Index: 2, SenderName: "Ben", Text: "我拿拿鐵 65"},
	}

	got := BuildPromptText("幫我整理上面對話中大家飲料要付多少錢", messages)

	for _, want := range []string{"只能使用 conversation_context", "使用者任務", "[1] Amy: 我要紅茶 30", "[2] Ben: 我拿拿鐵 65"} {
		if !strings.Contains(got, want) {
			t.Fatalf("BuildPromptText() missing %q in:\n%s", want, got)
		}
	}
}

func TestBuildPromptTextDistinguishesAssistantRepliesFromGroupDiscussion(t *testing.T) {
	messages := []Message{
		{Index: 1, SenderName: "Amy", Text: "大家有討論到旅遊要去哪裡嗎？"},
		{Index: 2, SenderName: "Bot", SenderID: "bot-1", Text: "可以考慮海邊或山上。"},
	}

	got := BuildPromptText("大家有討論到旅遊要去哪裡嗎？", messages)

	checks := []string{
		"必須區分使用者訊息與助理回覆",
		"助理提出的建議、追問或先前回答，不等於群組成員已討論、同意或決定",
		"不可把它當成大家的共識或討論結果",
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("BuildPromptText() missing assistant/group distinction %q in:\n%s", check, got)
		}
	}
}

func TestSelectPromptMessagesKeepsLatestWithinLimits(t *testing.T) {
	now := time.Now()
	items := []*ent.ChannelMessage{
		{ID: uuid.New(), SenderName: "A", Content: "第一則", MessageType: "text", CreatedAt: now},
		{ID: uuid.New(), SenderName: "B", Content: "第二則", MessageType: "text", CreatedAt: now},
		{ID: uuid.New(), SenderName: "C", Content: "第三則", MessageType: "text", CreatedAt: now},
	}

	got := selectPromptMessages(items, 2, 100, nil, nil)

	if len(got) != 2 {
		t.Fatalf("selectPromptMessages() len = %d, want 2", len(got))
	}
	if got[0].Text != "第二則" || got[1].Text != "第三則" {
		t.Fatalf("selectPromptMessages() = %#v", got)
	}
	if got[0].Index != 1 || got[1].Index != 2 {
		t.Fatalf("message indexes = %d, %d; want 1, 2", got[0].Index, got[1].Index)
	}
}

func TestSelectPromptMessagesSkipsNonTextAndStopsAtCharLimit(t *testing.T) {
	items := []*ent.ChannelMessage{
		{ID: uuid.New(), SenderName: "A", Content: "貼圖", MessageType: "image"},
		{ID: uuid.New(), SenderName: "B", Content: "短句", MessageType: "text"},
		{ID: uuid.New(), SenderName: "C", Content: "超過限制", MessageType: "text"},
	}

	got := selectPromptMessages(items, 3, len([]rune("短句")), nil, nil)

	if len(got) != 1 || got[0].Text != "短句" {
		t.Fatalf("selectPromptMessages() = %#v, want only short text", got)
	}
}

func TestSelectPromptMessagesSkipsExcludedSenderIDsBeforeLimit(t *testing.T) {
	items := []*ent.ChannelMessage{
		{ID: uuid.New(), SenderName: "A", SenderID: "human-1", Content: "第一則", MessageType: "text"},
		{ID: uuid.New(), SenderName: "Bot", SenderID: "bot-1", Content: "指令已執行成功", MessageType: "text"},
		{ID: uuid.New(), SenderName: "B", SenderID: "human-2", Content: "第二則", MessageType: "text"},
	}

	got := selectPromptMessages(items, 2, 100, []string{"bot-1"}, nil)

	if len(got) != 2 {
		t.Fatalf("selectPromptMessages() len = %d, want 2", len(got))
	}
	if got[0].Text != "第一則" || got[1].Text != "第二則" {
		t.Fatalf("selectPromptMessages() = %#v", got)
	}
}

func TestSelectPromptMessagesDeduplicatesPlatformMessageIDs(t *testing.T) {
	items := []*ent.ChannelMessage{
		{ID: uuid.New(), PlatformMessageID: "same-ts", SenderName: "A", Content: "重複一", MessageType: "text"},
		{ID: uuid.New(), PlatformMessageID: "same-ts", SenderName: "A", Content: "重複二", MessageType: "text"},
		{ID: uuid.New(), PlatformMessageID: "source-ts", SenderName: "B", Content: "來源指令", MessageType: "text"},
		{ID: uuid.New(), PlatformMessageID: "other-ts", SenderName: "C", Content: "正常對話", MessageType: "text"},
	}

	got := selectPromptMessages(items, 10, 100, nil, []string{"source-ts"})

	if len(got) != 2 {
		t.Fatalf("selectPromptMessages() len = %d, want 2", len(got))
	}
	if got[0].Text != "重複一" || got[1].Text != "正常對話" {
		t.Fatalf("selectPromptMessages() = %#v", got)
	}
}

func TestSelectPromptMessagesKeepsStructuredMentionsForAIJudgment(t *testing.T) {
	items := []*ent.ChannelMessage{
		{ID: uuid.New(), SenderName: "A", Content: "<@BOT> 幫我整理上面", MessageType: "text", Edges: ent.ChannelMessageEdges{Mentions: []*ent.ChannelMessageMention{{PlatformUserID: "BOT", IsBot: true}}}},
		{ID: uuid.New(), SenderName: "B", Content: "午餐想吃牛肉麵", MessageType: "text"},
	}

	got := selectPromptMessages(items, 10, 100, nil, nil)

	if len(got) != 2 {
		t.Fatalf("selectPromptMessages() len = %d, want 2", len(got))
	}
	if got[0].Text != "<@BOT> 幫我整理上面" || got[1].Text != "午餐想吃牛肉麵" {
		t.Fatalf("selectPromptMessages() = %#v", got)
	}
}
