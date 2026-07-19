package llminteraction

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInteractionClientSendsConfiguredModelName(t *testing.T) {
	// 這個測試鎖住 local profile -> 9003 request 的核心契約：
	// provider profile 裡的 model_name 必須以結構化欄位送出，而不是混進 prompt 文字。
	// 這樣 todo_extractor 可以走 qwen 2B，action decision 可以走 qwen 9B，兩者仍共用同一個 9003 服務。
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":"v1","answer":"ok","confidence":0.8}`))
	}))
	defer server.Close()

	client := NewInteractionClientWithModel(server.URL, 5, "qwen3.5:2b", "/predict/action_decision", "/predict/question_answer")
	if client == nil {
		t.Fatal("expected client")
	}
	if _, err := client.AnswerQuestion(context.Background(), "Answer as JSON", "hello"); err != nil {
		t.Fatalf("answer question failed: %v", err)
	}

	if captured["model_name"] != "qwen3.5:2b" {
		t.Fatalf("expected model_name qwen3.5:2b, got %#v", captured["model_name"])
	}
	if captured["prompt"] == "" {
		t.Fatalf("expected composed prompt")
	}
}
