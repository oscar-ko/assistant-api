package line

import (
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// webhookHandler 處理 LINE webhook 的 HTTP 入口，並委派到 service 層。
func webhookHandler(svc WebhookProcessor) gin.HandlerFunc {
	// 若呼叫端未注入實作，使用預設 console 版本，確保路由可直接運作。
	if svc == nil {
		svc = NewWebhookService(nil)
	}

	return func(c *gin.Context) {
		// 讀取原始 body，交由 service 層做解析與後續處理。
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}

		// LINE 會附帶簽章，可在 service 層擴充驗簽邏輯。
		signature := strings.TrimSpace(c.GetHeader("X-Line-Signature"))
		svc.ProcessIncoming(body, signature)

		// webhook 需快速回應，避免 LINE 端重送。
		c.Status(http.StatusOK)
	}
}
