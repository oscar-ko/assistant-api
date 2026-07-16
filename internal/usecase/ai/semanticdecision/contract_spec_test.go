package semanticdecision

import "testing"

func TestGetActionDecisionContractSpec(t *testing.T) {
	spec, err := getActionDecisionContractSpec()
	if err != nil {
		t.Fatalf("expected valid action contract spec, got error: %v", err)
	}
	if spec.Name != "action_decision" {
		t.Fatalf("expected action_decision spec name, got: %s", spec.Name)
	}
	if spec.SchemaVersion != "v1" {
		t.Fatalf("expected schema version v1, got: %s", spec.SchemaVersion)
	}
	if len(spec.OutputFields) != 7 {
		t.Fatalf("expected 7 output fields, got: %d", len(spec.OutputFields))
	}
}

func TestGetQuestionAnswerContractSpec(t *testing.T) {
	spec, err := getQuestionAnswerContractSpec()
	if err != nil {
		t.Fatalf("expected valid question contract spec, got error: %v", err)
	}
	if spec.Name != "question_answer" {
		t.Fatalf("expected question_answer spec name, got: %s", spec.Name)
	}
	if spec.SchemaVersion != "v1" {
		t.Fatalf("expected schema version v1, got: %s", spec.SchemaVersion)
	}
	if len(spec.OutputFields) != 3 {
		t.Fatalf("expected 3 output fields, got: %d", len(spec.OutputFields))
	}
}
