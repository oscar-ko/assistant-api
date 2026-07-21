package llminteraction

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

//go:embed contracts/action_decision_v1.json
var actionDecisionContractRaw []byte

//go:embed contracts/question_answer_v1.json
var questionAnswerContractRaw []byte

//go:embed contracts/todo_analysis_v1.json
var todoAnalysisContractRaw []byte

type outputFieldSpec struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Const    string   `json:"const,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Minimum  *float64 `json:"minimum,omitempty"`
	Maximum  *float64 `json:"maximum,omitempty"`
	Notes    string   `json:"notes,omitempty"`
}

type promptContractSpec struct {
	Name          string            `json:"name"`
	SchemaVersion string            `json:"schema_version"`
	OutputFields  []outputFieldSpec `json:"output_fields"`
}

var (
	contractSpecOnce sync.Once
	actionSpec       promptContractSpec
	questionSpec     promptContractSpec
	todoSpec         promptContractSpec
	contractSpecErr  error
)

func loadContractSpecs() {
	actionSpec, contractSpecErr = parsePromptContractSpec(actionDecisionContractRaw)
	if contractSpecErr != nil {
		return
	}
	questionSpec, contractSpecErr = parsePromptContractSpec(questionAnswerContractRaw)
	if contractSpecErr != nil {
		return
	}
	todoSpec, contractSpecErr = parsePromptContractSpec(todoAnalysisContractRaw)
}

func parsePromptContractSpec(raw []byte) (promptContractSpec, error) {
	var spec promptContractSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return promptContractSpec{}, err
	}
	if strings.TrimSpace(spec.Name) == "" {
		return promptContractSpec{}, fmt.Errorf("contract spec name is required")
	}
	if strings.TrimSpace(spec.SchemaVersion) == "" {
		return promptContractSpec{}, fmt.Errorf("contract schema_version is required")
	}
	if len(spec.OutputFields) == 0 {
		return promptContractSpec{}, fmt.Errorf("contract output_fields is required")
	}
	for _, field := range spec.OutputFields {
		if strings.TrimSpace(field.Name) == "" {
			return promptContractSpec{}, fmt.Errorf("contract field name is required")
		}
		if strings.TrimSpace(field.Type) == "" {
			return promptContractSpec{}, fmt.Errorf("contract field type is required")
		}
	}
	return spec, nil
}

func getActionDecisionContractSpec() (promptContractSpec, error) {
	contractSpecOnce.Do(loadContractSpecs)
	if contractSpecErr != nil {
		return promptContractSpec{}, contractSpecErr
	}
	return actionSpec, nil
}

func getQuestionAnswerContractSpec() (promptContractSpec, error) {
	contractSpecOnce.Do(loadContractSpecs)
	if contractSpecErr != nil {
		return promptContractSpec{}, contractSpecErr
	}
	return questionSpec, nil
}

func getTodoAnalysisContractSpec() (promptContractSpec, error) {
	contractSpecOnce.Do(loadContractSpecs)
	if contractSpecErr != nil {
		return promptContractSpec{}, contractSpecErr
	}
	return todoSpec, nil
}

func actionDecisionContractPromptBlock() string {
	spec, err := getActionDecisionContractSpec()
	if err != nil {
		return "schema_version, next_step, api_operation, action_params, missing_parameters, confidence, reason"
	}
	return renderPromptContractBlock(spec)
}

func questionAnswerContractPromptBlock() string {
	spec, err := getQuestionAnswerContractSpec()
	if err != nil {
		return "schema_version, answer, confidence"
	}
	return renderPromptContractBlock(spec)
}

func todoAnalysisContractPromptBlock() string {
	// Todo analyzer 的 retry prompt 與 primary prompt 共用同一份 contract spec，
	// 避免 Python validator、Go prompt 與測試各自維護欄位清單後產生漂移。
	spec, err := getTodoAnalysisContractSpec()
	if err != nil {
		return "schema_version, decision, linked_message_id, summary, assignees, due_text, confidence, missing_fields, reason"
	}
	return renderPromptContractBlock(spec)
}

func renderPromptContractBlock(spec promptContractSpec) string {
	names := make([]string, 0, len(spec.OutputFields))
	constraints := make([]string, 0, len(spec.OutputFields))
	for _, field := range spec.OutputFields {
		names = append(names, field.Name)
		constraint := fmt.Sprintf("- %s: %s", field.Name, field.Type)
		if field.Required {
			constraint += ", required"
		}
		if strings.TrimSpace(field.Const) != "" {
			constraint += fmt.Sprintf(", const=%s", strings.TrimSpace(field.Const))
		}
		if len(field.Enum) > 0 {
			constraint += fmt.Sprintf(", enum=%s", strings.Join(field.Enum, "|"))
		}
		if field.Minimum != nil {
			constraint += fmt.Sprintf(", min=%g", *field.Minimum)
		}
		if field.Maximum != nil {
			constraint += fmt.Sprintf(", max=%g", *field.Maximum)
		}
		if strings.TrimSpace(field.Notes) != "" {
			constraint += fmt.Sprintf(", notes=%s", strings.TrimSpace(field.Notes))
		}
		constraints = append(constraints, constraint)
	}

	return "schema_version=" + spec.SchemaVersion +
		"\n欄位固定如下：\n" + strings.Join(names, ", ") +
		"\n欄位約束（由 contract spec 產生）：\n" + strings.Join(constraints, "\n")
}
