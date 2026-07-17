package slack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const slackWebhookRetryDedupTTL = 2 * time.Minute

var slackWebhookRetryDeduper = newSlackWebhookRetryDeduper(slackWebhookRetryDedupTTL)

type slackWebhookRetryDeduperStore struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

func newSlackWebhookRetryDeduper(ttl time.Duration) *slackWebhookRetryDeduperStore {
	return &slackWebhookRetryDeduperStore{
		seen: make(map[string]time.Time),
		ttl:  ttl,
	}
}

func (d *slackWebhookRetryDeduperStore) mark(body []byte) bool {
	if d == nil || len(body) == 0 {
		return true
	}
	now := time.Now()
	keyBytes := sha256.Sum256(body)
	key := hex.EncodeToString(keyBytes[:])

	d.mu.Lock()
	defer d.mu.Unlock()

	for seenKey, expiresAt := range d.seen {
		if now.After(expiresAt) {
			delete(d.seen, seenKey)
		}
	}

	if expiresAt, ok := d.seen[key]; ok && now.Before(expiresAt) {
		return false
	}
	d.seen[key] = now.Add(d.ttl)
	return true
}

func webhookHandler(svc WebhookProcessor) gin.HandlerFunc {
	if svc == nil {
		svc = NewWebhookService(nil, nil)
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
			zap.L().Warn("slack webhook signature validation failed",
				zap.String("reason", err.Error()),
				zap.Bool("has_signature_header", signature != ""),
				zap.Bool("has_timestamp_header", timestamp != ""),
				zap.String("timestamp", timestamp),
			)
			c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			return
		}

		var req slackWebhookRequest
		if err := json.Unmarshal(body, &req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid slack webhook payload"})
			return
		}

		if strings.EqualFold(strings.TrimSpace(req.Type), "event_callback") {
			if !slackWebhookRetryDeduper.mark(body) {
				eventType := ""
				messageID := ""
				if req.Event != nil {
					eventType = strings.TrimSpace(req.Event.Type)
					messageID = strings.TrimSpace(req.Event.TS)
				}
				zap.L().Info("duplicate slack webhook event skipped",
					zap.String("event_type", eventType),
					zap.String("message_id", messageID),
				)
				c.Status(http.StatusOK)
				return
			}

			bodyCopy := append([]byte(nil), body...)
			go func() {
				if _, err := svc.ProcessIncoming(bodyCopy); err != nil {
					zap.L().Warn("slack webhook async processing failed", zap.Error(err))
				}
			}()
			c.Status(http.StatusOK)
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
