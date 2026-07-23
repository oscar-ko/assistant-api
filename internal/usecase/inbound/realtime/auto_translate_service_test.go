package realtime

import (
	"reflect"
	"strings"
	"testing"
)

func TestComposeTranslationsSeparatesLanguagesWithBlankLine(t *testing.T) {
	// 使用者要求不同語言至少換行，且不要 [en]/[ja] 或 bullet 前綴；
	// 因此輸出只保留翻譯文字本身，語言之間用空白行分隔。
	got := composeTranslations(
		[]string{"en", "ja"},
		map[string]string{
			"en": "Hello everyone.",
			"ja": "みなさん、こんにちは。",
		},
	)
	want := "Hello everyone.\n\nみなさん、こんにちは。"

	if got != want {
		t.Fatalf("composeTranslations() = %q, want %q", got, want)
	}
	if !strings.Contains(got, "\n\nみなさん") {
		t.Fatalf("composeTranslations() did not separate languages with a blank line: %q", got)
	}
}

func TestExcludeSourceLanguageLocalesRemovesSameLanguageTargets(t *testing.T) {
	// zh 與 zh-TW 視為同一來源語言，避免中文訊息再被翻成中文一次。
	// 重複目標語系也會在過濾時去重，降低模型呼叫成本與重複輸出機率。
	targetLocales := []string{"zh-TW", "ja", "en-US", "zh", "ja"}

	got := excludeSourceLanguageLocales(targetLocales, "zh")
	want := []string{"ja", "en-US"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("excludeSourceLanguageLocales() = %#v, want %#v", got, want)
	}
}

func TestExcludeSourceLanguageLocalesKeepsDifferentRegionalTargets(t *testing.T) {
	// 同語言不同地區仍以第一段語言碼判斷；pt-PT 來源會排除 pt-BR，
	// 但 en、ja-JP 這類不同語言目標要保留。
	targetLocales := []string{"pt-BR", "en", "ja-JP"}

	got := excludeSourceLanguageLocales(targetLocales, "pt-PT")
	want := []string{"en", "ja-JP"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("excludeSourceLanguageLocales() = %#v, want %#v", got, want)
	}
}
