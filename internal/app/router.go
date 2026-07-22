package app

import (
	"fmt"
	"net/http"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	"assistant-api/internal/graph"
	"assistant-api/internal/graph/generated"
	aillminteraction "assistant-api/internal/integration/ai/llm_interaction"
	aitopkfilter "assistant-api/internal/integration/ai/topkfilter"
	lineprovider "assistant-api/internal/integration/provider/line"
	slackprovider "assistant-api/internal/integration/provider/slack"
	"assistant-api/internal/repository"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/gin-gonic/gin"
)

// NewRouter 集中註冊所有 HTTP 路由與 middleware。
func NewRouter(client *ent.Client) *gin.Engine {
	r := gin.Default()
	// 安全預設：不信任任何反向代理，避免 client IP 被偽造。
	_ = r.SetTrustedProxies(nil)

	registerHealthRoutes(r)
	registerGraphQLRoutes(r, client)
	lineprovider.RegisterRoutes(r, client)
	slackprovider.RegisterRoutes(r, client)

	return r
}

// registerHealthRoutes 註冊健康檢查路由，供監控與探針使用。
func registerHealthRoutes(r gin.IRouter) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
}

// registerGraphQLRoutes 註冊 GraphQL 查詢端點與 Playground。
func registerGraphQLRoutes(r gin.IRouter, client *ent.Client) {
	linePushService, linePushInitErr := lineprovider.NewPushMessageService()
	channelMessageRepo := repository.NewChannelMessageRepo(client)
	slackRepo := repository.NewSlackRepo(client)
	actionRouteRepo := repository.NewActionRouteRepo(client)
	filterService, err := aitopkfilter.BuildServiceFromConfig(actionRouteRepo, config.AI)
	if err != nil {
		panic(fmt.Errorf("failed to initialize top-k filter service: %w", err))
	}
	llmInteractionService, err := aillminteraction.BuildServiceFromConfig(config.AI, config.LLMProviders)
	if err != nil {
		panic(err)
	}
	// GraphQL dev helper 會直接呼叫 provider webhook processor 來模擬平台入站事件。
	// 因此這裡除了正式 HTTP route 會用到的 service，也把 LINE / Slack processor 注入 resolver：
	// - LINE 模擬會組 LINE webhook body 後呼叫 LineWebhookProcessor.ProcessIncoming。
	// - Slack 模擬會組 Slack event_callback、產生合法 dev signature，再呼叫 SlackWebhookProcessor。
	// 這樣開發工具測到的是正式 message pipeline，而不是另一條只為測試存在的落庫捷徑。
	lineWebhookProcessor := lineprovider.NewWebhookServiceWithOptions(channelMessageRepo, lineprovider.WebhookServiceOptions{LLMInteraction: llmInteractionService, FollowUpSender: linePushService})
	slackFollowUpSender, _ := slackprovider.NewPushMessageService(slackRepo)
	slackWebhookProcessor := slackprovider.NewWebhookServiceWithOptions(channelMessageRepo, slackRepo, slackprovider.WebhookServiceOptions{
		LLMInteraction: llmInteractionService,
		TopKFilter:     filterService,
		FollowUpSender: slackFollowUpSender,
	})
	// 將 Ent Resolver 注入 gqlgen executable schema。
	gqlServer := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: &graph.Resolver{
		Client:                client,
		LinePushService:       linePushService,
		LinePushInitErr:       linePushInitErr,
		LineWebhookProcessor:  lineWebhookProcessor,
		SlackWebhookProcessor: slackWebhookProcessor,
	}}))

	r.POST(config.GraphQL.QueryPath, gin.WrapH(gqlServer))
	r.GET(config.GraphQL.PlaygroundPath, gin.WrapH(playground.Handler("GraphQL Playground", config.GraphQL.QueryPath)))
}
