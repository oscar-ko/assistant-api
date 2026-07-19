package llminteraction

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInteractionClientSendsConfiguredModelName(t *testing.T) {
	// 這個測試鎖住 local profile -> 9003 request 的核心契約：
	// provider profile 裡的 model_name 必須以結構化欄位送出，而不是混進 prompt 文字。
	// 這樣不同角色可以依 profile 使用不同模型，但仍共用同一個本地 LLM interaction 服務。
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":"v1","answer":"ok","confidence":0.8}`))
	}))
	defer server.Close()

	client := NewInteractionClientWithModel(server.URL, 5, "qwen3.5:2b", "/predict/action_decision", "/predict/question_answer", "/predict/context_analyze")
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

func TestInteractionClientAnalyzeContextUsesContextPath(t *testing.T) {
	// 這個測試鎖住 context_analyzer 的 transport contract：
	// AnalyzeContext 必須走 dedicated /predict/context_analyze，並且仍要帶 profile.model_name。
	// 若未來有人把它誤接回 question_answer，capturedPath 會直接讓測試失敗。
	var capturedPath string
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":"v1","decision":"relevant","target_service":"todo.reminder","confidence":0.82,"extracted_fields":{"due_time_text":"明天"},"missing_fields":[],"reason":"nearby context is relevant"}`))
	}))
	defer server.Close()

	client := NewInteractionClientWithModel(server.URL, 5, "qwen3.5:2b", "/predict/action_decision", "/predict/question_answer", "/predict/context_analyze")
	result, err := client.AnalyzeContext(context.Background(), "Analyze as JSON", "明天")
	if err != nil {
		t.Fatalf("analyze context failed: %v", err)
	}

	if capturedPath != "/predict/context_analyze" {
		t.Fatalf("expected context path, got %s", capturedPath)
	}
	if captured["model_name"] != "qwen3.5:2b" {
		t.Fatalf("expected model_name qwen3.5:2b, got %#v", captured["model_name"])
	}
	if result.Decision != "relevant" || result.TargetService != "todo.reminder" {
		t.Fatalf("unexpected context result: %#v", result)
	}
}

func TestDecodeContextAnalysisResponseRejectsInvalidContract(t *testing.T) {
	// needs_more_info 沒有 missing_fields 會讓下游不知道要追問什麼。
	// Go client 邊界必須直接拒絕，避免把半成品 context decision 帶入 realtime workflow。
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"schema_version":"v1","decision":"needs_more_info","target_service":"todo.reminder","confidence":0.7,"extracted_fields":{},"missing_fields":[],"reason":"needs due time"}`)),
	}

	decoded, err := decodeContextAnalysisResponse(resp, "/predict/context_analyze")
	if err == nil {
		t.Fatalf("expected contract validation error, got decoded=%+v", decoded)
	}
}
