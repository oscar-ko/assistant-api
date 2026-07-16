package semanticdecision

import (
	"encoding/json"
	"testing"
)

// TestActionDecisionParamString 驗證單值參數讀取邏輯：
// 1) nil/空參數時回 false
// 2) 正確 string 能取值
// 3) 型別不符會被拒絕
func TestActionDecisionParamString(t *testing.T) {
	tests := []struct {
		name     string
		decision *ActionDecision
		key      string
		want     string
		ok       bool
	}{
		{
			name:     "nil decision",
			decision: nil,
			key:      "target_locales",
			want:     "",
			ok:       false,
		},
		{
			name:     "nil action params",
			decision: &ActionDecision{},
			key:      "target_locales",
			want:     "",
			ok:       false,
		},
		{
			name: "string parameter",
			decision: &ActionDecision{ActionParams: map[string]json.RawMessage{
				"target_locales": mustRawJSON(t, "ja-JP"),
			}},
			key:  "target_locales",
			want: "ja-JP",
			ok:   true,
		},
		{
			name: "invalid type",
			decision: &ActionDecision{ActionParams: map[string]json.RawMessage{
				"target_locales": mustRawJSON(t, []string{"ja-JP"}),
			}},
			key:  "target_locales",
			want: "",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// ParamString 採 (value, ok) 風格，這裡同時比對內容與存在旗標。
			got, ok := tt.decision.ParamString(tt.key)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("ParamString mismatch: got=(%q,%v) want=(%q,%v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

// TestActionDecisionParamStringSlice 驗證多值參數讀取邏輯：
// - 支援 JSON array 與單一 string fallback
// - 會自動去除空白與大小寫重覆值
func TestActionDecisionParamStringSlice(t *testing.T) {
	tests := []struct {
		name     string
		decision *ActionDecision
		key      string
		want     []string
	}{
		{
			name: "array value with dedupe",
			decision: &ActionDecision{ActionParams: map[string]json.RawMessage{
				"target_locales": mustRawJSON(t, []string{"en-US", "ja-JP", "en-us", ""}),
			}},
			key:  "target_locales",
			want: []string{"en-US", "ja-JP"},
		},
		{
			name: "single value fallback for generic helper",
			decision: &ActionDecision{ActionParams: map[string]json.RawMessage{
				"target_locales": mustRawJSON(t, " zh-TW "),
			}},
			key:  "target_locales",
			want: []string{"zh-TW"},
		},
		{
			name: "missing key",
			decision: &ActionDecision{ActionParams: map[string]json.RawMessage{
				"other": mustRawJSON(t, "x"),
			}},
			key:  "target_locales",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 這裡用 index 逐一比對，確保輸出順序符合「保留首個有效值」策略。
			got := tt.decision.ParamStringSlice(tt.key)
			if len(got) != len(tt.want) {
				t.Fatalf("ParamStringSlice length mismatch: got=%v want=%v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("ParamStringSlice mismatch at %d: got=%v want=%v", i, got, tt.want)
				}
			}
		})
	}
}

func TestValidateActionDecisionRejectsCandidateMetadataKeys(t *testing.T) {
	err := validateActionDecision(&ActionDecision{NextStep: NextStepExecuteAction, ActionParams: map[string]json.RawMessage{
		"route_text": mustRawJSON(t, "開啟英文翻譯"),
	}})
	if err == nil {
		t.Fatal("expected forbidden action_params key to be rejected")
	}
}

func TestValidateActionDecisionNormalizesLocaleFormat(t *testing.T) {
	decision := &ActionDecision{NextStep: NextStepExecuteAction, ActionParams: map[string]json.RawMessage{
				// execute_action 需同時帶有效 api_operation。
	}, APIOperation: "start_translation_locale"}
	decision.ActionParams = map[string]json.RawMessage{
		ActionParamTargetLocales: mustRawJSON(t, []string{"ja-jp", "EN-us", "zh-tw"}),
	}
	if err := validateActionDecision(decision); err != nil {
		t.Fatalf("expected locale normalization to pass, got error: %v", err)
	}

	gotMulti := decision.ParamStringSlice(ActionParamTargetLocales)
	wantMulti := []string{"ja-JP", "en-US", "zh-TW"}
	if len(gotMulti) != len(wantMulti) {
		t.Fatalf("normalized target_locales length mismatch: got=%v want=%v", gotMulti, wantMulti)
	}
	for i := range gotMulti {
		if gotMulti[i] != wantMulti[i] {
			t.Fatalf("normalized target_locales mismatch at %d: got=%v want=%v", i, gotMulti, wantMulti)
		}
	}
}

func TestValidateActionDecisionRejectsInvalidLocaleFormat(t *testing.T) {
	err := validateActionDecision(&ActionDecision{NextStep: NextStepExecuteAction, APIOperation: "start_translation_locale", ActionParams: map[string]json.RawMessage{
		ActionParamTargetLocales: mustRawJSON(t, []string{"english-US"}),
	}})
	if err == nil {
		t.Fatal("expected invalid locale format to be rejected")
	}
}

func TestValidateActionDecisionRejectsNonArrayTargetLocales(t *testing.T) {
	err := validateActionDecision(&ActionDecision{NextStep: NextStepExecuteAction, APIOperation: "start_translation_locale", ActionParams: map[string]json.RawMessage{
		ActionParamTargetLocales: mustRawJSON(t, "en-US"),
	}})
	if err == nil {
		t.Fatal("expected target_locales string value to be rejected")
	}
}

func TestValidateActionDecisionRequiresNextStep(t *testing.T) {
	err := validateActionDecision(&ActionDecision{})
	if err == nil {
		t.Fatal("expected next_step to be required")
	}
}

func TestValidateActionDecisionRejectsAnswerQuestionWithActionPayload(t *testing.T) {
	err := validateActionDecision(&ActionDecision{
		NextStep:     NextStepAnswerQuestion,
		APIOperation: "start_translation_locale",
		ActionParams: map[string]json.RawMessage{"x": mustRawJSON(t, "y")},
	})
	if err == nil {
		t.Fatal("expected answer_question payload with action data to be rejected")
	}
}

func TestValidateActionDecisionRejectsExecuteActionWithMissingParameters(t *testing.T) {
	err := validateActionDecision(&ActionDecision{
		NextStep:          NextStepExecuteAction,
		APIOperation:      "start_translation_locale",
		MissingParameters: []string{"target_locales"},
	})
	if err == nil {
		t.Fatal("expected execute_action with missing_parameters to be rejected")
	}
}

// mustRawJSON 是測試輔助：把任意 Go 值轉成 RawMessage，
// 讓測試可精準模擬上游 action_params 原始 JSON 片段。
func mustRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("failed to marshal test value: %v", err)
	}
	return data
}
