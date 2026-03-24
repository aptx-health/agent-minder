package autopilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseAgentLog_ResultEvent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")

	content := `{"type":"system","subtype":"init","session_id":"abc-123"}
{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Working on it..."}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":5,"total_cost_usd":0.42,"stop_reason":null,"result":"Done!","permission_denials":[],"session_id":"abc-123"}
`
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := parseAgentLog(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NumTurns != 5 {
		t.Errorf("NumTurns = %d, want 5", result.NumTurns)
	}
	if result.TotalCost != 0.42 {
		t.Errorf("TotalCost = %f, want 0.42", result.TotalCost)
	}
	if result.Result != "Done!" {
		t.Errorf("Result = %q, want %q", result.Result, "Done!")
	}
	if result.SessionID != "abc-123" {
		t.Errorf("SessionID = %q, want %q", result.SessionID, "abc-123")
	}
	if result.IsError {
		t.Error("IsError should be false")
	}
	if len(result.PermissionDenials) != 0 {
		t.Errorf("PermissionDenials should be empty, got %d", len(result.PermissionDenials))
	}
}

func TestParseAgentLog_NoResultEvent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")

	content := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[]}}
`
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := parseAgentLog(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result when no result event present")
	}
}

func TestParseAgentLog_EmptyPath(t *testing.T) {
	result, err := parseAgentLog("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil for empty path")
	}
}

func TestParseAgentLog_MissingFile(t *testing.T) {
	_, err := parseAgentLog("/nonexistent/path/agent.log")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestParseAgentLog_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")

	content := `not json at all
{"type":"result","subtype":"success","num_turns":3,"total_cost_usd":0.10,"result":"ok","permission_denials":[]}
more garbage
`
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := parseAgentLog(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NumTurns != 3 {
		t.Errorf("NumTurns = %d, want 3", result.NumTurns)
	}
}

func TestClassifyOutcome_PermissionDenials(t *testing.T) {
	result := &AgentResult{
		NumTurns:          5,
		TotalCost:         0.10,
		PermissionDenials: []json.RawMessage{json.RawMessage(`"Bash"`), json.RawMessage(`"Edit"`)},
	}
	status, reason, detail := classifyOutcome(result, 50, 3.0)
	if status != "warning" {
		t.Errorf("status = %q, want %q", status, "warning")
	}
	if reason != "permissions" {
		t.Errorf("reason = %q, want %q", reason, "permissions")
	}
	if detail == "" {
		t.Error("detail should not be empty")
	}
}

func TestClassifyOutcome_MaxTurns(t *testing.T) {
	result := &AgentResult{
		NumTurns:  50,
		TotalCost: 1.50,
	}
	status, reason, detail := classifyOutcome(result, 50, 3.0)
	if status != "failed" {
		t.Errorf("status = %q, want %q", status, "failed")
	}
	if reason != "max_turns" {
		t.Errorf("reason = %q, want %q", reason, "max_turns")
	}
	if detail == "" {
		t.Error("detail should not be empty")
	}
}

func TestClassifyOutcome_MaxBudget(t *testing.T) {
	result := &AgentResult{
		NumTurns:  10,
		TotalCost: 2.90, // >= 3.0 * 0.95 = 2.85
	}
	status, reason, detail := classifyOutcome(result, 50, 3.0)
	if status != "failed" {
		t.Errorf("status = %q, want %q", status, "failed")
	}
	if reason != "max_budget" {
		t.Errorf("reason = %q, want %q", reason, "max_budget")
	}
	if detail == "" {
		t.Error("detail should not be empty")
	}
}

func TestClassifyOutcome_Error(t *testing.T) {
	result := &AgentResult{
		IsError: true,
		Result:  "Something went terribly wrong",
	}
	status, reason, detail := classifyOutcome(result, 50, 3.0)
	if status != "failed" {
		t.Errorf("status = %q, want %q", status, "failed")
	}
	if reason != "error" {
		t.Errorf("reason = %q, want %q", reason, "error")
	}
	if detail != "Something went terribly wrong" {
		t.Errorf("detail = %q, want result text", detail)
	}
}

func TestClassifyOutcome_NoFailure(t *testing.T) {
	result := &AgentResult{
		NumTurns:  10,
		TotalCost: 0.50,
	}
	status, reason, detail := classifyOutcome(result, 50, 3.0)
	if status != "" {
		t.Errorf("status = %q, want empty", status)
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty", reason)
	}
	if detail != "" {
		t.Errorf("detail = %q, want empty", detail)
	}
}

func TestClassifyOutcome_Nil(t *testing.T) {
	status, reason, detail := classifyOutcome(nil, 50, 3.0)
	if status != "" || reason != "" || detail != "" {
		t.Error("expected all empty for nil result")
	}
}

func TestClassifyOutcome_PriorityOrder(t *testing.T) {
	// Hard failures (max_turns, budget, error) take priority over permission warnings.
	result := &AgentResult{
		NumTurns:          50,
		TotalCost:         2.90,
		IsError:           true,
		PermissionDenials: []json.RawMessage{json.RawMessage(`"Bash"`)},
	}
	status, reason, _ := classifyOutcome(result, 50, 3.0)
	if status != "failed" {
		t.Errorf("status = %q, want %q", status, "failed")
	}
	if reason != "max_turns" {
		t.Errorf("reason = %q, want %q (hard failures should take priority over permission warnings)", reason, "max_turns")
	}
}

func TestClassifyOutcome_BudgetBelowThreshold(t *testing.T) {
	result := &AgentResult{
		NumTurns:  10,
		TotalCost: 2.80, // below 3.0 * 0.95 = 2.85
	}
	status, _, _ := classifyOutcome(result, 50, 3.0)
	if status != "" {
		t.Error("should not classify as failed when cost is below threshold")
	}
}

func TestCountTurnsFromLog_CountsAssistantEvents(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")

	content := `{"type":"system","subtype":"init","session_id":"abc-123"}
{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Step 1"}]}}
{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Step 2"}]}}
{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Step 3"}]}}
`
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	count := countTurnsFromLog(logPath)
	if count != 3 {
		t.Errorf("countTurnsFromLog = %d, want 3", count)
	}
}

func TestCountTurnsFromLog_EmptyPath(t *testing.T) {
	count := countTurnsFromLog("")
	if count != 0 {
		t.Errorf("countTurnsFromLog = %d, want 0 for empty path", count)
	}
}

func TestCountTurnsFromLog_MissingFile(t *testing.T) {
	count := countTurnsFromLog("/nonexistent/path/agent.log")
	if count != 0 {
		t.Errorf("countTurnsFromLog = %d, want 0 for missing file", count)
	}
}

func TestCountTurnsFromLog_NoAssistantEvents(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")

	content := `{"type":"system","subtype":"init"}
{"type":"result","subtype":"success","num_turns":0}
`
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	count := countTurnsFromLog(logPath)
	if count != 0 {
		t.Errorf("countTurnsFromLog = %d, want 0", count)
	}
}

func TestCountTurnsFromLog_MalformedLines(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")

	content := `not json at all
{"type":"assistant","message":{"content":[]}}
more garbage
{"type":"assistant","message":{"content":[]}}
`
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	count := countTurnsFromLog(logPath)
	if count != 2 {
		t.Errorf("countTurnsFromLog = %d, want 2 (should skip malformed lines)", count)
	}
}
