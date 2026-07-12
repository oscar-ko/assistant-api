package app

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// registerHealthRoutes 註冊健康檢查路由，供監控與探針使用。
func registerHealthRoutes(r gin.IRouter) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
}
