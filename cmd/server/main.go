package main

import (
	"assistant-api/internal/app"
	"assistant-api/internal/config"
)

func main() {
	config.MustLoad()
	app.Start()
}
