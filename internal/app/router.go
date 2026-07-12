package app

import (
	"net/http"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	"assistant-api/internal/graph"
	"assistant-api/internal/graph/generated"
	lineprovider "assistant-api/internal/integration/provider/line"

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
	// 將 Ent Resolver 注入 gqlgen executable schema。
	gqlServer := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: &graph.Resolver{Client: client}}))

	r.POST(config.GraphQL.QueryPath, gin.WrapH(gqlServer))
	r.GET(config.GraphQL.PlaygroundPath, gin.WrapH(playground.Handler("GraphQL Playground", config.GraphQL.QueryPath)))
}
