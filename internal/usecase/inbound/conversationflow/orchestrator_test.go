package conversationflow

import (
	"strings"
	"testing"
)

// TestBuildFixedChainOperationCandidatesCarriesPrompt 驗證指令鍊固定候選會帶入 seed 動態 prompt。
func TestBuildFixedChainOperationCandidatesCarriesPrompt(t *testing.T) {
	o := &Orchestrator{}
	candidates := o.buildFixedChainOperationCandidates(&chainCommandContext{
		APIOperation: "start_translation_locale",
		SkillCode:    "channel.translation",
		ActionPrompt: "規則: target_locales 請輸出 xx-YY。",
	})

	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %d", len(candidates))
	}
	if candidates[0].Operation != "start_translation_locale" {
		t.Fatalf("unexpected operation: %s", candidates[0].Operation)
	}
	if candidates[0].Prompt == "" {
		t.Fatal("expected candidate prompt to be carried from chain context")
	}
}

func TestBuildCommandDecisionInputTextAddsTranslationFillPolicy(t *testing.T) {
	o := &Orchestrator{}
	text := o.buildCommandDecisionInputText("德文法文跟西班牙文", &chainCommandContext{
		APIOperation:      "start_translation_locale",
		MissingParameters: []string{"target_locales"},
	})

	if !strings.Contains(text, "mode=chain_parameter_fill") {
		t.Fatalf("expected chain phase marker, got: %s", text)
	}

	if !strings.Contains(text, "[parameter_fill_policy]") {
		t.Fatalf("expected parameter_fill_policy block, got: %s", text)
	}
	if !strings.Contains(text, "德文=de-DE") || !strings.Contains(text, "法文=fr-FR") || !strings.Contains(text, "西班牙文=es-ES") {
		t.Fatalf("expected locale mapping examples in policy block, got: %s", text)
	}
}

func TestBuildCommandDecisionInputTextSkipsPolicyWhenNoLocaleMissing(t *testing.T) {
	o := &Orchestrator{}
	text := o.buildCommandDecisionInputText("收到", &chainCommandContext{
		APIOperation:      "start_translation_locale",
		MissingParameters: []string{"something_else"},
	})

	if strings.Contains(text, "[parameter_fill_policy]") {
		t.Fatalf("did not expect parameter_fill_policy block when target_locales is not missing: %s", text)
	}
}

func TestBuildInitialDecisionInputTextAddsPhaseMarker(t *testing.T) {
	text := buildInitialDecisionInputText("@Jarvis 開啟翻譯")
	if !strings.Contains(text, "mode=initial_action_decision") {
		t.Fatalf("expected initial decision phase marker, got: %s", text)
	}
	if !strings.Contains(text, "[user_message]") {
		t.Fatalf("expected user_message section, got: %s", text)
	}
}
