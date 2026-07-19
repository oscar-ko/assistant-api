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

	usecaserealtime "assistant-api/internal/usecase/inbound/realtime"
)

type Classifier = usecaserealtime.Classifier
type ClassificationResult = usecaserealtime.ClassificationResult

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
		Labels []string `json:"labels,omitempty"`
	}
	type classifyResponse struct {
		ModelName            string             `json:"model_name"`
		Labels               []string           `json:"labels"`
		PredictedLabel       string             `json:"predicted_label"`
		ClassificationSignal string             `json:"classification_signal"`
		Scores               map[string]float64 `json:"scores"`
		Probabilities        map[string]float64 `json:"probabilities"`
		Confidence           float64            `json:"confidence"`
		ScoreMargin          float64            `json:"score_margin"`
	}

	// 9000 classifier 是 embedding + linear weights 的 coarse gate；只送原始文字，避免 prompt 污染向量空間。
	// labels 仍可由 ai.classifier.labels 限縮本次允許的模型 label space。
	payload, err := json.Marshal(classifyRequest{Text: inputText, Labels: append([]string(nil), c.labels...)})
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
	signal := strings.TrimSpace(decoded.ClassificationSignal)
	if signal == "" {
		return nil, fmt.Errorf("classifier endpoint returned empty classification_signal")
	}
	return &ClassificationResult{
		Tag:           tag,
		Signal:        signal,
		Labels:        dedupeStrings(decoded.Labels),
		Scores:        decoded.Scores,
		Probabilities: decoded.Probabilities,
		Confidence:    decoded.Confidence,
		ScoreMargin:   decoded.ScoreMargin,
		ModelName:     strings.TrimSpace(decoded.ModelName),
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
