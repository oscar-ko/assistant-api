package llminteraction

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

// NewOpenAIInteractionClient 建立可直接呼叫 OpenAI Chat Completions 的 interaction client。
// 這個 client 會把 OpenAI 回應轉成既有 ActionDecision / QuestionAnswer 契約。
func NewOpenAIInteractionClient(baseURL string, token string, decisionModel string, chatModel string, timeoutSeconds int, maxTokens *int, temperature *float64, useJSONResponseFmt *bool) (InteractionClient, error) {
	trimmedBaseURL := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmedBaseURL == "" {
		return nil, fmt.Errorf("openai base url is empty")
	}
	trimmedToken := strings.TrimSpace(token)
	if trimmedToken == "" {
		return nil, fmt.Errorf("openai token is empty")
	}
	trimmedDecisionModel := strings.TrimSpace(decisionModel)
	if trimmedDecisionModel == "" {
		trimmedDecisionModel = "gpt-4o-mini"
	}
	trimmedChatModel := strings.TrimSpace(chatModel)
	if trimmedChatModel == "" {
		trimmedChatModel = "gpt-4o-mini"
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
	return &openAIInteractionClient{
		baseURL:            trimmedBaseURL,
		token:              trimmedToken,
		decisionModel:      trimmedDecisionModel,
		chatModel:          trimmedChatModel,
		maxTokens:          normalizedMaxTokens,
		temperature:        normalizedTemperature,
		useJSONResponseFmt: useJSONResponseFormatOrDefault(useJSONResponseFmt),
		httpClient:         &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
	}, nil
}

type openAIInteractionClient struct {
	baseURL       string
	token         string
	decisionModel string
	chatModel     string
	maxTokens     *int
	temperature   *float64
	// useJSONResponseFmt 預設為 true；若設定檔明確關閉，則不送 response_format。
	useJSONResponseFmt bool
	httpClient         *http.Client
}

type openAIChatCompletionRequest struct {
	Model       string                        `json:"model"`
	Messages    []openAIChatCompletionMessage `json:"messages"`
	Temperature *float64                      `json:"temperature,omitempty"`
	MaxTokens   *int                          `json:"max_tokens,omitempty"`
	ResponseFmt *openAIResponseFormat         `json:"response_format,omitempty"`
}

type openAIResponseFormat struct {
	Type string `json:"type"`
}

type openAIChatCompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatCompletionResponse struct {
	Choices []struct {
		Message openAIChatCompletionMessage `json:"message"`
	} `json:"choices"`
}

func (c *openAIInteractionClient) ClassifyAction(ctx context.Context, prompt string, text string) (*ActionDecision, error) {
	content, err := c.completeJSON(ctx, c.decisionModel, prompt, text)
	if err != nil {
		return nil, err
	}
	decoded, err := parseActionDecisionContent(content)
	if err != nil {
		return nil, err
	}
	if err := validateActionDecision(decoded); err != nil {
		inferredMissing := InferMissingParametersFromReason(err.Error())
		return nil, &DecisionValidationError{
			Reason:            err.Error(),
			APIOperation:      strings.TrimSpace(decoded.APIOperation),
			MissingParameters: append([]string(nil), inferredMissing...),
		}
	}
	return decoded, nil
}

func (c *openAIInteractionClient) AnswerQuestion(ctx context.Context, prompt string, text string) (*QuestionAnswer, error) {
	content, err := c.completeJSON(ctx, c.chatModel, prompt, text)
	if err != nil {
		return nil, err
	}
	decoded, err := parseQuestionAnswerContent(content)
	if err != nil {
		return nil, err
	}
	if err := validateQuestionAnswer(decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (c *openAIInteractionClient) AnalyzeContext(ctx context.Context, prompt string, text string) (*ContextAnalysis, error) {
	// 雲端 client 沒有本地 route path 可切，仍提供 AnalyzeContext 以滿足同一個 InteractionClient 介面。
	// 這裡使用 chatModel，是因為 context analysis 需要產生嚴格 JSON，而不是 action-decision 專用模型語意。
	content, err := c.completeJSON(ctx, c.chatModel, prompt, text)
	if err != nil {
		return nil, err
	}
	decoded, err := parseContextAnalysisContent(content)
	if err != nil {
		return nil, err
	}
	if err := validateContextAnalysis(decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (c *openAIInteractionClient) AnalyzeTodo(ctx context.Context, prompt string, text string) (*TodoAnalysis, error) {
	// 雲端 client 沒有本地 route path 可切；使用同一個 chatModel，但回應必須符合 TodoAnalysis contract。
	content, err := c.completeJSON(ctx, c.chatModel, prompt, text)
	if err != nil {
		return nil, err
	}
	decoded, err := parseTodoAnalysisContent(content)
	if err != nil {
		return nil, err
	}
	if err := validateTodoAnalysis(decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (c *openAIInteractionClient) completeJSON(ctx context.Context, model string, prompt string, text string) (string, error) {
	if c == nil || c.httpClient == nil {
		return "", fmt.Errorf("openai interaction client is not initialized")
	}
	trimmedModel := strings.TrimSpace(model)
	if trimmedModel == "" {
		return "", fmt.Errorf("openai model is empty")
	}

	fullPrompt := strings.TrimSpace(prompt)
	if strings.TrimSpace(text) != "" {
		fullPrompt += "\n\n使用者原始輸入：\n" + strings.TrimSpace(text)
	}

	includeTemperature := c.temperature != nil
	includeMaxTokens := c.maxTokens != nil

	for {
		// 這裡只處理參數相容性；prompt 與輸出契約不做替代或降級。
		payload := openAIChatCompletionRequest{
			Model: trimmedModel,
			Messages: []openAIChatCompletionMessage{
				{Role: "user", Content: fullPrompt},
			},
		}
		if c.useJSONResponseFmt {
			payload.ResponseFmt = &openAIResponseFormat{Type: "json_object"}
		}
		if includeTemperature {
			payload.Temperature = c.temperature
		}
		if includeMaxTokens {
			payload.MaxTokens = c.maxTokens
		}

		content, statusCode, responseBody, err := c.completeJSONWithPayload(ctx, payload)
		if err == nil {
			return content, nil
		}

		if statusCode == http.StatusBadRequest {
			if includeTemperature && isIncompatibleRequestArgument(responseBody, "temperature") {
				includeTemperature = false
				continue
			}
			if includeMaxTokens && isIncompatibleRequestArgument(responseBody, "max_tokens") {
				includeMaxTokens = false
				continue
			}
		}

		return "", err
	}
}

func useJSONResponseFormatOrDefault(value *bool) bool {
	// 設定未指定時維持舊行為：預設啟用 JSON response format。
	if value == nil {
		return true
	}
	return *value
}

func (c *openAIInteractionClient) completeJSONWithPayload(ctx context.Context, payload openAIChatCompletionRequest) (string, int, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", resp.StatusCode, "", err
	}
	trimmedBody := strings.TrimSpace(string(respBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode, trimmedBody, fmt.Errorf("openai chat completion returned status %d: %s", resp.StatusCode, trimmedBody)
	}

	var decoded openAIChatCompletionResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", resp.StatusCode, trimmedBody, err
	}
	if len(decoded.Choices) == 0 {
		return "", resp.StatusCode, trimmedBody, fmt.Errorf("openai chat completion returned empty choices")
	}
	content := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if content == "" {
		return "", resp.StatusCode, trimmedBody, fmt.Errorf("openai chat completion returned empty content")
	}
	return normalizeJSONLikeContent(content), resp.StatusCode, trimmedBody, nil
}

func isIncompatibleRequestArgument(responseBody string, argName string) bool {
	msg := strings.ToLower(strings.TrimSpace(responseBody))
	if msg == "" {
		return false
	}
	needle := "model incompatible request argument supplied: " + strings.ToLower(strings.TrimSpace(argName))
	return strings.Contains(msg, needle)
}

func normalizeJSONLikeContent(content string) string {
	// 先清掉 code fence / BOM，再從同一段 content 中抽出第一個完整 JSON object。
	// 這不是換欄位 fallback，只是容忍模型在 JSON 前後補一句說明文字。
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSuffix(trimmed, "```")
		trimmed = strings.TrimSpace(trimmed)
	}
	trimmed = strings.TrimPrefix(trimmed, "\ufeff")
	if jsonOnly, ok := extractJSONObject(trimmed); ok {
		return jsonOnly
	}
	return trimmed
}

func extractJSONObject(content string) (string, bool) {
	// 透過括號深度掃描抓出第一個完整 JSON object，避免前後自然語言干擾。
	// 若內容根本沒有 JSON，仍會回 false，保持 fail-fast。
	start := strings.Index(content, "{")
	if start < 0 {
		return "", false
	}

	depth := 0
	inString := false
	escaped := false
	for idx := start; idx < len(content); idx++ {
		ch := content[idx]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return strings.TrimSpace(content[start : idx+1]), true
			}
		}
	}

	return "", false
}

func parseActionDecisionContent(content string) (*ActionDecision, error) {
	var decoded ActionDecision
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		return nil, err
	}
	return &decoded, nil
}

func parseQuestionAnswerContent(content string) (*QuestionAnswer, error) {
	var decoded QuestionAnswer
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		return nil, err
	}
	return &decoded, nil
}

func parseContextAnalysisContent(content string) (*ContextAnalysis, error) {
	// parse 階段只負責 JSON decode；欄位完整性與語意限制集中交給 validateContextAnalysis。
	var decoded ContextAnalysis
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		return nil, err
	}
	return &decoded, nil
}

func parseTodoAnalysisContent(content string) (*TodoAnalysis, error) {
	// parse 階段只負責 JSON decode；欄位完整性與語意限制集中交給 validateTodoAnalysis。
	var decoded TodoAnalysis
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		return nil, err
	}
	return &decoded, nil
}
