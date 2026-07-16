package qarouting

import "testing"

func TestShouldUseClarifyingQuestionMode(t *testing.T) {
	tests := []struct {
		name              string
		cause             string
		missingParameters []string
		want              bool
	}{
		{name: "low confidence always clarify", cause: "low_action_confidence", missingParameters: nil, want: true},
		{name: "ask clarifying with missing parameter", cause: "ask_clarifying_question", missingParameters: []string{"target_locales"}, want: true},
		{name: "ask clarifying without missing parameter falls back to qa", cause: "ask_clarifying_question", missingParameters: nil, want: false},
		{name: "answer question does not clarify", cause: "answer_question", missingParameters: []string{"target_locales"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldUseClarifyingQuestionMode(tt.cause, tt.missingParameters)
			if got != tt.want {
				t.Fatalf("unexpected mode decision: got=%v want=%v", got, tt.want)
			}
		})
	}
}
