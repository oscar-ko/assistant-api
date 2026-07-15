package semanticdecision

import (
	"strings"
	"testing"
)

func TestBuildFinalActionPromptIncludesActionPromptGuidance(t *testing.T) {
	candidates := []ActionCandidate{
		{
			Operation: "start_translation_locale",
			SkillCode: "channel.translation",
			RouteText: "開啟翻譯",
			Prompt:    "必要參數: target_locales (array)。",
		},
		{
			Operation: "start_translation_locale",
			SkillCode: "channel.translation",
			RouteText: "新增翻譯語系",
			Prompt:    "這筆重複 operation 應忽略，保留第一筆。",
		},
		{
			Operation: "stop_translation_all",
			SkillCode: "channel.translation",
			RouteText: "關閉翻譯",
			Prompt:    "作用範圍不明時先提問。",
		},
	}

	prompt := BuildFinalActionPrompt(candidates)
	if !strings.Contains(prompt, "operation 專屬動態規則（由 action.prompt 注入）：") {
		t.Fatalf("expected dynamic guidance section in prompt, got: %s", prompt)
	}
	if !strings.Contains(prompt, "operation=start_translation_locale prompt=必要參數: target_locales (array)。") {
		t.Fatalf("expected start_translation_locale guidance in prompt, got: %s", prompt)
	}
	if strings.Contains(prompt, "這筆重複 operation 應忽略，保留第一筆") {
		t.Fatalf("expected duplicate operation guidance to be deduped, got: %s", prompt)
	}
	if !strings.Contains(prompt, "operation=stop_translation_all prompt=作用範圍不明時先提問。") {
		t.Fatalf("expected stop_translation_all guidance in prompt, got: %s", prompt)
	}
}

func TestBuildClarifyingQuestionPromptIncludesDecisionReason(t *testing.T) {
	prompt := BuildClarifyingQuestionPrompt("缺少 target_locales，無法安全執行翻譯")
	if !strings.Contains(prompt, "缺少 target_locales，無法安全執行翻譯") {
		t.Fatalf("expected decision reason to be injected into clarifying prompt, got: %s", prompt)
	}
	if !strings.Contains(prompt, "提出一個最能消除歧義的追問問題") {
		t.Fatalf("expected clarifying question guidance in prompt, got: %s", prompt)
	}
	if !strings.Contains(prompt, "只能問一個最小必要問題") {
		t.Fatalf("expected minimal follow-up constraint in prompt, got: %s", prompt)
	}
}
