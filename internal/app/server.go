package app

import (
	"context"
	"log"
	"net/http"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	"assistant-api/internal/graph"
	"assistant-api/internal/graph/generated"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"
)

func Start() {
	ctx := context.Background()

	drv, err := entsql.Open(dialect.SQLite, config.Database.SQLiteDSN)
	if err != nil {
		log.Fatalf("failed opening sqlite connection: %v", err)
	}
	defer drv.Close()

	client := ent.NewClient(ent.Driver(drv))
	defer client.Close()

	if err := client.Schema.Create(ctx); err != nil {
		log.Fatalf("failed creating schema resources: %v", err)
	}

	r := gin.Default()
	gqlServer := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: &graph.Resolver{Client: client}}))

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	r.POST(config.GraphQL.QueryPath, gin.WrapH(gqlServer))
	r.GET(config.GraphQL.PlaygroundPath, gin.WrapH(playground.Handler("GraphQL Playground", config.GraphQL.QueryPath)))

	if err := r.Run(":" + config.Server.Port); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
