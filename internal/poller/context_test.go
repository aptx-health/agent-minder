package poller

import (
	"strings"
	"testing"
)

func TestTruncateSlice_UnderLimit(t *testing.T) {
	items := []string{"a", "b", "c"}
	result, overflow := truncateSlice(items, 5)
	if len(result) != 3 {
		t.Errorf("expected 3 items, got %d", len(result))
	}
	if overflow != "" {
		t.Errorf("expected no overflow message, got %q", overflow)
	}
}

func TestTruncateSlice_AtLimit(t *testing.T) {
	items := []string{"a", "b", "c"}
	result, overflow := truncateSlice(items, 3)
	if len(result) != 3 {
		t.Errorf("expected 3 items, got %d", len(result))
	}
	if overflow != "" {
		t.Errorf("expected no overflow message, got %q", overflow)
	}
}

func TestTruncateSlice_OverLimit(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}
	result, overflow := truncateSlice(items, 3)
	if len(result) != 3 {
		t.Errorf("expected 3 items, got %d", len(result))
	}
	if !strings.Contains(overflow, "2 more") {
		t.Errorf("expected overflow mentioning '2 more', got %q", overflow)
	}
}

func TestTruncateSlice_Empty(t *testing.T) {
	var items []string
	result, overflow := truncateSlice(items, 5)
	if len(result) != 0 {
		t.Errorf("expected 0 items, got %d", len(result))
	}
	if overflow != "" {
		t.Errorf("expected no overflow, got %q", overflow)
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{"abcdefgh", 2},
		{strings.Repeat("x", 100), 25},
		{strings.Repeat("x", 101), 26},
	}
	for _, tt := range tests {
		got := estimateTokens(tt.input)
		if got != tt.want {
			t.Errorf("estimateTokens(%d chars) = %d, want %d", len(tt.input), got, tt.want)
		}
	}
}

func TestEstimateTokens_EmptyString(t *testing.T) {
	// Empty string edge case: (0 + 3) / 4 = 0 (integer division).
	got := estimateTokens("")
	if got != 0 {
		t.Errorf("estimateTokens('') = %d, want 0", got)
	}
}
