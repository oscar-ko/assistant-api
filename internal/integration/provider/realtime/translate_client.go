package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Translator defines translation capability.
type Translator interface {
	Translate(ctx context.Context, text string, targetLocales []string) (map[string]string, error)
}

// LocalTranslateClient calls a local JSON-contract translation endpoint.
type LocalTranslateClient struct {
	baseURL      string
	endpointPath string
	client       *http.Client
}

// NewLocalTranslateClient builds the default /translate local client.
func NewLocalTranslateClient(baseURL string, timeoutSeconds int) *LocalTranslateClient {
	return NewLocalContractTranslateClient(baseURL, timeoutSeconds, "/translate")
}

// NewLocalContractTranslateClient builds a local contract client with custom endpoint path.
func NewLocalContractTranslateClient(baseURL string, timeoutSeconds int, endpointPath string) *LocalTranslateClient {
	trimmed := strings.TrimSpace(baseURL)
	if timeoutSeconds <= 0 {
		timeoutSeconds = 90
	}
	path := strings.TrimSpace(endpointPath)
	if path == "" {
		path = "/translate"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return &LocalTranslateClient{
		baseURL:      strings.TrimRight(trimmed, "/"),
		endpointPath: path,
		client:       &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
	}
}

// Translate performs one request to translate text into multiple locales.
func (c *LocalTranslateClient) Translate(ctx context.Context, text string, targetLocales []string) (map[string]string, error) {
	if c == nil {
		return nil, fmt.Errorf("translate client is not initialized")
	}
	if strings.TrimSpace(c.baseURL) == "" {
		return nil, fmt.Errorf("llm interaction service url is empty")
	}
	inputText := strings.TrimSpace(text)
	if inputText == "" {
		return nil, fmt.Errorf("text is required")
	}
	locales := dedupeLocales(targetLocales)
	if len(locales) == 0 {
		return nil, fmt.Errorf("target locales are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	type translateRequest struct {
		Prompt        string   `json:"prompt"`
		Text          string   `json:"text"`
		TargetLocales []string `json:"target_locales"`
	}
	type translateResponse struct {
		SchemaVersion string            `json:"schema_version"`
		Translations  map[string]string `json:"translations"`
	}

	endpoint := c.baseURL + c.endpointPath
	prompt := "You are a translation engine. Translate the message content into all target locales. Return strict JSON only with this schema: {\"schema_version\":\"v1\",\"translations\":{\"<locale>\":\"<translation>\"}}. The translations object keys must exactly match target locales. Do not include extra keys or explanations."
	payload, err := json.Marshal(translateRequest{Prompt: prompt, Text: inputText, TargetLocales: locales})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("translate endpoint status %d body: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var decoded translateResponse
	if err := json.Unmarshal(bodyBytes, &decoded); err != nil {
		return nil, err
	}
	if len(decoded.Translations) == 0 {
		return nil, fmt.Errorf("translate endpoint returned empty translations")
	}

	result := make(map[string]string, len(locales))
	for _, locale := range locales {
		if value, ok := decoded.Translations[locale]; ok {
			if translated := strings.TrimSpace(value); translated != "" {
				result[locale] = translated
			}
			continue
		}
		for key, value := range decoded.Translations {
			if strings.EqualFold(strings.TrimSpace(key), locale) {
				if translated := strings.TrimSpace(value); translated != "" {
					result[locale] = translated
				}
				break
			}
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("translate endpoint returned no matching locale translations")
	}
	return result, nil
}

// dedupeLocales removes blank and duplicate locale values.
func dedupeLocales(locales []string) []string {
	if len(locales) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(locales))
	out := make([]string, 0, len(locales))
	for _, locale := range locales {
		trimmed := strings.TrimSpace(locale)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
