package slack

import (
	"context"
	"strings"

	"assistant-api/internal/usecase/inbound/messagepersist"
)

// NewSenderNameResolver 提供 Slack provider 的 sender name resolver。
func NewSenderNameResolver(tokenStore slackBotTokenStore) messagepersist.SenderNameResolver {
	return messagepersist.SenderNameResolverFunc(func(ctx context.Context, platform string, platformTenantID string, channelID string, channelType string, senderID string) (string, error) {
		if !strings.EqualFold(strings.TrimSpace(platform), "slack") {
			return "", nil
		}
		return GetUserDisplayNameByID(ctx, tokenStore, platformTenantID, senderID)
	})
}
