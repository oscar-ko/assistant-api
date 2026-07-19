package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalClassifierClientSendsRawTextAndDecodesSignal(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model_name":"test-model",
			"labels":["none","todo"],
			"predicted_label":"todo",
			"classification_signal":"candidate",
			"scores":{"none":0.1,"todo":1.2},
			"probabilities":{"none":0.25,"todo":0.75},
			"confidence":0.75,
			"score_margin":0.5
		}`))
	}))
	defer server.Close()

	client := NewLocalClassifierClient(server.URL, 5, "/predict/classifier", []string{"todo", "none"})
	result, err := client.Classify(context.Background(), " remind me tomorrow ")
	if err != nil {
		t.Fatalf("classify failed: %v", err)
	}

	if captured["text"] != "remind me tomorrow" {
		t.Fatalf("expected trimmed raw text, got %#v", captured["text"])
	}
	if _, exists := captured["prompt"]; exists {
		t.Fatalf("expected prompt to be omitted from classifier request")
	}
	if result.Tag != "todo" || result.Signal != "candidate" {
		t.Fatalf("unexpected result tag/signal: %#v", result)
	}
	if result.Confidence != 0.75 || result.ScoreMargin != 0.5 {
		t.Fatalf("unexpected confidence/margin: %#v", result)
	}
}

func TestLocalClassifierClientRequiresSignal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model_name":"test-model","labels":["none"],"predicted_label":"none","scores":{"none":1}}`))
	}))
	defer server.Close()

	client := NewLocalClassifierClient(server.URL, 5, "/predict/classifier", nil)
	if _, err := client.Classify(context.Background(), "hello"); err == nil {
		t.Fatalf("expected missing classification_signal to fail")
	}
}
