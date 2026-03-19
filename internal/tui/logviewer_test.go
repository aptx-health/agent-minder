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

func TestDimTimestamp_ISOTimestampPreservesContent(t *testing.T) {
	dimStyle := mutedStyle()
	input := "2024-01-15T10:30:00Z some important log line"
	result := dimTimestamp(input, dimStyle)

	// The content after the timestamp should be preserved.
	if !strings.Contains(result, "some important log line") {
		t.Errorf("dimTimestamp() should preserve content after ISO timestamp, got: %q", result)
	}
}

func TestDimTimestamp_BracketedTimestampPreservesContent(t *testing.T) {
	dimStyle := mutedStyle()
	input := "[10:30:00] log message here"
	result := dimTimestamp(input, dimStyle)

	if !strings.Contains(result, " log message here") {
		t.Errorf("dimTimestamp() should preserve content after bracketed timestamp, got: %q", result)
	}
}

func TestDimTimestamp_NoTimestampReturnsInput(t *testing.T) {
	dimStyle := mutedStyle()
	input := "plain text without any timestamp prefix"
	result := dimTimestamp(input, dimStyle)

	if result != input {
		t.Errorf("dimTimestamp() with no timestamp should return input unchanged, got: %q", result)
	}
}

func TestRenderPlainLog_ErrorKeywords(t *testing.T) {
	// Test all error-like keywords that trigger error styling.
	keywords := []string{"error", "ERROR", "panic", "PANIC", "fatal", "FATAL", "failed", "FAILED"}
	for _, kw := range keywords {
		content := "normal line\n" + kw + ": something went wrong\nafter"
		result := renderPlainLog(content)
		if !strings.Contains(result, "something went wrong") {
			t.Errorf("renderPlainLog() should render line with keyword %q", kw)
		}
	}
}

func TestRenderPlainLog_EmptyContent(t *testing.T) {
	result := renderPlainLog("")
	if result == "" {
		t.Error("renderPlainLog(\"\") should return at least a newline")
	}
}

func TestRenderPlainLog_TimestampLines(t *testing.T) {
	content := "2024-01-15T10:30:00Z normal log line\n[10:30:00] bracketed log line"
	result := renderPlainLog(content)

	if !strings.Contains(result, "normal log line") {
		t.Error("renderPlainLog() should contain timestamp line content")
	}
	if !strings.Contains(result, "bracketed log line") {
		t.Error("renderPlainLog() should contain bracketed timestamp content")
	}
}

func TestRenderJSONL_EmptyContent(t *testing.T) {
	result := renderJSONL("")
	if result != "" {
		t.Errorf("renderJSONL(\"\") = %q, want empty", result)
	}
}

func TestRenderJSONL_NonErrorStopReason(t *testing.T) {
	lines := []string{
		`{"type":"result","num_turns":5,"total_cost_usd":0.10,"duration_ms":60000,"stop_reason":"max_turns"}`,
	}
	content := strings.Join(lines, "\n")
	result := renderJSONL(content)

	// Non-end_turn stop reasons should be shown in parentheses.
	if !strings.Contains(result, "max_turns") {
		t.Error("renderJSONL() should show non-end_turn stop reason")
	}
}

func TestRenderJSONL_SystemInitSkipped(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"abc","tools":["Bash","Read"]}`,
		`{"type":"system","subtype":"hook_started","hook_id":"123"}`,
	}
	content := strings.Join(lines, "\n")
	result := renderJSONL(content)

	// System init and hooks are silently skipped — result should be empty.
	if strings.TrimSpace(result) != "" {
		t.Errorf("renderJSONL() should skip system init/hooks, got: %q", result)
	}
}

func TestRenderJSONL_ToolUseTruncation(t *testing.T) {
	// Tool input longer than 80 chars should be truncated.
	longInput := strings.Repeat("x", 100)
	lines := []string{
		`{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"tool_use","name":"Bash","input":{"command":"` + longInput + `"}}]}}`,
	}
	content := strings.Join(lines, "\n")
	result := renderJSONL(content)

	if !strings.Contains(result, "Bash") {
		t.Error("renderJSONL() should show tool name")
	}
	if !strings.Contains(result, "...") {
		t.Error("renderJSONL() should truncate long tool input with ...")
	}
}

func TestRenderJSONL_TextTruncation(t *testing.T) {
	// Text longer than 80 chars should be truncated.
	longText := strings.Repeat("y", 100)
	lines := []string{
		`{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"` + longText + `"}]}}`,
	}
	content := strings.Join(lines, "\n")
	result := renderJSONL(content)

	if !strings.Contains(result, "...") {
		t.Error("renderJSONL() should truncate long text with ...")
	}
}

func TestIsJSONL_AllBlankLines(t *testing.T) {
	if isJSONL("\n\n\n") {
		t.Error("isJSONL with only blank lines should return false")
	}
}

func TestFormatTimestamp_LongTimestamp(t *testing.T) {
	// Timestamp longer than 8 chars that doesn't parse as RFC3339.
	input := "some-weird-timestamp-format"
	got := formatTimestamp(input)
	// Should return first 8 chars as fallback.
	if got != "some-wei" {
		t.Errorf("formatTimestamp(%q) = %q, want %q", input, got, "some-wei")
	}
}

func TestExtractLogToolInput_EmptyMap(t *testing.T) {
	got := extractLogToolInput(map[string]any{})
	// Empty map marshals to "{}", which is the JSON fallback.
	if got != "{}" {
		t.Errorf("extractLogToolInput(empty map) = %q, want %q", got, "{}")
	}
}

func TestExtractLogToolInput_EmptyValues(t *testing.T) {
	// Keys exist but with empty values — should fall back to JSON.
	got := extractLogToolInput(map[string]any{"command": ""})
	if got == "" {
		t.Error("extractLogToolInput() with empty command should not return empty (should fallback to JSON)")
	}
}
