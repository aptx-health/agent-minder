package agentutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseAgentLog_EmptyPath(t *testing.T) {
	r, err := ParseAgentLog("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r != nil {
		t.Fatalf("expected nil result, got %+v", r)
	}
}

func TestParseAgentLog_NonexistentFile(t *testing.T) {
	_, err := ParseAgentLog("/tmp/nonexistent-agent-log-file-abc123")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestParseAgentLog_EmptyFile(t *testing.T) {
	path := writeTestFile(t, "")
	r, err := ParseAgentLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r != nil {
		t.Fatalf("expected nil result, got %+v", r)
	}
}

func TestParseAgentLog_ValidResultEvent(t *testing.T) {
	content := `{"type":"assistant","message":"thinking..."}
{"type":"tool_use","name":"Bash","input":"ls"}
{"type":"result","subtype":"success","is_error":false,"num_turns":5,"total_cost_usd":0.42,"stop_reason":"end_turn","result":"All done","session_id":"sess-123"}
`
	path := writeTestFile(t, content)
	r, err := ParseAgentLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil result")
	}
	if r.SubType != "success" {
		t.Errorf("SubType = %q, want %q", r.SubType, "success")
	}
	if r.IsError {
		t.Error("IsError = true, want false")
	}
	if r.NumTurns != 5 {
		t.Errorf("NumTurns = %d, want 5", r.NumTurns)
	}
	if r.TotalCost != 0.42 {
		t.Errorf("TotalCost = %f, want 0.42", r.TotalCost)
	}
	if r.Result != "All done" {
		t.Errorf("Result = %q, want %q", r.Result, "All done")
	}
	if r.SessionID != "sess-123" {
		t.Errorf("SessionID = %q, want %q", r.SessionID, "sess-123")
	}
}

func TestParseAgentLog_ErrorResult(t *testing.T) {
	content := `{"type":"result","subtype":"error","is_error":true,"num_turns":1,"total_cost_usd":0.01,"result":"permission denied","permission_denials":[{"tool":"Bash"}],"session_id":"sess-456"}
`
	path := writeTestFile(t, content)
	r, err := ParseAgentLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil result")
	}
	if !r.IsError {
		t.Error("IsError = false, want true")
	}
	if r.SubType != "error" {
		t.Errorf("SubType = %q, want %q", r.SubType, "error")
	}
	if len(r.PermissionDenials) != 1 {
		t.Errorf("len(PermissionDenials) = %d, want 1", len(r.PermissionDenials))
	}
}

func TestParseAgentLog_MissingResultEvent(t *testing.T) {
	content := `{"type":"assistant","message":"hello"}
{"type":"tool_use","name":"Read","input":"/tmp/foo"}
{"type":"tool_result","output":"file contents"}
`
	path := writeTestFile(t, content)
	r, err := ParseAgentLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r != nil {
		t.Fatalf("expected nil result for log without result event, got %+v", r)
	}
}

func TestParseAgentLog_MalformedJSONLines(t *testing.T) {
	content := `not json at all
{"type":"assistant", broken json
{"type":"result","subtype":"success","is_error":false,"num_turns":3,"total_cost_usd":0.10,"result":"done","session_id":"s1"}
more garbage
`
	path := writeTestFile(t, content)
	r, err := ParseAgentLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil result despite malformed lines")
	}
	if r.SubType != "success" {
		t.Errorf("SubType = %q, want %q", r.SubType, "success")
	}
	if r.NumTurns != 3 {
		t.Errorf("NumTurns = %d, want 3", r.NumTurns)
	}
}

func TestParseAgentLog_OnlyMalformedJSON(t *testing.T) {
	content := `this is not json
{broken
also not valid}
`
	path := writeTestFile(t, content)
	r, err := ParseAgentLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r != nil {
		t.Fatalf("expected nil result for all-malformed file, got %+v", r)
	}
}

func TestParseAgentLog_FirstResultEventWins(t *testing.T) {
	content := `{"type":"result","subtype":"success","is_error":false,"num_turns":2,"total_cost_usd":0.05,"result":"first","session_id":"s1"}
{"type":"result","subtype":"error","is_error":true,"num_turns":10,"total_cost_usd":1.00,"result":"second","session_id":"s2"}
`
	path := writeTestFile(t, content)
	r, err := ParseAgentLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil result")
	}
	if r.Result != "first" {
		t.Errorf("Result = %q, want %q (first result event should win)", r.Result, "first")
	}
	if r.SessionID != "s1" {
		t.Errorf("SessionID = %q, want %q", r.SessionID, "s1")
	}
}

func TestParseAgentLog_LargeLine(t *testing.T) {
	// Create a valid result event with a large result field (under 1MB limit).
	bigResult := strings.Repeat("x", 500_000)
	line := `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":0.01,"result":"` + bigResult + `","session_id":"big"}`
	path := writeTestFile(t, line+"\n")
	r, err := ParseAgentLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil result for large line")
	}
	if len(r.Result) != 500_000 {
		t.Errorf("Result length = %d, want 500000", len(r.Result))
	}
}
