package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Service 轉換文字為 embedding 向量。
type Service interface {
	GetEmbedding(ctx context.Context, text string) ([]float64, error)
}

type client struct {
	baseURL    string
	embedPath  string
	httpClient *http.Client
}

type request struct {
	Text string `json:"text"`
}

type response struct {
	Embedding []float64 `json:"embedding"`
}

// NewClient 建立 embedding client。
func NewClient(baseURL string, timeoutSeconds int, embedPath string) Service {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	embedPath = strings.TrimSpace(embedPath)
	if embedPath == "" {
		embedPath = "/embed"
	}
	if !strings.HasPrefix(embedPath, "/") {
		embedPath = "/" + embedPath
	}
	return &client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		embedPath:  embedPath,
		httpClient: &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
	}
}

func (c *client) GetEmbedding(ctx context.Context, text string) ([]float64, error) {
	if c == nil {
		return nil, fmt.Errorf("embedding client is not initialized")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	body, err := json.Marshal(request{Text: text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+c.embedPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("embedding service returned status %d", resp.StatusCode)
	}
	var decoded response
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode embedding response failed: %w", err)
	}
	if len(decoded.Embedding) == 0 {
		return nil, fmt.Errorf("embedding response has no embedding field")
	}
	return decoded.Embedding, nil
}
