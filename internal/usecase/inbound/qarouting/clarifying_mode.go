package qarouting

import "strings"

// ShouldUseClarifyingQuestionMode decides whether the fallback should ask a
// clarifying question or directly answer the user's question.
func ShouldUseClarifyingQuestionMode(cause string, missingParameters []string) bool {
	trimmedCause := strings.ToLower(strings.TrimSpace(cause))
	if trimmedCause != "ask_clarifying_question" {
		return false
	}
	for _, parameter := range missingParameters {
		if strings.TrimSpace(parameter) != "" {
			return true
		}
	}
	return false
}
