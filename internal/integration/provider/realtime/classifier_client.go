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

// Classifier tags a message with a service-oriented category.
type Classifier interface {
	Classify(ctx context.Context, text string) (*ClassificationResult, error)
}

// ClassificationResult is the strict subset of the classifier response needed by realtime services.
type ClassificationResult struct {
	Tag       string
	Labels    []string
	Scores    map[string]float64
	ModelName string
}

const classifierPrompt = `Classify the incoming non-command chat message into one trained service tag.
Use only the classifier model label space and return the predicted label from the model.`

// LocalClassifierClient calls the venv classifier service at /predict/classifier.
type LocalClassifierClient struct {
	baseURL      string
	endpointPath string
	labels       []string
	client       *http.Client
}

// NewLocalClassifierClient builds a local classifier client.
func NewLocalClassifierClient(baseURL string, timeoutSeconds int, endpointPath string, labels []string) *LocalClassifierClient {
	path := strings.TrimSpace(endpointPath)
	if path == "" {
		path = "/predict/classifier"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}
	return &LocalClassifierClient{
		baseURL:      strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		endpointPath: path,
		labels:       dedupeStrings(labels),
		client:       &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
	}
}

// Classify sends text to the classifier and returns the predicted tag.
func (c *LocalClassifierClient) Classify(ctx context.Context, text string) (*ClassificationResult, error) {
	if c == nil {
		return nil, fmt.Errorf("classifier client is not initialized")
	}
	if strings.TrimSpace(c.baseURL) == "" {
		return nil, fmt.Errorf("classifier service url is empty")
	}
	inputText := strings.TrimSpace(text)
	if inputText == "" {
		return nil, fmt.Errorf("text is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	type classifyRequest struct {
		Text   string   `json:"text"`
		Prompt string   `json:"prompt,omitempty"`
		Labels []string `json:"labels,omitempty"`
	}
	type classifyResponse struct {
		ModelName      string             `json:"model_name"`
		Labels         []string           `json:"labels"`
		PredictedLabel string             `json:"predicted_label"`
		Scores         map[string]float64 `json:"scores"`
	}

	payload, err := json.Marshal(classifyRequest{Text: inputText, Prompt: classifierPrompt, Labels: append([]string(nil), c.labels...)})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+c.endpointPath, bytes.NewReader(payload))
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
		return nil, fmt.Errorf("classifier endpoint status %d body: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var decoded classifyResponse
	if err := json.Unmarshal(bodyBytes, &decoded); err != nil {
		return nil, err
	}
	tag := strings.TrimSpace(decoded.PredictedLabel)
	if tag == "" {
		return nil, fmt.Errorf("classifier endpoint returned empty predicted_label")
	}
	return &ClassificationResult{
		Tag:       tag,
		Labels:    dedupeStrings(decoded.Labels),
		Scores:    decoded.Scores,
		ModelName: strings.TrimSpace(decoded.ModelName),
	}, nil
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
