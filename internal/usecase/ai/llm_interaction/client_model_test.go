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

	client := NewInteractionClientWithModel(server.URL, 5, "qwen3.5:2b", "/predict/action_decision", "/predict/question_answer", "/predict/context_analyze", "/predict/todo_analyze", "/predict/todo_due_time_normalize")
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

	client := NewInteractionClientWithModel(server.URL, 5, "qwen3.5:2b", "/predict/action_decision", "/predict/question_answer", "/predict/context_analyze", "/predict/todo_analyze", "/predict/todo_due_time_normalize")
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

func TestInteractionClientAnalyzeTodoUsesTodoPath(t *testing.T) {
	// 這個測試鎖住 Todo Reminder structured analyzer 的 transport contract：
	// AnalyzeTodo 必須走 dedicated /predict/todo_analyze，不可共用 context_analyze 的通用 schema。
	var capturedPath string
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":"v1","decision":"update_candidate","linked_message_id":"msg-1","summary":"補報價單","assignees":["我"],"due_text":"晚點","confidence":0.88,"missing_fields":[],"reason":"current message commits to prior todo"}`))
	}))
	defer server.Close()

	client := NewInteractionClientWithModel(server.URL, 5, "qwen3.5:2b", "/predict/action_decision", "/predict/question_answer", "/predict/context_analyze", "/predict/todo_analyze", "/predict/todo_due_time_normalize")
	result, err := client.AnalyzeTodo(context.Background(), "Analyze todo as JSON", "我晚點補")
	if err != nil {
		t.Fatalf("analyze todo failed: %v", err)
	}

	if capturedPath != "/predict/todo_analyze" {
		t.Fatalf("expected todo path, got %s", capturedPath)
	}
	if captured["model_name"] != "qwen3.5:2b" {
		t.Fatalf("expected model_name qwen3.5:2b, got %#v", captured["model_name"])
	}
	jsonRetryPrompt, ok := captured["json_decode_retry_prompt"].(string)
	if !ok {
		t.Fatalf("json_decode_retry_prompt missing or invalid: %#v", captured["json_decode_retry_prompt"])
	}
	for _, fragment := range []string{"todo_analysis", "Do not put JSON fragments inside key names", "assignees and missing_fields must be JSON arrays"} {
		if !strings.Contains(jsonRetryPrompt, fragment) {
			t.Fatalf("expected todo json retry prompt to contain %q, got %s", fragment, jsonRetryPrompt)
		}
	}
	validationRetryPrompt, ok := captured["validation_retry_prompt"].(string)
	if !ok {
		t.Fatalf("validation_retry_prompt missing or invalid: %#v", captured["validation_retry_prompt"])
	}
	for _, fragment := range []string{"todo_analysis", "confidence: number, required", "Validation failure: {validation_error}"} {
		if !strings.Contains(validationRetryPrompt, fragment) {
			t.Fatalf("expected todo validation retry prompt to contain %q, got %s", fragment, validationRetryPrompt)
		}
	}
	if result.Decision != "update_candidate" || result.LinkedMessageID != "msg-1" || result.Summary != "補報價單" {
		t.Fatalf("unexpected todo result: %#v", result)
	}
}

func TestInteractionClientAnalyzeTodoDueTimeUsesTodoDueTimePath(t *testing.T) {
	// 這個測試鎖住 Todo Reminder due_text 正規化的 transport contract：
	// AnalyzeTodoDueTime 必須走 dedicated /predict/todo_due_time_normalize，不可共用 todo_analyze。
	var capturedPath string
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":"v1","decision":"normalized","due_at":"2026-07-20T09:00:00+08:00","timezone":"Asia/Taipei","precision":"datetime","confidence":0.86,"missing_fields":[],"reason":"explicit weekday and time"}`))
	}))
	defer server.Close()

	client := NewInteractionClientWithModel(server.URL, 5, "qwen3.5:2b", "/predict/action_decision", "/predict/question_answer", "/predict/context_analyze", "/predict/todo_analyze", "/predict/todo_due_time_normalize")
	result, err := client.AnalyzeTodoDueTime(context.Background(), "Normalize due time as JSON", "下週一九點")
	if err != nil {
		t.Fatalf("analyze todo due time failed: %v", err)
	}

	if capturedPath != "/predict/todo_due_time_normalize" {
		t.Fatalf("expected todo due time path, got %s", capturedPath)
	}
	if captured["model_name"] != "qwen3.5:2b" {
		t.Fatalf("expected model_name qwen3.5:2b, got %#v", captured["model_name"])
	}
	if result.Decision != "normalized" || result.DueAt == "" || result.Timezone != "Asia/Taipei" {
		t.Fatalf("unexpected todo due time result: %#v", result)
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

func TestDecodeTodoAnalysisResponseRejectsInvalidContract(t *testing.T) {
	// update_candidate 必須指出 linked_message_id，否則後續狀態機不知道要更新哪個候選。
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"schema_version":"v1","decision":"update_candidate","linked_message_id":"","summary":"補報價單","assignees":[],"due_text":"晚點","confidence":0.7,"missing_fields":[],"reason":"continues a prior todo"}`)),
	}

	decoded, err := decodeTodoAnalysisResponse(resp, "/predict/todo_analyze")
	if err == nil {
		t.Fatalf("expected contract validation error, got decoded=%+v", decoded)
	}
}

func TestDecodeTodoDueTimeResponseRejectsInvalidContract(t *testing.T) {
	// normalized 若沒有 RFC3339 due_at，後續 reminder scheduler 會無法安全排程。
	// Go client 邊界必須直接拒絕，不把不完整時間寫進 candidate。
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"schema_version":"v1","decision":"normalized","due_at":"明天","timezone":"Asia/Taipei","precision":"datetime","confidence":0.7,"missing_fields":[],"reason":"not normalized"}`)),
	}

	decoded, err := decodeTodoDueTimeResponse(resp, "/predict/todo_due_time_normalize")
	if err == nil {
		t.Fatalf("expected contract validation error, got decoded=%+v", decoded)
	}
}
