package app

import (
	"context"
	"fmt"
	"log"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Start 負責組裝基礎資源並啟動 HTTP 伺服器。
func Start() {
	ctx := context.Background()

	// 使用 PostgreSQL 建立資料庫連線。
	drv, err := openDBDriver()
	if err != nil {
		log.Fatalf("failed opening database connection: %v", err)
	}
	defer drv.Close()

	// 建立 Ent Client 並確保資料表 schema 已同步。
	client := ent.NewClient(ent.Driver(drv))
	defer client.Close()

	if config.Database.AutoSchemaCreate {
		if err := client.Schema.Create(ctx); err != nil {
			log.Fatalf("failed creating schema resources: %v", err)
		}
	}

	// 組裝路由後啟動服務。
	r := NewRouter(client)

	if err := r.Run(":" + config.Server.Port); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

func openDBDriver() (*entsql.Driver, error) {
	dsn := config.PostgreSQL.GetDSN()
	if dsn == "" {
		return nil, fmt.Errorf("postgresql dsn is empty, please check postgresql config")
	}
	return entsql.Open(dialect.Postgres, dsn)
}
