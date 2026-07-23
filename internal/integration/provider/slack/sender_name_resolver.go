package slack

import (
	"context"
	"strings"

	"assistant-api/internal/config"
	"assistant-api/internal/usecase/inbound/messagepersist"
)

// NewSenderNameResolver 提供 Slack provider 的 sender name resolver。
func NewSenderNameResolver(tokenStore slackBotTokenStore) messagepersist.SenderNameResolver {
	return messagepersist.SenderNameResolverFunc(func(ctx context.Context, platform string, platformTenantID string, channelID string, channelType string, senderID string) (string, error) {
		if !strings.EqualFold(strings.TrimSpace(platform), "slack") {
			return "", nil
		}
		appID := workspaceAppIDFromContext(ctx)
		teamID := strings.TrimSpace(platformTenantID)
		trimmedSenderID := strings.TrimSpace(senderID)
		// Slack bot 的名字不是一般 users.info 的 user profile；bot user id 是 workspace install 產物，
		// 需要先用 app_id + team_id 查出本次 app 的 bot_user_id，再對回 config 裡的 bot name。
		// 這樣 channel_messages.sender_name 才會落成 Jarvis/Thor/Hulk，而不是 null 或錯誤的真人名稱。
		botUserID, err := tokenStore.ResolveWorkspaceBotUserID(ctx, appID, teamID)
		if err != nil {
			return "", err
		}
		if strings.EqualFold(strings.TrimSpace(botUserID), trimmedSenderID) {
			bot, err := config.Slack.BotByAppID(appID)
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(bot.Name), nil
		}
		return GetUserDisplayNameByID(ctx, tokenStore, appID, teamID, trimmedSenderID)
	})
}
