package llminteraction

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
	if !strings.Contains(prompt, "欄位約束（由 contract spec 產生）：") {
		t.Fatalf("expected generated contract block in prompt, got: %s", prompt)
	}
	if !strings.Contains(prompt, "schema_version=v1") {
		t.Fatalf("expected schema version from spec in prompt, got: %s", prompt)
	}
	if !strings.Contains(prompt, "next_step: string, required, enum=execute_action|ask_clarifying_question|answer_question") {
		t.Fatalf("expected next_step enum constraints in prompt, got: %s", prompt)
	}
	globalSkillSpecificRules := []string{
		"翻譯類 action",
		"target_locales 必須輸出",
		"BCP47/locale code",
	}
	for _, rule := range globalSkillSpecificRules {
		if strings.Contains(prompt, rule) {
			t.Fatalf("expected global final action prompt not to contain skill-specific rule %q, got: %s", rule, prompt)
		}
	}
}

func TestBuildFinalActionPromptTellsModelNotToTreatRankAsFinalDecision(t *testing.T) {
	candidates := []ActionCandidate{
		{
			Operation: "stop_translation_locale",
			SkillCode: "channel.translation",
			RouteText: "把某個語言的翻譯關掉",
			Prompt:    "用途: 停用指定翻譯語系。規則: 只接受使用者明確指定的目標語系。",
		},
		{
			Operation: "stop_translation_all",
			SkillCode: "channel.translation",
			RouteText: "關閉翻譯服務",
			Prompt:    "用途: 關閉整體翻譯服務。規則: 不需語系參數。",
		},
	}

	prompt := BuildFinalActionPrompt(candidates)
	checks := []string{
		"候選排序與 score 只代表召回/精排參考，不是最終答案",
		"必要語意條件是否已由使用者訊息明確提供",
		"使用者訊息只表達整體啟用/停用意圖",
		"route_text 是召回提示語，不是使用者已提供參數的證據",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Fatalf("expected prompt to contain %q, got: %s", check, prompt)
		}
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
	if !strings.Contains(prompt, "欄位固定如下：") {
		t.Fatalf("expected generated question-answer contract fields in prompt, got: %s", prompt)
	}
	if !strings.Contains(prompt, "schema_version, answer, confidence") {
		t.Fatalf("expected question-answer fields from spec in prompt, got: %s", prompt)
	}
}

func TestBuildQuestionAnswerPromptUsesContractSpecBlock(t *testing.T) {
	prompt := BuildQuestionAnswerPrompt()
	if !strings.Contains(prompt, "schema_version=v1") {
		t.Fatalf("expected schema version from spec in prompt, got: %s", prompt)
	}
	if !strings.Contains(prompt, "answer: string, required") {
		t.Fatalf("expected answer field constraints in prompt, got: %s", prompt)
	}
	if !strings.Contains(prompt, "confidence: number, required, min=0, max=1") {
		t.Fatalf("expected confidence constraints in prompt, got: %s", prompt)
	}
}
