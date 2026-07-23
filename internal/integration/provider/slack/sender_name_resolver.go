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
