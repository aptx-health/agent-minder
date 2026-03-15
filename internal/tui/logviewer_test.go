package tui

import (
	"strings"
	"testing"
)

func TestIsJSONL(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", false},
		{"plain text", "hello world\nfoo bar", false},
		{"single json line", `{"type":"assistant","tool":"Read"}`, true},
		{"jsonl with blank lines", "\n" + `{"type":"result"}` + "\n", true},
		{"invalid json", `{not json}`, false},
		{"mixed content starting with json", `{"type":"system"}` + "\nplain text", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isJSONL(tt.content); got != tt.want {
				t.Errorf("isJSONL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatTimestamp(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "        "},
		{"2024-01-15T10:30:00Z", "10:30:00"},
		{"2024-01-15T10:30:00+00:00", "10:30:00"},
		{"short", "short"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := formatTimestamp(tt.input); got != tt.want {
				t.Errorf("formatTimestamp(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRenderPlainLog(t *testing.T) {
	content := "normal line\nERROR: something failed\n\nanother line"
	result := renderPlainLog(content)

	// Should contain all lines.
	if !strings.Contains(result, "normal line") {
		t.Error("expected normal line in output")
	}
	if !strings.Contains(result, "something failed") {
		t.Error("expected error line in output")
	}
	if !strings.Contains(result, "another line") {
		t.Error("expected another line in output")
	}
}

func TestRenderJSONL(t *testing.T) {
	// Use real stream-json format from Claude Code.
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"abc","tools":["Bash","Read"]}`,
		`{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"tool_use","name":"Read","input":{"file_path":"internal/foo.go"}}]}}`,
		`{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Looking at the code..."}]}}`,
		`{"type":"result","subtype":"success","num_turns":12,"total_cost_usd":0.08,"duration_ms":180000,"stop_reason":"end_turn"}`,
		`not json at all`,
	}
	content := strings.Join(lines, "\n")

	result := renderJSONL(content)

	if !strings.Contains(result, "Read") {
		t.Error("expected tool name 'Read' in output")
	}
	if !strings.Contains(result, "Looking at the code") {
		t.Error("expected text content in output")
	}
	if !strings.Contains(result, "Completed") {
		t.Error("expected result line in output")
	}
	// Malformed line should be rendered dimmed.
	if !strings.Contains(result, "not json at all") {
		t.Error("expected malformed line in output")
	}
}

func TestRenderJSONL_ErrorResult(t *testing.T) {
	lines := []string{
		`{"type":"result","subtype":"error","is_error":true,"result":"API error","num_turns":3,"total_cost_usd":0.02,"duration_ms":5000}`,
	}
	content := strings.Join(lines, "\n")

	result := renderJSONL(content)

	if !strings.Contains(result, "Failed") {
		t.Error("expected 'Failed' in error result output")
	}
	if !strings.Contains(result, "API error") {
		t.Error("expected error message in output")
	}
}

func TestRenderJSONL_SystemError(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"error","error":"rate limited"}`,
	}
	content := strings.Join(lines, "\n")

	result := renderJSONL(content)

	if !strings.Contains(result, "rate limited") {
		t.Error("expected error message in output")
	}
}

func TestDimTimestamp(t *testing.T) {
	// We can't easily test ANSI output, but we can test that the function doesn't panic.
	dimStyle := mutedStyle()

	tests := []string{
		"plain text no timestamp",
		"2024-01-15T10:30:00Z some log line",
		"[10:30:00] bracketed timestamp",
		"short",
		"",
	}

	for _, input := range tests {
		result := dimTimestamp(input, dimStyle)
		if result == "" && input != "" {
			t.Errorf("dimTimestamp(%q) returned empty string", input)
		}
	}
}
