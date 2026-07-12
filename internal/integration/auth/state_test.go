package auth

import "testing"

func TestValidateState(t *testing.T) {
	tests := []struct {
		name     string
		got      string
		expected string
		want     bool
	}{
		{name: "empty got should fail", got: "", expected: "abc", want: false},
		{name: "empty expected is compatible", got: "abc", expected: "", want: true},
		{name: "mismatch should fail", got: "abc", expected: "def", want: false},
		{name: "exact match should pass", got: "abc", expected: "abc", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateState(tt.got, tt.expected); got != tt.want {
				t.Fatalf("ValidateState(%q, %q) = %v, want %v", tt.got, tt.expected, got, tt.want)
			}
		})
	}
}
