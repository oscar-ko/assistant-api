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

// CloudTranslateClient 透過 Chat Completions 相容協定輸出翻譯 JSON。
//
// 注意：
// - 這裡不限定雲端供應商品牌，只要求目標端點相容 Chat Completions 協定。
// - 例如 OpenAI、OpenRouter（代理 Gemini/Claude）、或自建相容 gateway 都可套用。
type CloudTranslateClient struct {
	baseURL     string
	token       string
	modelName   string
	maxTokens   *int
	temperature *float64
	headers     map[string]string
	httpClient  *http.Client
}

// NewCloudTranslateClient 建立相容協定翻譯 client。
func NewCloudTranslateClient(baseURL string, token string, modelName string, timeoutSeconds int, maxTokens *int, temperature *float64, headers map[string]string) (*CloudTranslateClient, error) {
	trimmedBaseURL := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmedBaseURL == "" {
		return nil, fmt.Errorf("chat-completions-compatible base url is empty")
	}
	trimmedToken := strings.TrimSpace(token)
	if trimmedToken == "" {
		return nil, fmt.Errorf("chat-completions-compatible token is empty")
	}
	trimmedModel := strings.TrimSpace(modelName)
	if trimmedModel == "" {
		return nil, fmt.Errorf("chat-completions-compatible model name is empty")
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 120
	}
	var normalizedMaxTokens *int
	if maxTokens != nil {
		value := *maxTokens
		if value > 0 {
			normalizedMaxTokens = &value
		}
	}
	var normalizedTemperature *float64
	if temperature != nil {
		value := *temperature
		normalizedTemperature = &value
	}
	copiedHeaders := make(map[string]string, len(headers))
	for key, value := range headers {
		trimmedKey := strings.TrimSpace(key)
		trimmedValue := strings.TrimSpace(value)
		if trimmedKey == "" || trimmedValue == "" {
			continue
		}
		copiedHeaders[trimmedKey] = trimmedValue
	}
	return &CloudTranslateClient{
		baseURL:     trimmedBaseURL,
		token:       trimmedToken,
		modelName:   trimmedModel,
		maxTokens:   normalizedMaxTokens,
		temperature: normalizedTemperature,
		headers:     copiedHeaders,
		httpClient:  &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
	}, nil
}

type cloudTranslateChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type cloudTranslateResponseFormat struct {
	Type string `json:"type"`
}

type cloudTranslateRequest struct {
	Model          string                        `json:"model"`
	Messages       []cloudTranslateChatMessage   `json:"messages"`
	MaxTokens      *int                          `json:"max_tokens,omitempty"`
	Temperature    *float64                      `json:"temperature,omitempty"`
	ResponseFormat *cloudTranslateResponseFormat `json:"response_format,omitempty"`
}

type cloudTranslateChatResponse struct {
	Choices []struct {
		Message cloudTranslateChatMessage `json:"message"`
	} `json:"choices"`
}

type cloudTranslateResponse struct {
	SchemaVersion string            `json:"schema_version"`
	Translations  map[string]string `json:"translations"`
}

// Translate 呼叫雲端相容端點，並嚴格解析為翻譯契約。
func (c *CloudTranslateClient) Translate(ctx context.Context, text string, targetLocales []string) (map[string]string, error) {
	if c == nil || c.httpClient == nil {
		return nil, fmt.Errorf("chat-completions-compatible translate client is not initialized")
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

	prompt := buildCloudTranslatePrompt(locales, inputText)
	payload := cloudTranslateRequest{
		Model: c.modelName,
		Messages: []cloudTranslateChatMessage{
			{Role: "user", Content: prompt},
		},
		MaxTokens:      c.maxTokens,
		Temperature:    c.temperature,
		ResponseFormat: &cloudTranslateResponseFormat{Type: "json_object"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 16384))
	if err != nil {
		return nil, err
	}
	trimmedBody := strings.TrimSpace(string(responseBody))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("chat-completions-compatible translate status %d body: %s", resp.StatusCode, trimmedBody)
	}

	var decodedChat cloudTranslateChatResponse
	if err := json.Unmarshal(responseBody, &decodedChat); err != nil {
		return nil, err
	}
	if len(decodedChat.Choices) == 0 {
		return nil, fmt.Errorf("chat-completions-compatible translate returned empty choices")
	}
	content := normalizeCloudTranslateJSONLikeContent(decodedChat.Choices[0].Message.Content)
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("chat-completions-compatible translate returned empty content")
	}

	var decodedTranslate cloudTranslateResponse
	if err := json.Unmarshal([]byte(content), &decodedTranslate); err != nil {
		return nil, err
	}
	if len(decodedTranslate.Translations) == 0 {
		return nil, fmt.Errorf("chat-completions-compatible translate returned empty translations")
	}

	result := make(map[string]string, len(locales))
	for _, locale := range locales {
		// 雲端模型也必須遵守 target locale key 契約。
		// 僅容忍 key 的大小寫差異，不接受模型自行新增或替換語系。
		if value, ok := decodedTranslate.Translations[locale]; ok {
			if translated := strings.TrimSpace(value); translated != "" {
				result[locale] = translated
			}
			continue
		}
		for key, value := range decodedTranslate.Translations {
			if strings.EqualFold(strings.TrimSpace(key), locale) {
				if translated := strings.TrimSpace(value); translated != "" {
					result[locale] = translated
				}
				break
			}
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("chat-completions-compatible translate returned no matching locale translations")
	}
	// 不允許 partial success：少任何一個目標語系都回錯，交由上層記錄告警並停止推播。
	if missing := missingTranslationLocales(locales, result); len(missing) > 0 {
		return nil, fmt.Errorf("chat-completions-compatible translate missing locale translations: %s", strings.Join(missing, ", "))
	}
	return result, nil
}

func buildCloudTranslatePrompt(locales []string, inputText string) string {
	return strings.TrimSpace(`You are a translation engine.
Translate the user message into all target locales.
Return strict JSON only with this schema:
{"schema_version":"v1","translations":{"<locale>":"<translation>"}}
Rules:
- translations keys must exactly match target locales
- no extra keys
- no markdown/code fences

Target locales: ` + strings.Join(locales, ", ") + `

User message:
` + inputText)
}

func normalizeCloudTranslateJSONLikeContent(content string) string {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSuffix(trimmed, "```")
		trimmed = strings.TrimSpace(trimmed)
	}
	return trimmed
}
