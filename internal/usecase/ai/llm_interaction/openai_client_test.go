package llminteraction

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIInteractionClient_AnswerQuestionIncludesJSONResponseFormatByDefault(t *testing.T) {
	// 預設 profile 仍要維持嚴格 JSON 模式，確保既有契約不會因為設定改動而鬆掉。
	var captured openAIChatCompletionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"schema_version\":\"v1\",\"answer\":\"測試答案\",\"confidence\":0.9}"}}]}`))
	}))
	defer server.Close()

	client, err := NewOpenAIInteractionClient(server.URL, "token", "gpt-4o-mini", "gpt-4o-mini", 30, nil, nil, nil)
	if err != nil {
		t.Fatalf("init client: %v", err)
	}

	answer, err := client.AnswerQuestion(context.Background(), BuildQuestionAnswerPrompt(), "推薦電影")
	if err != nil {
		t.Fatalf("answer question: %v", err)
	}
	if answer == nil || answer.Answer != "測試答案" {
		t.Fatalf("unexpected answer: %#v", answer)
	}
	if captured.ResponseFmt == nil || captured.ResponseFmt.Type != "json_object" {
		t.Fatalf("expected response_format json_object, got %#v", captured.ResponseFmt)
	}
}

func TestOpenAIInteractionClient_AnswerQuestionOmitsJSONResponseFormatWhenDisabled(t *testing.T) {
	// 搜尋型 model 會拒絕 response_format，這裡驗證可以透過設定檔關閉該欄位。
	disableJSONResponseFmt := false
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"schema_version\":\"v1\",\"answer\":\"測試答案\",\"confidence\":0.9}"}}]}`))
	}))
	defer server.Close()

	client, err := NewOpenAIInteractionClient(server.URL, "token", "gpt-5-search-api-2025-10-14", "gpt-5-search-api-2025-10-14", 30, nil, nil, &disableJSONResponseFmt)
	if err != nil {
		t.Fatalf("init client: %v", err)
	}

	answer, err := client.AnswerQuestion(context.Background(), BuildQuestionAnswerPrompt(), "推薦電影")
	if err != nil {
		t.Fatalf("answer question: %v", err)
	}
	if answer == nil || answer.Answer != "測試答案" {
		t.Fatalf("unexpected answer: %#v", answer)
	}
	if _, exists := captured["response_format"]; exists {
		t.Fatalf("expected response_format to be omitted, got %#v", captured["response_format"])
	}
}

func TestOpenAIInteractionClient_AnswerQuestionExtractsJSONFromSurroundingText(t *testing.T) {
	// 某些模型會在 JSON 前後加說明文字，這裡驗證可以從同一段 content 抽出完整 JSON。
	disableJSONResponseFmt := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"以下是回覆 JSON：\n{\"schema_version\":\"v1\",\"answer\":\"你可以看《超人》與《F1電影》。\",\"confidence\":0.72}\n請直接使用。"}}]}`))
	}))
	defer server.Close()

	client, err := NewOpenAIInteractionClient(server.URL, "token", "gpt-5-search-api-2025-10-14", "gpt-5-search-api-2025-10-14", 30, nil, nil, &disableJSONResponseFmt)
	if err != nil {
		t.Fatalf("init client: %v", err)
	}

	answer, err := client.AnswerQuestion(context.Background(), BuildQuestionAnswerPrompt(), "推薦幾部現在最新的電影吧")
	if err != nil {
		t.Fatalf("answer question: %v", err)
	}
	if answer == nil {
		t.Fatal("expected parsed answer")
	}
	if answer.Answer != "你可以看《超人》與《F1電影》。" {
		t.Fatalf("unexpected answer: %#v", answer)
	}
	if answer.Confidence != 0.72 {
		t.Fatalf("unexpected confidence: %#v", answer)
	}
}
