package slack

import (
	"encoding/json"
	"testing"
)

func TestSlackEventChannelRefAcceptsStringChannel(t *testing.T) {
	// member_joined_channel 這類 lifecycle event 會把 channel 放成純字串 id；
	// 這個測試保護既有事件格式，避免為 channel_joined 支援物件 payload 時破壞原本路徑。
	var event slackEvent
	if err := json.Unmarshal([]byte(`{"type":"member_joined_channel","channel":"C123","user":"U123"}`), &event); err != nil {
		t.Fatalf("unmarshal slack event: %v", err)
	}
	if got := event.Channel.String(); got != "C123" {
		t.Fatalf("unexpected channel id: %s", got)
	}
}

func TestSlackEventChannelRefAcceptsObjectChannel(t *testing.T) {
	// channel_joined 可能帶完整 channel 物件；我們要保留 id 供資料庫建 channel，
	// 也保留 name，讓建立流程可少打一次 conversations.info。
	var event slackEvent
	if err := json.Unmarshal([]byte(`{"type":"channel_joined","channel":{"id":"C123","name":"general"}}`), &event); err != nil {
		t.Fatalf("unmarshal slack event: %v", err)
	}
	if got := event.Channel.String(); got != "C123" {
		t.Fatalf("unexpected channel id: %s", got)
	}
	if got := event.Channel.Name; got != "general" {
		t.Fatalf("unexpected channel name: %s", got)
	}
}

func TestAdaptSlackEventToUnifiedUsesObjectChannelID(t *testing.T) {
	// adapter 不應關心 channel id 來自字串還是物件；只要 webhook 層解析完成，
	// unified message 就應拿到一致的 ChannelID/ChannelType，供 persistence 查找系統 channel。
	event := slackEvent{
		Type:    "message",
		Text:    "hello",
		User:    "U123",
		Channel: slackChannelRef{ID: "C123", Name: "general"},
		TS:      "1710000000.000100",
	}

	message, ok, reason := adaptSlackEventToUnified(event)
	if !ok {
		t.Fatalf("adapt failed: %s", reason)
	}
	if message.ChannelID != "C123" {
		t.Fatalf("unexpected channel id: %s", message.ChannelID)
	}
	if message.ChannelType != "group" {
		t.Fatalf("unexpected channel type: %s", message.ChannelType)
	}
}

func TestSlackMessageLifecycleAction(t *testing.T) {
	// Slack 可能以 message subtype 表達 bot 加入/離開頻道；
	// lifecycle 判斷必須只接受這些 structured subtype，避免用顯示文字做脆弱判斷。
	tests := []struct {
		name       string
		subtype    string
		wantAction string
		wantOK     bool
	}{
		{name: "public channel join", subtype: "channel_join", wantAction: slackLifecycleActionJoin, wantOK: true},
		{name: "private group join", subtype: "group_join", wantAction: slackLifecycleActionJoin, wantOK: true},
		{name: "public channel leave", subtype: "channel_leave", wantAction: slackLifecycleActionLeave, wantOK: true},
		{name: "private group leave", subtype: "group_leave", wantAction: slackLifecycleActionLeave, wantOK: true},
		{name: "regular message subtype", subtype: "message_changed", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAction, gotOK := slackMessageLifecycleAction(tt.subtype)
			if gotOK != tt.wantOK {
				t.Fatalf("unexpected ok: got %v want %v", gotOK, tt.wantOK)
			}
			if gotAction != tt.wantAction {
				t.Fatalf("unexpected action: got %q want %q", gotAction, tt.wantAction)
			}
		})
	}
}
