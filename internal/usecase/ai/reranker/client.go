package reranker

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

// RankedDocument 表示 cross-encoder 重排後的單筆結果。
type RankedDocument struct {
	Index    int
	Document string
	Score    float64
}

// Service 抽象出 cross-encoder rerank 能力。
type Service interface {
	Rerank(ctx context.Context, query string, documents []string, topK int) ([]RankedDocument, error)
}

// client 封裝對 cross-encoder reranker service 的 HTTP 呼叫細節。
type client struct {
	baseURL              string
	rerankPath           string
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

// rerankRequest 對應 reranker API 的請求格式。
type rerankRequest struct {
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopK      *int     `json:"top_k,omitempty"`
}

// rerankResponse 對應 reranker API 的回應格式。
type rerankResponse struct {
	Documents []struct {
		Index    int     `json:"index"`
		Document string  `json:"document"`
		Score    float64 `json:"score"`
	} `json:"documents"`
}

const (
	minPositiveDurationMS = 1
)

// NewClient 建立 cross-encoder reranker client。
func NewClient(baseURL string, timeoutSeconds int, rerankPath string, maxAttempts int, retryBackoffMS int, aliveProbeIntervalMS int, aliveProbeTimeoutMS int, aliveSuccessTTLMS int, aliveFailureCooldownMS int) Service {
	// 必要參數：baseURL 不可為空，否則呼叫端應視為未啟用 reranker。
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil
	}
	if timeoutSeconds <= 0 || maxAttempts <= 0 || retryBackoffMS < minPositiveDurationMS || aliveProbeIntervalMS < minPositiveDurationMS || aliveProbeTimeoutMS < minPositiveDurationMS || aliveSuccessTTLMS < minPositiveDurationMS || aliveFailureCooldownMS < minPositiveDurationMS {
		return nil
	}
	rerankPath = strings.TrimSpace(rerankPath)
	if rerankPath == "" {
		return nil
	}
	if !strings.HasPrefix(rerankPath, "/") {
		rerankPath = "/" + rerankPath
	}

	// 將 URL/path 標準化後建立 client，後續重試與逾時由同一實例統一控管。
	c := &client{
		baseURL:              strings.TrimRight(baseURL, "/"),
		rerankPath:           rerankPath,
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

// Rerank 以 query + documents 呼叫 cross-encoder 進行重排。
// 失敗策略與 embedding client 對齊：
// - 可恢復錯誤（網路抖動、429/408/5xx）會重試。
// - 不可恢復錯誤（request 建立失敗、JSON decode 失敗）直接回傳。
// - documents 全為空白時回傳 nil, nil，表示沒有可重排目標，而非錯誤。
func (c *client) Rerank(ctx context.Context, query string, documents []string, topK int) ([]RankedDocument, error) {
	// 防禦式檢查：避免 nil receiver 導致 panic。
	if c == nil {
		return nil, fmt.Errorf("reranker client is not initialized")
	}
	// 先做快速探活，避免服務未啟動時還要等完整請求逾時。
	if aliveErr := c.probeAlive(ctx); aliveErr != nil {
		return nil, fmt.Errorf("reranker service is not alive: %w", aliveErr)
	}
	// query 是重排語意主軸，空字串直接視為請求錯誤。
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	// 預先過濾空白文件，避免把無效文本送去服務端浪費推論成本。
	filteredDocs := make([]string, 0, len(documents))
	for _, doc := range documents {
		trimmed := strings.TrimSpace(doc)
		if trimmed == "" {
			continue
		}
		filteredDocs = append(filteredDocs, trimmed)
	}
	if len(filteredDocs) == 0 {
		return nil, nil
	}

	// top_k 僅在有效值時帶入，避免送出 0 影響服務端預設策略。
	// 這裡使用 *int 搭配 json omitempty：
	// - nil 代表「由服務端自行決定回傳幾筆」
	// - 非 nil 才代表「呼叫端明確要求 top_k」
	// 這可避免 0 被誤解成有效值，污染服務端 fallback 邏輯。
	var topKPtr *int
	if topK > 0 {
		topKCopy := topK
		topKPtr = &topKCopy
	}

	body, err := json.Marshal(rerankRequest{Query: query, Documents: filteredDocs, TopK: topKPtr})
	if err != nil {
		// JSON 組包失敗屬本地端錯誤，不進重試。
		return nil, err
	}

	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		// 每次重試都重新建立 request，避免 body reader 已被消耗。
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+c.rerankPath, bytes.NewReader(body))
		if reqErr != nil {
			// request 無法建立通常是程式或參數問題，直接回傳。
			return nil, reqErr
		}
		req.Header.Set("Content-Type", "application/json")

		resp, doErr := c.httpClient.Do(req)
		if doErr != nil {
			lastErr = fmt.Errorf("reranker request failed: %w", doErr)
			// 只在可恢復錯誤時重試，避免對不可恢復錯誤無限浪費等待。
			if attempt < c.maxAttempts && isRetryableRequestError(doErr) {
				if waitErr := c.waitBackoff(ctx, attempt); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, lastErr
		}

		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			statusErr := fmt.Errorf("reranker service returned status %d", resp.StatusCode)
			_ = resp.Body.Close()
			lastErr = statusErr
			// HTTP 狀態碼重試策略：僅 429/408/5xx 會重試，其餘直接失敗。
			if attempt < c.maxAttempts && isRetryableStatusCode(resp.StatusCode) {
				if waitErr := c.waitBackoff(ctx, attempt); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, statusErr
		}

		var decoded rerankResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&decoded)
		_ = resp.Body.Close()
		if decodeErr != nil {
			// 回應格式錯誤通常代表契約不一致，屬不可恢復錯誤。
			return nil, fmt.Errorf("decode reranker response failed: %w", decodeErr)
		}

		result := make([]RankedDocument, 0, len(decoded.Documents))
		for _, item := range decoded.Documents {
			// 保留服務端的 index/document/score，交由上層決定如何映射原候選。
			result = append(result, RankedDocument{Index: item.Index, Document: item.Document, Score: item.Score})
		}
		return result, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("reranker request failed: exhausted retries")
}

// isRetryableRequestError 判斷網路層錯誤是否值得重試。
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

// isRetryableStatusCode 判斷 HTTP 狀態碼是否值得重試。
func isRetryableStatusCode(status int) bool {
	if status == http.StatusTooManyRequests || status == http.StatusRequestTimeout {
		return true
	}
	return status >= http.StatusInternalServerError
}

// waitBackoff 依重試次數做線性退避，並尊重 ctx cancellation。
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
		return fmt.Errorf("reranker client is not initialized")
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
		return fmt.Errorf("reranker client is not initialized")
	}

	parsed, err := url.Parse(c.baseURL)
	if err != nil {
		c.storeProbeResult(fmt.Errorf("invalid reranker base url: %w", err))
		return fmt.Errorf("invalid reranker base url: %w", err)
	}
	host := strings.TrimSpace(parsed.Host)
	if host == "" {
		err := fmt.Errorf("reranker base url has empty host")
		c.storeProbeResult(err)
		return fmt.Errorf("reranker base url has empty host")
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
		return fmt.Errorf("reranker client is not initialized"), true
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
