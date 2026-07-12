package main

import (
	"assistant-api/internal/app"
	"assistant-api/internal/config"
)

// main 為服務進入點：先載入設定，再啟動 HTTP 服務。
func main() {
	config.MustLoad()
	app.Start()
}
