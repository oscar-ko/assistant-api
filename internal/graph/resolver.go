package graph

import (
	"assistant-api/internal/ent"
	lineprovider "assistant-api/internal/integration/provider/line"
	slackprovider "assistant-api/internal/integration/provider/slack"
)

// Resolver keeps dependencies for GraphQL resolvers.
//
// 這裡注入 provider webhook processor 是為了 dev-only GraphQL mutation：
// simulateTodoConversation 需要把模擬 payload 丟進正式 LINE / Slack webhook pipeline，
// 讓開發時驗證的 persistence、command gate、realtime Todo analyzer 與正式 webhook 流程一致。
// 不要在 resolver 內另寫一套直接 SaveReceivedMessage 的捷徑，否則測試工具會繞過 provider adapter 與 runtime context。
type Resolver struct {
	Client                *ent.Client
	LinePushService       lineprovider.PushMessageService
	LinePushInitErr       error
	LineWebhookProcessor  lineprovider.WebhookProcessor
	SlackPushService      slackprovider.PushMessageService
	SlackWebhookProcessor slackprovider.WebhookProcessor
}
