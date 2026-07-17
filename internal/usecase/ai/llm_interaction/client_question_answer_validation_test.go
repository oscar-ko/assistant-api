package llminteraction

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestValidateQuestionAnswer(t *testing.T) {
	// 這組測試覆蓋 question-answer 契約的核心邊界：
	// - schema_version 多種別名正規化
	// - answer 必填
	// - confidence 合法範圍
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
			name: "valid schema alias 1.0",
			input: &QuestionAnswer{
				SchemaVersion: "1.0",
				Answer:        "ok",
				Confidence:    0.8,
			},
			wantError: false,
		},
		{
			name: "valid schema alias 2.0",
			input: &QuestionAnswer{
				SchemaVersion: "2.0",
				Answer:        "ok",
				Confidence:    0.8,
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
			name: "valid schema version v2",
			input: &QuestionAnswer{
				SchemaVersion: "v2",
				Answer:        "ok",
				Confidence:    0.8,
			},
			wantError: false,
		},
		{
			name: "invalid schema version v3",
			input: &QuestionAnswer{
				SchemaVersion: "v3",
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
			// 每個 case 都走同一個驗證入口，
			// 確保後續調整 normalize/validate 時不會破壞既有契約語意。
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

	decoded, err := decodeQuestionAnswerResponse(resp, "/predict/question_answer")
	if err == nil {
		t.Fatalf("expected contract validation error, got decoded=%+v", decoded)
	}
}
