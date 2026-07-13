package whatsapp

import (
	"context"

	"assistant-api/internal/usecase/inbound/messagepersist"
)

// NewSenderNameResolver 提供 WhatsApp provider 的 sender name resolver。
// 目前先使用 Noop，之後可改接 contact/profile 來源。
func NewSenderNameResolver() messagepersist.SenderNameResolver {
	return messagepersist.SenderNameResolverFunc(func(ctx context.Context, platform string, channelID string, channelType string, senderID string) (string, error) {
		return "", nil
	})
}
