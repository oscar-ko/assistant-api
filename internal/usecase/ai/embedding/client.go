package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Service 轉換文字為 embedding 向量。
type Service interface {
	GetEmbedding(ctx context.Context, text string) ([]float64, error)
}

type client struct {
	baseURL              string
	embedPath            string
	httpClient           *http.Client
	maxAttempts          int
	backoffBase          time.Duration
	aliveProbeInterval   time.Duration
	aliveProbeTimeout    time.Duration
	aliveSuccessTTL      time.Duration
	aliveFailureCooldown time.Duration

	probeMu      sync.Mutex
	lastProbeAt  time.Time
	lastProbeErr error
}

type request struct {
	Text string `json:"text"`
}

type response struct {
	Embedding []float64 `json:"embedding"`
}

const (
	minPositiveDurationMS = 1
)

// NewClient 建立 embedding client。
//
// 參數說明：
// - baseURL: embedding service 的主機位址，例如 http://127.0.0.1:9000
// - timeoutSeconds: 單次 HTTP 請求逾時秒數
// - embedPath: embedding API 路徑
// - maxAttempts: 單次 embedding 最多重試幾次（包含第一次）
// - retryBackoffMS: 每次重試的基礎等待毫秒數，會隨 attempt 線性放大
func NewClient(baseURL string, timeoutSeconds int, embedPath string, maxAttempts int, retryBackoffMS int, aliveProbeIntervalMS int, aliveProbeTimeoutMS int, aliveSuccessTTLMS int, aliveFailureCooldownMS int) Service {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil
	}
	if timeoutSeconds <= 0 || maxAttempts <= 0 || retryBackoffMS < minPositiveDurationMS || aliveProbeIntervalMS < minPositiveDurationMS || aliveProbeTimeoutMS < minPositiveDurationMS || aliveSuccessTTLMS < minPositiveDurationMS || aliveFailureCooldownMS < minPositiveDurationMS {
		return nil
	}
	embedPath = strings.TrimSpace(embedPath)
	if embedPath == "" {
		return nil
	}
	if !strings.HasPrefix(embedPath, "/") {
		embedPath = "/" + embedPath
	}
	c := &client{
		baseURL:              strings.TrimRight(baseURL, "/"),
		embedPath:            embedPath,
		httpClient:           &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
		maxAttempts:          maxAttempts,
		backoffBase:          time.Duration(retryBackoffMS) * time.Millisecond,
		aliveProbeInterval:   time.Duration(aliveProbeIntervalMS) * time.Millisecond,
		aliveProbeTimeout:    time.Duration(aliveProbeTimeoutMS) * time.Millisecond,
		aliveSuccessTTL:      time.Duration(aliveSuccessTTLMS) * time.Millisecond,
		aliveFailureCooldown: time.Duration(aliveFailureCooldownMS) * time.Millisecond,
	}
	c.startBackgroundProbeLoop()
	return c
}

// GetEmbedding 將單一文字送往 embedding service，並在可恢復錯誤時重試。
//
// 重試策略：
// - 連線拒絕、逾時、連線重置等網路錯誤 => 會重試
// - 429 / 408 / 5xx => 會重試
// - JSON decode 錯誤或回傳 embedding 為空 => 視為不可恢復，直接失敗
func (c *client) GetEmbedding(ctx context.Context, text string) ([]float64, error) {
	if c == nil {
		return nil, fmt.Errorf("embedding client is not initialized")
	}
	// 先做快速探活，避免服務未啟動時還要等完整請求逾時。
	if aliveErr := c.probeAlive(ctx); aliveErr != nil {
		return nil, fmt.Errorf("embedding service is not alive: %w", aliveErr)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	body, err := json.Marshal(request{Text: text})
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+c.embedPath, bytes.NewReader(body))
		if reqErr != nil {
			return nil, reqErr
		}
		req.Header.Set("Content-Type", "application/json")

		resp, doErr := c.httpClient.Do(req)
		if doErr != nil {
			lastErr = fmt.Errorf("embedding request failed: %w", doErr)
			if attempt < c.maxAttempts && isRetryableRequestError(doErr) {
				if waitErr := c.waitBackoff(ctx, attempt); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, lastErr
		}

		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			statusErr := fmt.Errorf("embedding service returned status %d", resp.StatusCode)
			_ = resp.Body.Close()
			lastErr = statusErr
			if attempt < c.maxAttempts && isRetryableStatusCode(resp.StatusCode) {
				if waitErr := c.waitBackoff(ctx, attempt); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, statusErr
		}

		var decoded response
		decodeErr := json.NewDecoder(resp.Body).Decode(&decoded)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode embedding response failed: %w", decodeErr)
		}
		if len(decoded.Embedding) == 0 {
			return nil, fmt.Errorf("embedding response has no embedding field")
		}
		return decoded.Embedding, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("embedding request failed: exhausted retries")
}

