package main

import (
	"assistant-api/internal/app"
	"assistant-api/internal/config"

	"go.uber.org/zap"
)

// main 為服務進入點：先載入設定，再啟動 HTTP 服務。
func main() {
	config.MustLoad()
	logger, err := zap.NewDevelopment()
	if err == nil {
		zap.ReplaceGlobals(logger)
		defer func() {
			_ = logger.Sync()
		}()
	}
	app.Start()
}
