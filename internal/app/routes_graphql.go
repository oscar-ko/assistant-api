package app

import (
	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	"assistant-api/internal/graph"
	"assistant-api/internal/graph/generated"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/gin-gonic/gin"
)

// registerGraphQLRoutes 註冊 GraphQL 查詢端點與 Playground。
func registerGraphQLRoutes(r gin.IRouter, client *ent.Client) {
	// 將 Ent Resolver 注入 gqlgen executable schema。
	gqlServer := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: &graph.Resolver{Client: client}}))

	r.POST(config.GraphQL.QueryPath, gin.WrapH(gqlServer))
	r.GET(config.GraphQL.PlaygroundPath, gin.WrapH(playground.Handler("GraphQL Playground", config.GraphQL.QueryPath)))
}
