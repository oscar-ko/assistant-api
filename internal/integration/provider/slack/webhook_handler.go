package slack

import (
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func webhookHandler(svc WebhookProcessor) gin.HandlerFunc {
	if svc == nil {
		svc = NewWebhookService(nil)
	}

	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}

		signature := strings.TrimSpace(c.GetHeader("X-Slack-Signature"))
		timestamp := strings.TrimSpace(c.GetHeader("X-Slack-Request-Timestamp"))
		if err := svc.ValidateSignature(timestamp, signature, body); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			return
		}

		challenge, err := svc.ProcessIncoming(body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if strings.TrimSpace(challenge) != "" {
			c.Header("Content-Type", "text/plain; charset=utf-8")
			c.String(http.StatusOK, challenge)
			return
		}

		c.Status(http.StatusOK)
	}
}
