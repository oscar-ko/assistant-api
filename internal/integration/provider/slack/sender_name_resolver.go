package slack

import (
	"assistant-api/internal/usecase/inbound/messagepersist"
)

// NewSenderNameResolver 提供 Slack provider 的 sender name resolver。
// 目前先使用 Noop，之後可改接 Slack profile/user lookup。
func NewSenderNameResolver() messagepersist.SenderNameResolver {
	return messagepersist.NoopSenderNameResolver{}
}
