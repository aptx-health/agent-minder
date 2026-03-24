package remotetui

import (
	"testing"
	"time"

	"github.com/dustinlange/agent-minder/internal/api"
)

func TestNew(t *testing.T) {
	client := api.NewClient("localhost:7749", "test-key")
	m := New(client, "localhost:7749", 0, 0)

	if m.taskPollInterval != defaultTaskPollInterval {
		t.Errorf("expected default task poll %v, got %v", defaultTaskPollInterval, m.taskPollInterval)
	}
	if m.analysisPollInterval != defaultAnalysisPollInterval {
		t.Errorf("expected default analysis poll %v, got %v", defaultAnalysisPollInterval, m.analysisPollInterval)
	}
	if m.remote != "localhost:7749" {
		t.Errorf("expected remote %q, got %q", "localhost:7749", m.remote)
	}
}

func TestNewCustomIntervals(t *testing.T) {
	client := api.NewClient("localhost:7749", "")
	m := New(client, "localhost:7749", 3*time.Second, 15*time.Second)

	if m.taskPollInterval != 3*time.Second {
		t.Errorf("expected task poll 3s, got %v", m.taskPollInterval)
	}
	if m.analysisPollInterval != 15*time.Second {
		t.Errorf("expected analysis poll 15s, got %v", m.analysisPollInterval)
	}
}

func TestStatusIcon(t *testing.T) {
	tests := []struct {
		status string
		empty  bool
	}{
		{"running", false},
		{"queued", false},
		{"done", false},
		{"review", false},
		{"reviewing", false},
		{"reviewed", false},
		{"bailed", false},
		{"failed", false},
		{"stopped", false},
		{"blocked", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		result := statusIcon(tt.status)
		if tt.empty && result != " " {
			t.Errorf("statusIcon(%q) = %q, want space", tt.status, result)
		}
		if !tt.empty && result == "" {
			t.Errorf("statusIcon(%q) returned empty string", tt.status)
		}
	}
}

func TestColorStatus(t *testing.T) {
	statuses := []string{"running", "queued", "done", "review", "reviewing", "reviewed", "bailed", "failed", "stopped", "blocked", "other"}
	for _, s := range statuses {
		result := colorStatus(s)
		if result == "" {
			t.Errorf("colorStatus(%q) returned empty", s)
		}
	}
}

func TestFormatElapsed(t *testing.T) {
	tests := []struct {
		start     string
		completed string
		live      bool
		empty     bool
	}{
		{"", "", false, true},
		{"2026-01-01 10:00:00", "2026-01-01 10:05:30", false, false},
		{"2026-01-01 10:00:00", "", false, true},
		{"2026-01-01 10:00:00", "", true, false},
		{"bad-date", "", false, true},
	}
	for _, tt := range tests {
		result := formatElapsed(tt.start, tt.completed, tt.live)
		if tt.empty && result != "" {
			t.Errorf("formatElapsed(%q, %q, %v) = %q, want empty", tt.start, tt.completed, tt.live, result)
		}
		if !tt.empty && result == "" {
			t.Errorf("formatElapsed(%q, %q, %v) returned empty", tt.start, tt.completed, tt.live)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m30s"},
		{3661 * time.Second, "1h01m"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestWordWrap(t *testing.T) {
	result := wordWrap("hello world this is a test", 10)
	if result == "" {
		t.Error("wordWrap returned empty")
	}
	// Each line should be roughly within width.
	for _, line := range splitLines(result) {
		if len(line) > 15 { // some tolerance for word boundaries
			t.Errorf("line too long: %q (len %d)", line, len(line))
		}
	}
}

func splitLines(s string) []string {
	var lines []string
	for _, l := range splitByNewline(s) {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func splitByNewline(s string) []string {
	result := make([]string, 0)
	start := 0
	for i := range s {
		if s[i] == '\n' {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		result = append(result, s[start:])
	}
	return result
}