func isRetryableRequestError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "connection refused") || strings.Contains(lower, "actively refused") {
		return true
	}
	if strings.Contains(lower, "connection reset") || strings.Contains(lower, "broken pipe") {
		return true
	}
	return false
}

func isRetryableStatusCode(status int) bool {
	if status == http.StatusTooManyRequests || status == http.StatusRequestTimeout {
		return true
	}
	return status >= http.StatusInternalServerError
}

// waitBackoff 根據目前重試次數進行線性退避等待。
// 例如 backoffBase=500ms 時：
// attempt=1 -> 500ms, attempt=2 -> 1000ms, attempt=3 -> 1500ms
func (c *client) waitBackoff(ctx context.Context, attempt int) error {
	if attempt <= 0 {
		attempt = 1
	}
	wait := time.Duration(attempt) * c.backoffBase
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// probeAlive 透過 TCP 快速探測服務是否可連線，避免等待長 timeout 才失敗。
func (c *client) probeAlive(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("embedding client is not initialized")
	}
	if cachedErr, ok := c.getCachedProbeResult(); ok {
		return cachedErr
	}
	// 背景探活尚未產出狀態時，首請求做一次同步探測作為保底。
	return c.probeAliveOnce(ctx)
}

func (c *client) startBackgroundProbeLoop() {
	if c == nil {
		return
	}
	go func() {
		c.probeAliveOnce(context.Background())
		ticker := time.NewTicker(c.aliveProbeInterval)
		defer ticker.Stop()
		for range ticker.C {
			c.probeAliveOnce(context.Background())
		}
	}()
}

func (c *client) probeAliveOnce(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("embedding client is not initialized")
	}

	parsed, err := url.Parse(c.baseURL)
	if err != nil {
		c.storeProbeResult(fmt.Errorf("invalid embedding base url: %w", err))
		return fmt.Errorf("invalid embedding base url: %w", err)
	}
	host := strings.TrimSpace(parsed.Host)
	if host == "" {
		err := fmt.Errorf("embedding base url has empty host")
		c.storeProbeResult(err)
		return fmt.Errorf("embedding base url has empty host")
	}
	probeCtx, cancel := context.WithTimeout(ctx, c.aliveProbeTimeout)
	defer cancel()
	dialer := &net.Dialer{}
	conn, dialErr := dialer.DialContext(probeCtx, "tcp", host)
	if dialErr != nil {
		c.storeProbeResult(dialErr)
		return dialErr
	}
	_ = conn.Close()
	c.storeProbeResult(nil)
	return nil
}

// getCachedProbeResult 回傳快取探活結果。
// ok=true 代表可直接使用快取；ok=false 代表需重新探活。
func (c *client) getCachedProbeResult() (error, bool) {
	if c == nil {
		return fmt.Errorf("embedding client is not initialized"), true
	}
	now := time.Now()
	c.probeMu.Lock()
	defer c.probeMu.Unlock()
	if c.lastProbeAt.IsZero() {
		return nil, false
	}
	age := now.Sub(c.lastProbeAt)
	if c.lastProbeErr == nil {
		if age <= c.aliveSuccessTTL {
			return nil, true
		}
		return nil, false
	}
	if age <= c.aliveFailureCooldown {
		return c.lastProbeErr, true
	}
	return nil, false
}

// storeProbeResult 記錄最近一次探活結果，供下一次快速判斷。
func (c *client) storeProbeResult(err error) {
	if c == nil {
		return
	}
	c.probeMu.Lock()
	defer c.probeMu.Unlock()
	c.lastProbeAt = time.Now()
	c.lastProbeErr = err
}
