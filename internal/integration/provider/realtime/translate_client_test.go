package realtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalTranslateClientRejectsPartialLocaleTranslations(t *testing.T) {
	// 回歸保護：翻譯 API 若只回其中一個 target locale，不能送出半套推播。
	// 這比自動補空字串或靜默忽略更安全，因為使用者可從 log/error 看到模型契約破壞。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":"v1","translations":{"en":"Hello everyone."}}`))
	}))
	defer server.Close()

	client := NewLocalTranslateClient(server.URL, 5)
	_, err := client.Translate(context.Background(), "大家好", []string{"en", "ja"})
	if err == nil {
		t.Fatal("Translate() error = nil, want missing locale error")
	}
	if !strings.Contains(err.Error(), "missing locale translations: ja") {
		t.Fatalf("Translate() error = %q, want missing ja locale", err.Error())
	}
}

func TestLocalTranslateClientAcceptsAllRequestedLocaleTranslations(t *testing.T) {
	// 正向案例：所有要求的 locale 都有非空翻譯時，才視為一次完整成功。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":"v1","translations":{"en":"Hello everyone.","ja":"みなさん、こんにちは。"}}`))
	}))
	defer server.Close()

	client := NewLocalTranslateClient(server.URL, 5)
	translations, err := client.Translate(context.Background(), "大家好", []string{"en", "ja"})
	if err != nil {
		t.Fatalf("Translate() error = %v", err)
	}
	if translations["en"] != "Hello everyone." || translations["ja"] != "みなさん、こんにちは。" {
		t.Fatalf("Translate() = %#v", translations)
	}
}
