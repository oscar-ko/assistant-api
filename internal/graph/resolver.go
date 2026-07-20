package graph

import (
	"assistant-api/internal/ent"
	lineprovider "assistant-api/internal/integration/provider/line"
)

// Resolver keeps dependencies for GraphQL resolvers.
type Resolver struct {
	Client               *ent.Client
	LinePushService      lineprovider.PushMessageService
	LinePushInitErr      error
	LineWebhookProcessor lineprovider.WebhookProcessor
}
