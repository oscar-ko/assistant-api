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

// Translator 定義翻譯服務介面。
//
// 抽象這層介面的目的：
// - 讓上層 AutoTranslateService 只依賴能力（Translate），不依賴 HTTP 實作細節。
// - 測試時可注入 stub/fake translator，避免真的呼叫外部模型端點。
// - 未來若改成本地模型或其他 provider，只需換實作不改業務流程。
type Translator interface {
	Translate(ctx context.Context, text string, targetLocales []string) (map[string]string, error)
}

// LLMTranslateClient 負責呼叫 LLM interaction 的 /translate 端點。
//
// 邊界責任：
// - 只處理「輸入驗證、HTTP 請求、回應解析、契約檢查」。
// - 不處理「誰可翻譯、何時翻譯、翻完要不要推播」等業務規則。
// 這樣可以確保 provider 層的 transport 與 usecase 規則分離。
type LLMTranslateClient struct {
	baseURL string
	client  *http.Client
}

// NewLLMTranslateClient 建立翻譯 client。
//
// 設定策略：
// - baseURL 在此先做 trim 與去尾斜線，避免每次 Translate 重複處理。
// - timeout 若未設定或 <=0，使用 90 秒預設值，避免無上限等待。
func NewLLMTranslateClient(baseURL string, timeoutSeconds int) *LLMTranslateClient {
	trimmed := strings.TrimSpace(baseURL)
	if timeoutSeconds <= 0 {
		timeoutSeconds = 90
	}
	return &LLMTranslateClient{
		baseURL: strings.TrimRight(trimmed, "/"),
		client:  &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
	}
}

// Translate 使用單次呼叫將文字翻譯成多語系。
//
// 失敗策略（fail-fast）：
// 1) client/baseURL/text/locales 任一無效直接回錯，不做猜測補值。
// 2) 上游回非 2xx 時，保留 status + body，方便從 log 快速定位問題。
// 3) 回應若無 translations 或與 target locale 對不上，視為契約失敗。
//
// 契約策略：
// - 請求固定包含 prompt/text/target_locales。
// - 回應期望 schema_version + translations map。
// - 允許 locale key 大小寫差異（例如 en-us / EN-US），但最終以原請求 locale 輸出。
func (c *LLMTranslateClient) Translate(ctx context.Context, text string, targetLocales []string) (map[string]string, error) {
	if c == nil {
		return nil, fmt.Errorf("translate client is not initialized")
	}
	if strings.TrimSpace(c.baseURL) == "" {
		return nil, fmt.Errorf("llm interaction service url is empty")
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
		// 統一補背景 context，避免下游 HTTP request 收到 nil context。
		ctx = context.Background()
	}

	type translateRequest struct {
		Prompt        string   `json:"prompt"`
		Text          string   `json:"text"`
		TargetLocales []string `json:"target_locales"`
	}
	type translateResponse struct {
		SchemaVersion string            `json:"schema_version"`
		Translations  map[string]string `json:"translations"`
	}

	// translate 端點是專用契約入口，不與 question_answer/action_decision 混用。
	endpoint := c.baseURL + "/translate"
	// prompt 由 Go 端固定輸入，確保不同 provider 的翻譯規範一致。
	prompt := "You are a translation engine. Translate the message content into all target locales. Return strict JSON only with this schema: {\"schema_version\":\"v1\",\"translations\":{\"<locale>\":\"<translation>\"}}. The translations object keys must exactly match target locales. Do not include extra keys or explanations."
	payload, err := json.Marshal(translateRequest{Prompt: prompt, Text: inputText, TargetLocales: locales})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 限制錯誤回應讀取大小，避免異常大 body 佔用過多記憶體。
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("translate endpoint status %d body: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var decoded translateResponse
	if err := json.Unmarshal(bodyBytes, &decoded); err != nil {
		return nil, err
	}
	if len(decoded.Translations) == 0 {
		return nil, fmt.Errorf("translate endpoint returned empty translations")
	}

	result := make(map[string]string, len(locales))
	for _, locale := range locales {
		// 先走精確 key 命中，確保常見情況為 O(1) 查找。
		if value, ok := decoded.Translations[locale]; ok {
			if translated := strings.TrimSpace(value); translated != "" {
				result[locale] = translated
			}
			continue
		}
		// 後備走大小寫不敏感比對，兼容上游偶發大小寫差異輸出。
		for key, value := range decoded.Translations {
			if strings.EqualFold(strings.TrimSpace(key), locale) {
				if translated := strings.TrimSpace(value); translated != "" {
					result[locale] = translated
				}
				break
			}
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("translate endpoint returned no matching locale translations")
	}
	return result, nil
}

// dedupeLocales 去除空白與重複語系，並保留第一次出現的原始值。
//
// 注意：
// - 這裡採大小寫敏感去重（與舊邏輯一致），避免自動改寫 locale 造成語意不明。
// - 不做語系正規化或推導（例如不把 en 轉 en-US），遵守 fail-fast 原則。
func dedupeLocales(locales []string) []string {
	if len(locales) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(locales))
	out := make([]string, 0, len(locales))
	for _, locale := range locales {
		trimmed := strings.TrimSpace(locale)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
