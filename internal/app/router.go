package app

import (
	"assistant-api/internal/ent"

	"github.com/gin-gonic/gin"
)

// NewRouter 集中註冊所有 HTTP 路由與 middleware。
func NewRouter(client *ent.Client) *gin.Engine {
	r := gin.Default()

	registerHealthRoutes(r)
	registerGraphQLRoutes(r, client)

	return r
}
