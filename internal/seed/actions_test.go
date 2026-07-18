package seed

import (
	"strings"
	"testing"
)

func TestTranslationStopSeedsPreferAllWhenLocaleIsUnspecified(t *testing.T) {
	all := findDefaultActionSeed(t, "stop_translation_all")
	locale := findDefaultActionSeed(t, "stop_translation_locale")

	if !containsString(all.RouteTexts, "關閉翻譯") {
		t.Fatalf("expected stop_translation_all to include generic close translation route, got: %#v", all.RouteTexts)
	}
	if !strings.Contains(all.CommandPurpose, "未明確限定目標語系或語言") {
		t.Fatalf("expected stop_translation_all prompt to cover unspecified locale, got: %s", all.CommandPurpose)
	}
	if !strings.Contains(locale.CommandPurpose, "若只是要求停用翻譯但未限定語系或語言，應選 stop_translation_all") {
		t.Fatalf("expected stop_translation_locale prompt to reject generic disable translation, got: %s", locale.CommandPurpose)
	}

	staleLocaleRoutes := []string{"關閉某語言翻譯", "把某個語言的翻譯關掉", "不要某語言翻譯"}
	for _, staleRoute := range staleLocaleRoutes {
		if containsString(locale.RouteTexts, staleRoute) {
			t.Fatalf("expected stale abstract locale route %q to be removed, got: %#v", staleRoute, locale.RouteTexts)
		}
	}
}

func TestNormalizeSeedRouteTexts(t *testing.T) {
	got := normalizeSeedRouteTexts([]string{" 關閉翻譯 ", "", "關閉翻譯", "停止翻譯"})
	want := []string{"關閉翻譯", "停止翻譯"}
	if len(got) != len(want) {
		t.Fatalf("normalizeSeedRouteTexts len = %d, want %d: %#v", len(got), len(want), got)
	}
	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("normalizeSeedRouteTexts[%d] = %q, want %q: %#v", idx, got[idx], want[idx], got)
		}
	}
}

func findDefaultActionSeed(t *testing.T, operation string) defaultActionSeed {
	t.Helper()
	for _, skillSeed := range defaultActionCatalogSeeds() {
		for _, actionSeed := range skillSeed.Actions {
			if actionSeed.APIOperation == operation {
				return actionSeed
			}
		}
	}
	t.Fatalf("default action seed not found for operation %s", operation)
	return defaultActionSeed{}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
