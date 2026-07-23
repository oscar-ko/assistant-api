package realtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/abadojack/whatlanggo"
)

// WhatlangLanguageDetector 是即時翻譯的來源語言偵測 adapter。
//
// 設計重點：
// - usecase 只需要 ISO-639-1 語言碼，例如 zh、en、ja。
// - adapter 負責把 whatlanggo 的偵測結果轉成 usecase 可用的穩定契約。
// - 偵測不可靠時回錯，讓上層選擇「不做同語言過濾」，而不是猜錯來源語言後誤刪目標語系。
type WhatlangLanguageDetector struct{}

// NewWhatlangLanguageDetector 建立預設來源語言偵測器。
func NewWhatlangLanguageDetector() *WhatlangLanguageDetector {
	return &WhatlangLanguageDetector{}
}

// DetectLanguage 回傳輸入文字的 ISO-639-1 語言碼。
//
// 注意：短句、混語、表情符號或只有人名的訊息可能讓偵測結果不可靠。
// 這種情況直接回錯，避免即時翻譯流程把「不確定」誤當成某個語言。
func (d *WhatlangLanguageDetector) DetectLanguage(ctx context.Context, text string) (string, error) {
	input := strings.TrimSpace(text)
	if input == "" {
		return "", fmt.Errorf("text is required")
	}
	info := whatlanggo.Detect(input)
	if !info.IsReliable() {
		return "", fmt.Errorf("source language detection is not reliable")
	}
	language := strings.TrimSpace(info.Lang.Iso6391())
	if language == "" {
		return "", fmt.Errorf("source language is unknown")
	}
	return language, nil
}
