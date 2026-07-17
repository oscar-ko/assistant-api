package slack

import (
	"testing"

	"assistant-api/internal/config"
)

func TestSlackLoginStartURL(t *testing.T) {
	oldLoginRedirectURI := config.Slack.LoginRedirectURI
	defer func() {
		config.Slack.LoginRedirectURI = oldLoginRedirectURI
	}()

	config.Slack.LoginRedirectURI = "https://example.com/slack/login/callback"
	got := slackLoginStartURL()
	if got != "https://example.com/slack/login/start?public=1" {
		t.Fatalf("unexpected login start url: %s", got)
	}
}
