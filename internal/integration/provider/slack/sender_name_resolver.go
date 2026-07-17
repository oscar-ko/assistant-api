package slack

import (
	"context"
	"strings"

	"assistant-api/internal/usecase/inbound/messagepersist"
)

// NewSenderNameResolver 提供 Slack provider 的 sender name resolver。
// 目前先使用 Noop，之後可改接 Slack profile/user lookup。
func NewSenderNameResolver() messagepersist.SenderNameResolver {
	return messagepersist.SenderNameResolverFunc(func(ctx context.Context, platform string, channelID string, channelType string, senderID string) (string, error) {
		if !strings.EqualFold(strings.TrimSpace(platform), "slack") {
			return "", nil
		}
		return GetUserDisplayNameByID(ctx, senderID)
	})
}
