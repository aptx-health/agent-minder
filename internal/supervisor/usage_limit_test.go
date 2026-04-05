package supervisor

import "testing"

func TestIsUsageLimitError(t *testing.T) {
	tests := []struct {
		name   string
		result *AgentResult
		want   bool
	}{
		{
			name:   "nil result",
			result: nil,
			want:   false,
		},
		{
			name:   "normal success",
			result: &AgentResult{Result: "All done, PR opened."},
			want:   false,
		},
		{
			name:   "hit your limit message",
			result: &AgentResult{Result: "You've hit your limit · resets 3pm (America/Denver)", IsError: true},
			want:   true,
		},
		{
			name:   "session limit reached",
			result: &AgentResult{Result: "Session limit reached · resets 6pm", IsError: true},
			want:   true,
		},
		{
			name:   "usage limit uppercase",
			result: &AgentResult{Result: "USAGE LIMIT. RESET AT 01:00 PM", IsError: true},
			want:   true,
		},
		{
			name:   "rate limit reached",
			result: &AgentResult{Result: "API Error: Rate limit reached", IsError: true},
			want:   true,
		},
		{
			name:   "rate_limit category",
			result: &AgentResult{Result: "rate_limit", IsError: true},
			want:   true,
		},
		{
			name:   "billing_error category",
			result: &AgentResult{Result: "billing_error", IsError: true},
			want:   true,
		},
		{
			name:   "normal error not usage limit",
			result: &AgentResult{Result: "permission denied: cannot access file", IsError: true},
			want:   false,
		},
		{
			name:   "bail report not usage limit",
			result: &AgentResult{Result: "I cannot complete this task because the architecture is unclear"},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isUsageLimitError(tt.result)
			if got != tt.want {
				t.Errorf("isUsageLimitError() = %v, want %v", got, tt.want)
			}
		})
	}
}
