package conversationflow

import (
	"strings"
	"testing"

	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"
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

func TestBuildCommandDecisionInputTextCarriesChainContext(t *testing.T) {
	o := &Orchestrator{}
	text := o.buildCommandDecisionInputText("德文法文跟西班牙文", &chainCommandContext{
		APIOperation:      "start_translation_locale",
		MissingParameters: []string{"target_locales"},
	})

	if !strings.Contains(text, "mode=chain_parameter_fill") {
		t.Fatalf("expected chain phase marker, got: %s", text)
	}

	if !strings.Contains(text, "api_operation=start_translation_locale") {
		t.Fatalf("expected chain operation in context, got: %s", text)
	}
	if !strings.Contains(text, "missing_parameters=target_locales") {
		t.Fatalf("expected missing parameters in context, got: %s", text)
	}
}

func TestBuildCommandDecisionInputTextSkipsExtraPolicyText(t *testing.T) {
	o := &Orchestrator{}
	text := o.buildCommandDecisionInputText("收到", &chainCommandContext{
		APIOperation:      "start_translation_locale",
		MissingParameters: []string{"something_else"},
	})

	if strings.Contains(text, "[parameter_fill_policy]") {
		t.Fatalf("did not expect hardcoded policy block: %s", text)
	}
	if strings.Contains(text, "德文=de-DE") || strings.Contains(text, "英文=en-US") {
		t.Fatalf("did not expect locale mapping examples in orchestrator prompt: %s", text)
	}
}

func TestBuildMissingParameterTemplateQuestionIsGeneric(t *testing.T) {
	got := buildMissingParameterTemplateQuestion([]string{"target_locales"})
	if !strings.Contains(got, "請補充以下必要資訊後我才能執行指令：") {
		t.Fatalf("expected generic template, got: %s", got)
	}
	if strings.Contains(got, "英文") || strings.Contains(got, "日文") || strings.Contains(got, "翻譯成哪些語言") {
		t.Fatalf("did not expect translation-specific wording, got: %s", got)
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

func TestInferClarifyingOperation(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		candidates []llminteraction.ActionCandidate
		chainCtx   *chainCommandContext
		want       string
	}{
		{name: "prefer current decision operation", current: "start_translation_locale", want: "start_translation_locale"},
		{name: "fallback to chain operation", current: "", chainCtx: &chainCommandContext{APIOperation: "stop_translation_locale"}, want: "stop_translation_locale"},
		{name: "fallback to first candidate operation", current: "", candidates: []llminteraction.ActionCandidate{{Operation: "start_translation_locale"}}, want: "start_translation_locale"},
		{name: "empty when no source available", current: "", candidates: nil, chainCtx: nil, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferClarifyingOperation(tt.current, tt.candidates, tt.chainCtx)
			if got != tt.want {
				t.Fatalf("unexpected inferred operation: got=%q want=%q", got, tt.want)
			}
		})
	}
}
