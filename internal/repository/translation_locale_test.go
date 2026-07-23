package repository

import "testing"

func TestNormalizeTranslationLocaleTagAcceptsCodesOnly(t *testing.T) {
	// target_locale 的契約是 BCP-47 風格的語言碼/語系碼，不是自然語言名稱。
	// 第一段必須是目標語言本身；例如 fr 或 fr-FR 才是法文，en-FR 仍然是英文。
	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "language code", input: "FR", want: "fr", ok: true},
		{name: "region locale", input: "zh_tw", want: "zh-TW", ok: true},
		{name: "natural language label", input: "法文", ok: false},
		{name: "english language name", input: "French", ok: false},
		{name: "too short", input: "f", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeTranslationLocaleTag(tt.input)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("normalizeTranslationLocaleTag(%q) = (%q, %v), want (%q, %v)", tt.input, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestNormalizeLocaleFilterDropsInvalidLocales(t *testing.T) {
	// 刪除/過濾時也套用相同正規化規則；無效輸入直接丟棄，避免拿自然語言文字組查詢條件。
	got := normalizeLocaleFilter([]string{"FR", "fr", "法文", "zh_tw"})
	want := []string{"fr", "zh-TW"}

	if len(got) != len(want) {
		t.Fatalf("normalizeLocaleFilter() = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("normalizeLocaleFilter() = %#v, want %#v", got, want)
		}
	}
}
