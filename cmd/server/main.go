package main

import (
	"assistant-api/internal/app"
	"assistant-api/internal/config"
	appLogger "assistant-api/internal/pkg/log"
)

// main 為服務進入點：先載入設定，再啟動 HTTP 服務。
func main() {
	config.MustLoad()
	if err := appLogger.InitLogger(); err != nil {
		panic(err)
	}
	app.Start()
}
