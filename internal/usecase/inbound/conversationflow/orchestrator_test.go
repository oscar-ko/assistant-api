package conversationflow

import "testing"

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
