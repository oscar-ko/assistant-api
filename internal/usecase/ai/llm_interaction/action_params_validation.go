package llminteraction

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
)

// DecisionValidationError 表示模型回傳符合 JSON 但不符合 action 決策契約。
// 例如：next_step 不合法、execute_action 缺 api_operation、target_locales 格式錯誤等。
type DecisionValidationError struct {
	// Reason 是原始驗證失敗訊息，保留給 log 與追問理由。
	Reason string
	// APIOperation 是模型回覆中的 operation（若有），用於落 action_results。
	APIOperation string
	// MissingParameters 是從契約錯誤推斷出的缺參數清單。
	// 若上游原本未提供 missing_parameters，這欄可補齊可追蹤性。
	MissingParameters []string
}

func (e *DecisionValidationError) Error() string {
	if e == nil {
		return "action decision validation failed"
	}
	return strings.TrimSpace(e.Reason)
}

// IsDecisionValidationError 用於區分「可追問修正」的契約錯誤與其他運行時錯誤。
func IsDecisionValidationError(err error) bool {
	var target *DecisionValidationError
	return errors.As(err, &target)
}

// AsDecisionValidationError returns typed validation error when present.
func AsDecisionValidationError(err error) *DecisionValidationError {
	var target *DecisionValidationError
	if errors.As(err, &target) {
		return target
	}
	return nil
}

var actionParamKeyPattern = regexp.MustCompile(`action_params\.([a-zA-Z0-9_]+)`)

// InferMissingParametersFromReason extracts missing parameter keys from contract error messages.
func InferMissingParametersFromReason(reason string) []string {
	// 這裡只抽 action_params.<key> 型態，避免把一般敘述文字誤判成參數名。
	matches := actionParamKeyPattern.FindAllStringSubmatch(strings.TrimSpace(reason), -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, item := range matches {
		if len(item) < 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(item[1]))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

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

	if len(decision.ActionParams) > 0 {
		for key := range decision.ActionParams {
			normalized := strings.ToLower(strings.TrimSpace(key))
			if _, forbidden := forbiddenActionParamKeys[normalized]; forbidden {
				return fmt.Errorf("action decision contains forbidden action_params key %q", normalized)
			}
		}
		if err := normalizeLocaleActionParams(decision); err != nil {
			return err
		}
	}
	return nil
}

// validateQuestionAnswer 驗證問答契約，並做最小正規化。
// 這層會在 local/cloud 兩種 client 路徑共用，確保切換 provider 仍維持同一份 contract。
func validateQuestionAnswer(answer *QuestionAnswer) error {
	if answer == nil {
		return nil
	}

	answer.SchemaVersion = strings.TrimSpace(answer.SchemaVersion)
	answer.Answer = strings.TrimSpace(answer.Answer)

	if answer.SchemaVersion == "" {
		return fmt.Errorf("question answer schema_version is required")
	}
	if answer.SchemaVersion != "v1" {
		return fmt.Errorf("question answer schema_version %q is invalid", answer.SchemaVersion)
	}
	if answer.Answer == "" {
		return fmt.Errorf("question answer answer is required")
	}
	if math.IsNaN(answer.Confidence) || answer.Confidence < 0 || answer.Confidence > 1 {
		return fmt.Errorf("question answer confidence must be between 0 and 1")
	}

	return nil
}

// normalizeLocaleActionParams 驗證翻譯 locale 參數格式，
// 接受語言碼（xx）或 locale（xx-YY）。
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
				return fmt.Errorf("action_params.%s must contain language code (xx) or locale (xx-YY), got %q", ActionParamTargetLocales, strings.TrimSpace(locale))
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

// normalizeLocaleTag 驗證 locale 格式並回傳正規化格式。
// 規則：接受 xx 或 xx-YY；不做語言->地區推測。
func normalizeLocaleTag(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) == 2 && isASCIIAlpha2(trimmed) {
		return strings.ToLower(trimmed), true
	}
	if len(trimmed) == 5 && trimmed[2] == '_' {
		trimmed = trimmed[:2] + "-" + trimmed[3:]
	}
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
