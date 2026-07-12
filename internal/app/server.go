package app

import (
	"context"
	"log"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

// Start 負責組裝基礎資源並啟動 HTTP 伺服器。
func Start() {
	ctx := context.Background()

	// 建立 SQLite 連線驅動，供 Ent 使用。
	drv, err := entsql.Open(dialect.SQLite, config.Database.SQLiteDSN)
	if err != nil {
		log.Fatalf("failed opening sqlite connection: %v", err)
	}
	defer drv.Close()

	// 建立 Ent Client 並確保資料表 schema 已同步。
	client := ent.NewClient(ent.Driver(drv))
	defer client.Close()

	if err := client.Schema.Create(ctx); err != nil {
		log.Fatalf("failed creating schema resources: %v", err)
	}

	// 組裝路由後啟動服務。
	r := NewRouter(client)

	if err := r.Run(":" + config.Server.Port); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
