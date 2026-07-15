package semanticdecision

import (
	"encoding/json"
	"fmt"
	"strings"
)

// forbiddenActionParamKeys 是安全邊界：
// 候選描述欄位不允許出現在 action_params，避免模型把檢索 metadata 當成執行參數。
var forbiddenActionParamKeys = map[string]struct{}{
	"route_text": {},
	"skill":      {},
	"score":      {},
	"operation":  {},
}

// validateActionDecision 驗證 action_params 契約，並在可接受情況下做格式正規化。
func validateActionDecision(decision *ActionDecision) error {
	if decision == nil {
		return nil
	}
	nextStep := strings.TrimSpace(decision.NextStep)
	switch nextStep {
	case NextStepExecuteAction, NextStepAskClarifyingQuestion, NextStepAnswerQuestion:
		// valid
	case "":
		return fmt.Errorf("action decision next_step is required")
	default:
		return fmt.Errorf("action decision next_step %q is invalid", nextStep)
	}

	decision.NextStep = nextStep
	decision.APIOperation = strings.TrimSpace(decision.APIOperation)
	decision.Reason = strings.TrimSpace(decision.Reason)

	if nextStep == NextStepExecuteAction && decision.APIOperation == "" {
		return fmt.Errorf("action decision api_operation is required when next_step=execute_action")
	}
	if len(decision.MissingParameters) > 0 && nextStep != NextStepAskClarifyingQuestion {
		return fmt.Errorf("action decision next_step must be ask_clarifying_question when missing_parameters is not empty")
	}
	if nextStep == NextStepExecuteAction && len(decision.MissingParameters) > 0 {
		return fmt.Errorf("action decision missing_parameters must be empty when next_step=execute_action")
	}
	if nextStep == NextStepAnswerQuestion {
		if decision.APIOperation != "" {
			return fmt.Errorf("action decision api_operation must be empty when next_step=answer_question")
		}
		if len(decision.ActionParams) != 0 {
			return fmt.Errorf("action decision action_params must be empty when next_step=answer_question")
		}
		if len(decision.MissingParameters) != 0 {
			return fmt.Errorf("action decision missing_parameters must be empty when next_step=answer_question")
		}
	}

	if len(decision.ActionParams) == 0 {
		return nil
	}
	for key := range decision.ActionParams {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if _, forbidden := forbiddenActionParamKeys[normalized]; forbidden {
			return fmt.Errorf("action decision contains forbidden action_params key %q", normalized)
		}
	}
	if err := normalizeLocaleActionParams(decision); err != nil {
		return err
	}
	return nil
}

// normalizeLocaleActionParams 強制翻譯 locale 參數採用 xx-YY，
// 並把大小寫統一為 lang 小寫 + region 大寫（例如 en-US）。
func normalizeLocaleActionParams(decision *ActionDecision) error {
	if decision == nil || len(decision.ActionParams) == 0 {
		return nil
	}

	if raw, ok := decision.ActionParams[ActionParamTargetLocales]; ok && len(raw) > 0 {
		var locales []string
		if err := json.Unmarshal(raw, &locales); err != nil {
			return fmt.Errorf("action_params.%s must be string array", ActionParamTargetLocales)
		}
		if len(locales) == 0 {
			return fmt.Errorf("action_params.%s must contain at least one locale", ActionParamTargetLocales)
		}
		normalizedLocales := make([]string, 0, len(locales))
		seen := make(map[string]struct{}, len(locales))
		for _, locale := range locales {
			normalized, valid := normalizeLocaleTag(locale)
			if !valid {
				return fmt.Errorf("action_params.%s must contain ISO 639-1 + ISO 3166-1 values (xx-YY), got %q", ActionParamTargetLocales, strings.TrimSpace(locale))
			}
			if _, exists := seen[normalized]; exists {
				continue
			}
			seen[normalized] = struct{}{}
			normalizedLocales = append(normalizedLocales, normalized)
		}
		if len(normalizedLocales) == 0 {
			return fmt.Errorf("action_params.%s must contain at least one locale", ActionParamTargetLocales)
		}
		buf, err := json.Marshal(normalizedLocales)
		if err != nil {
			return fmt.Errorf("failed to normalize action_params.%s: %w", ActionParamTargetLocales, err)
		}
		decision.ActionParams[ActionParamTargetLocales] = buf
	}

	return nil
}

// normalizeLocaleTag 驗證 locale 是否符合 xx-YY，並回傳正規化格式。
func normalizeLocaleTag(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) != 5 || trimmed[2] != '-' {
		return "", false
	}
	lang := trimmed[:2]
	region := trimmed[3:]
	if !isASCIIAlpha2(lang) || !isASCIIAlpha2(region) {
		return "", false
	}
	return strings.ToLower(lang) + "-" + strings.ToUpper(region), true
}

func isASCIIAlpha2(value string) bool {
	if len(value) != 2 {
		return false
	}
	for i := 0; i < 2; i++ {
		ch := value[i]
		if (ch < 'A' || ch > 'Z') && (ch < 'a' || ch > 'z') {
			return false
		}
	}
	return true
}
