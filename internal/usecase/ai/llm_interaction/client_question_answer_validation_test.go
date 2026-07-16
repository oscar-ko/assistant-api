package llminteraction

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestValidateQuestionAnswer(t *testing.T) {
	tests := []struct {
		name      string
		input     *QuestionAnswer
		wantError bool
	}{
		{
			name: "valid answer",
			input: &QuestionAnswer{
				SchemaVersion: " v1 ",
				Answer:        " 好的，我來幫你處理 ",
				Confidence:    0.87,
			},
			wantError: false,
		},
		{
			name: "missing schema version",
			input: &QuestionAnswer{
				SchemaVersion: "",
				Answer:        "ok",
				Confidence:    0.8,
			},
			wantError: true,
		},
		{
			name: "invalid schema version",
			input: &QuestionAnswer{
				SchemaVersion: "v2",
				Answer:        "ok",
				Confidence:    0.8,
			},
			wantError: true,
		},
		{
			name: "missing answer",
			input: &QuestionAnswer{
				SchemaVersion: "v1",
				Answer:        "   ",
				Confidence:    0.8,
			},
			wantError: true,
		},
		{
			name: "confidence too high",
			input: &QuestionAnswer{
				SchemaVersion: "v1",
				Answer:        "ok",
				Confidence:    1.2,
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateQuestionAnswer(tt.input)
			if tt.wantError && err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("expected no validation error, got: %v", err)
			}
		})
	}
}

func TestDecodeQuestionAnswerResponseRejectsInvalidContract(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"schema_version":"v1","answer":"","confidence":0.6}`)),
	}

	decoded, err := decodeQuestionAnswerResponse(resp)
	if err == nil {
		t.Fatalf("expected contract validation error, got decoded=%+v", decoded)
	}
}
