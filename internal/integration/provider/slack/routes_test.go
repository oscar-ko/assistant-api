package slack

import (
	"testing"

	"assistant-api/internal/config"
)

func TestSlackLoginStartURL(t *testing.T) {
	oldLoginRedirectURI := config.Slack.LoginRedirectURI
	oldBots := config.Slack.Bots
	defer func() {
		config.Slack.LoginRedirectURI = oldLoginRedirectURI
		config.Slack.Bots = oldBots
	}()

	// slackLoginStartURL 會先解析 DefaultBot；測試必須提供完整 bot config，
	// 否則 strict 多 bot 設定會故意回相對路徑，無法覆蓋絕對 login redirect URL 的組裝邏輯。
	config.Slack.LoginRedirectURI = "https://example.com/slack/login/callback"
	config.Slack.Bots = []config.SlackBotConfig{{
		Name:          "Jarvis",
		AppID:         "A0TEST",
		ClientID:      "client-id",
		ClientSecret:  "client-secret",
		SigningSecret: "signing-secret",
	}}
	got := slackLoginStartURL()
	if got != "https://example.com/slack/login/start?public=1" {
		t.Fatalf("unexpected login start url: %s", got)
	}
}
