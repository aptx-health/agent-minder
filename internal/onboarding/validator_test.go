package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dustinlange/agent-minder/internal/agentutil"
)

// --- Mock runner ---

type mockAttempt struct {
	exitCode   int
	logContent string
	err        error
}

type mockRunner struct {
	attempts []mockAttempt
	idx      int
	// captured args from each call for assertion
	calls []mockCall
}

type mockCall struct {
	args    []string
	dir     string
	logPath string
}

func (m *mockRunner) Run(_ context.Context, args []string, dir string, logPath string) (int, error) {
	m.calls = append(m.calls, mockCall{args: args, dir: dir, logPath: logPath})

	if m.idx >= len(m.attempts) {
		return -1, fmt.Errorf("no more mock attempts configured")
	}
	a := m.attempts[m.idx]
	m.idx++

	if a.err != nil {
		return -1, a.err
	}

	if err := os.WriteFile(logPath, []byte(a.logContent), 0o644); err != nil {
		return -1, err
	}
	return a.exitCode, nil
}

// --- Stream-json log fixtures ---

func successLog(numTurns int, cost float64) string {
	return fmt.Sprintf(
		`{"type":"system","subtype":"init","session_id":"test-123"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Exploring..."}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":%d,"total_cost_usd":%.2f,"result":"Architecture looks good.","permission_denials":[],"session_id":"test-123"}
`, numTurns, cost)
}

func permissionDenialLog(denials ...string) string {
	var raw []json.RawMessage
	for _, d := range denials {
		raw = append(raw, json.RawMessage(d))
	}
	data, _ := json.Marshal(raw)
	return fmt.Sprintf(
		`{"type":"system","subtype":"init","session_id":"test-456"}
{"type":"result","subtype":"success","is_error":false,"num_turns":3,"total_cost_usd":0.05,"result":"Permission denied","permission_denials":%s,"session_id":"test-456"}
`, string(data))
}

func errorLog(msg string) string {
	return fmt.Sprintf(
		`{"type":"system","subtype":"init"}
{"type":"result","subtype":"error","is_error":true,"num_turns":1,"total_cost_usd":0.01,"result":"%s","permission_denials":[],"session_id":"test-789"}
`, msg)
}

// --- buildTestPrompt tests ---

func TestBuildTestPrompt_BothCommands(t *testing.T) {
	p := buildTestPrompt("go test ./...", "golangci-lint run")
	if !strings.Contains(p, "go test ./...") {
		t.Error("prompt should contain test command")
	}
	if !strings.Contains(p, "golangci-lint run") {
		t.Error("prompt should contain lint command")
	}
	if !strings.Contains(p, "Explore this repository") {
		t.Error("prompt should contain exploration instruction")
	}
	if !strings.Contains(p, "Report what happened.") {
		t.Error("prompt should end with report instruction")
	}
}

func TestBuildTestPrompt_NoCommands(t *testing.T) {
	p := buildTestPrompt("", "")
	if strings.Contains(p, "Run the test command") {
		t.Error("should not mention test command when empty")
	}
	if strings.Contains(p, "Run the lint command") {
		t.Error("should not mention lint command when empty")
	}
	if !strings.Contains(p, "Explore this repository") {
		t.Error("prompt should still contain exploration instruction")
	}
}

func TestBuildTestPrompt_OnlyTest(t *testing.T) {
	p := buildTestPrompt("npm test", "")
	if !strings.Contains(p, "npm test") {
		t.Error("prompt should contain test command")
	}
	if strings.Contains(p, "Run the lint command") {
		t.Error("should not mention lint command when empty")
	}
}

// --- buildTestArgs tests ---

func TestBuildTestArgs(t *testing.T) {
	tools := []string{"Bash(go *)", "Read", "Edit"}
	args := buildTestArgs(tools, "go test ./...", "golangci-lint run", "claude-haiku-4-5")

	// Check required flags.
	hasFlag := func(flag, value string) bool {
		for i, a := range args {
			if a == flag && i+1 < len(args) && args[i+1] == value {
				return true
			}
		}
		return false
	}

	if !hasFlag("--output-format", "stream-json") {
		t.Error("missing --output-format stream-json")
	}
	if !hasFlag("--max-turns", "5") {
		t.Error("missing --max-turns 5")
	}
	if !hasFlag("--max-budget-usd", "0.25") {
		t.Error("missing --max-budget-usd 0.25")
	}
	if !hasFlag("--model", "claude-haiku-4-5") {
		t.Error("missing --model claude-haiku-4-5")
	}

	// Check that all allowed tools are in a single comma-separated --allowedTools value.
	var toolsVal string
	for i, a := range args {
		if a == "--allowedTools" && i+1 < len(args) {
			toolsVal = args[i+1]
			break
		}
	}
	if toolsVal == "" {
		t.Fatal("missing --allowedTools flag")
	}
	for _, tool := range []string{"Read", "Edit", "Bash(go:*)"} {
		if !strings.Contains(toolsVal, tool) {
			t.Errorf("--allowedTools value %q missing %q", toolsVal, tool)
		}
	}

	// Check that prompt is the last arg.
	lastArg := args[len(args)-1]
	if !strings.Contains(lastArg, "Explore this repository") {
		t.Error("prompt should be the last argument")
	}

	// Check -p is present.
	found := false
	for _, a := range args {
		if a == "-p" {
			found = true
			break
		}
	}
	if !found {
		t.Error("missing -p flag")
	}
}

func TestBuildTestArgs_NoModel(t *testing.T) {
	args := buildTestArgs([]string{"Read"}, "go test ./...", "", "")
	for _, a := range args {
		if a == "--model" {
			t.Error("should not include --model when empty")
		}
	}
}

// --- ParseAgentLog tests (via agentutil) ---

func TestParseValidationLog_Success(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")
	if err := os.WriteFile(logPath, []byte(successLog(4, 0.12)), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := agentutil.ParseAgentLog(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	r := *result
	if r.NumTurns != 4 {
		t.Errorf("NumTurns = %d, want 4", r.NumTurns)
	}
	if r.IsError {
		t.Error("IsError should be false")
	}
	if len(result.PermissionDenials) != 0 {
		t.Errorf("PermissionDenials len = %d, want 0", len(result.PermissionDenials))
	}
}

func TestParseValidationLog_PermissionDenials(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")
	content := permissionDenialLog(
		`{"tool_name":"Bash","command":"npm test"}`,
		`{"tool_name":"Write"}`,
	)
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := agentutil.ParseAgentLog(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.PermissionDenials) != 2 {
		t.Errorf("PermissionDenials len = %d, want 2", len(result.PermissionDenials))
	}
}

func TestParseValidationLog_NoResultEvent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")
	content := `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[]}}
`
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := agentutil.ParseAgentLog(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result when no result event present")
	}
}

func TestParseValidationLog_MalformedLines(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")
	content := `not json
{"type":"result","subtype":"success","is_error":false,"num_turns":2,"total_cost_usd":0.05,"result":"ok","permission_denials":[]}
more garbage
`
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := agentutil.ParseAgentLog(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("should find result event among garbage lines")
	}
	if result.NumTurns != 2 {
		t.Errorf("NumTurns = %d, want 2", result.NumTurns)
	}
}

func TestParseValidationLog_MissingFile(t *testing.T) {
	_, err := agentutil.ParseAgentLog("/nonexistent/path/test.log")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// --- classifyValidation tests ---

func TestClassifyValidation_Success(t *testing.T) {
	result := &agentutil.AgentResult{NumTurns: 3, TotalCost: 0.05}
	reason := classifyValidation(result, 0)
	if reason != "" {
		t.Errorf("reason = %q, want empty", reason)
	}
}

func TestClassifyValidation_Permissions(t *testing.T) {
	result := &agentutil.AgentResult{
		PermissionDenials: []json.RawMessage{json.RawMessage(`"Bash"`)},
	}
	reason := classifyValidation(result, 0)
	if reason != "permissions" {
		t.Errorf("reason = %q, want %q", reason, "permissions")
	}
}

func TestClassifyValidation_Error(t *testing.T) {
	result := &agentutil.AgentResult{IsError: true, Result: "something broke"}
	reason := classifyValidation(result, 1)
	if !strings.Contains(reason, "agent error") {
		t.Errorf("reason = %q, should contain 'agent error'", reason)
	}
}

func TestClassifyValidation_LongErrorTruncated(t *testing.T) {
	result := &agentutil.AgentResult{IsError: true, Result: strings.Repeat("x", 600)}
	reason := classifyValidation(result, 1)
	if !strings.HasSuffix(reason, "...") {
		t.Error("long error should be truncated with ...")
	}
}

func TestClassifyValidation_ExitCodeNoResult(t *testing.T) {
	reason := classifyValidation(nil, 1)
	if !strings.Contains(reason, "exited with code 1") {
		t.Errorf("reason = %q, should mention exit code", reason)
	}
}

func TestClassifyValidation_NilResultZeroExit(t *testing.T) {
	reason := classifyValidation(nil, 0)
	if !strings.Contains(reason, "no result event") {
		t.Errorf("reason = %q, should mention no result event", reason)
	}
}

func TestClassifyValidation_PermissionsPriority(t *testing.T) {
	// Permissions should take priority over error flag.
	result := &agentutil.AgentResult{
		IsError:           true,
		PermissionDenials: []json.RawMessage{json.RawMessage(`"Bash"`)},
	}
	reason := classifyValidation(result, 1)
	if reason != "permissions" {
		t.Errorf("reason = %q, want %q (permissions should take priority)", reason, "permissions")
	}
}

// --- denialToPattern tests ---

func TestDenialToPattern_ObjectWithToolName(t *testing.T) {
	raw := json.RawMessage(`{"tool_name":"Read"}`)
	got := denialToPattern(raw)
	if got != "Read" {
		t.Errorf("got %q, want %q", got, "Read")
	}
}

func TestDenialToPattern_BashWithCommand(t *testing.T) {
	raw := json.RawMessage(`{"tool_name":"Bash","command":"npm run test"}`)
	got := denialToPattern(raw)
	if got != "Bash(npm *)" {
		t.Errorf("got %q, want %q", got, "Bash(npm *)")
	}
}

func TestDenialToPattern_BashWithToolInput(t *testing.T) {
	raw := json.RawMessage(`{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`)
	got := denialToPattern(raw)
	if got != "Bash(go *)" {
		t.Errorf("got %q, want %q", got, "Bash(go *)")
	}
}

func TestDenialToPattern_BashNoCommand(t *testing.T) {
	raw := json.RawMessage(`{"tool_name":"Bash"}`)
	got := denialToPattern(raw)
	if got != "Bash" {
		t.Errorf("got %q, want %q", got, "Bash")
	}
}

func TestDenialToPattern_PlainString(t *testing.T) {
	raw := json.RawMessage(`"Edit"`)
	got := denialToPattern(raw)
	if got != "Edit" {
		t.Errorf("got %q, want %q", got, "Edit")
	}
}

func TestDenialToPattern_EmptyObject(t *testing.T) {
	raw := json.RawMessage(`{}`)
	got := denialToPattern(raw)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestDenialToPattern_EmptyString(t *testing.T) {
	raw := json.RawMessage(`""`)
	got := denialToPattern(raw)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// --- extractDeniedToolPatterns tests ---

func TestExtractDeniedToolPatterns_Mixed(t *testing.T) {
	result := &agentutil.AgentResult{
		PermissionDenials: []json.RawMessage{
			json.RawMessage(`{"tool_name":"Bash","command":"npm test"}`),
			json.RawMessage(`"Write"`),
			json.RawMessage(`{"tool_name":"Read"}`),
		},
	}
	patterns := extractDeniedToolPatterns(result)
	if len(patterns) != 3 {
		t.Fatalf("got %d patterns, want 3: %v", len(patterns), patterns)
	}

	expected := map[string]bool{"Bash(npm *)": true, "Write": true, "Read": true}
	for _, p := range patterns {
		if !expected[p] {
			t.Errorf("unexpected pattern: %q", p)
		}
	}
}

func TestExtractDeniedToolPatterns_Dedup(t *testing.T) {
	result := &agentutil.AgentResult{
		PermissionDenials: []json.RawMessage{
			json.RawMessage(`"Bash"`),
			json.RawMessage(`"Bash"`),
			json.RawMessage(`"Read"`),
		},
	}
	patterns := extractDeniedToolPatterns(result)
	if len(patterns) != 2 {
		t.Errorf("got %d patterns, want 2 (dedup): %v", len(patterns), patterns)
	}
}

func TestExtractDeniedToolPatterns_Nil(t *testing.T) {
	patterns := extractDeniedToolPatterns(nil)
	if patterns != nil {
		t.Errorf("expected nil, got %v", patterns)
	}
}

func TestExtractDeniedToolPatterns_Empty(t *testing.T) {
	result := &agentutil.AgentResult{}
	patterns := extractDeniedToolPatterns(result)
	if patterns != nil {
		t.Errorf("expected nil, got %v", patterns)
	}
}

// --- containsTool tests ---

func TestContainsTool(t *testing.T) {
	tools := []string{"Bash(go *)", "Read", "Edit"}
	if !containsTool(tools, "Read") {
		t.Error("should find Read")
	}
	if containsTool(tools, "Write") {
		t.Error("should not find Write")
	}
	if containsTool(tools, "Bash") {
		t.Error("should not match partial pattern")
	}
}

// --- Validate integration tests (with mock runner) ---

func TestValidate_PassOnFirstAttempt(t *testing.T) {
	runner := &mockRunner{
		attempts: []mockAttempt{
			{exitCode: 0, logContent: successLog(3, 0.08)},
		},
	}

	cfg := ValidateConfig{
		RepoDir:      "/tmp/testrepo",
		AllowedTools: []string{"Bash(go *)", "Read"},
		TestCommand:  "go test ./...",
		LogDir:       t.TempDir(),
	}

	result, err := Validate(context.Background(), cfg, runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected pass")
	}
	if result.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", result.Attempts)
	}
	if len(result.DeniedTools) != 0 {
		t.Errorf("DeniedTools = %v, want empty", result.DeniedTools)
	}
	if len(result.FinalTools) != 2 {
		t.Errorf("FinalTools len = %d, want 2", len(result.FinalTools))
	}
}

func TestValidate_PassAfterRefinement(t *testing.T) {
	runner := &mockRunner{
		attempts: []mockAttempt{
			// First attempt: permission denied for Write.
			{exitCode: 0, logContent: permissionDenialLog(`{"tool_name":"Write"}`)},
			// Second attempt: success with Write added.
			{exitCode: 0, logContent: successLog(4, 0.15)},
		},
	}

	cfg := ValidateConfig{
		RepoDir:      "/tmp/testrepo",
		AllowedTools: []string{"Read", "Edit"},
		TestCommand:  "go test ./...",
		LogDir:       t.TempDir(),
	}

	result, err := Validate(context.Background(), cfg, runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected pass after refinement")
	}
	if result.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", result.Attempts)
	}
	if len(result.DeniedTools) != 1 || result.DeniedTools[0] != "Write" {
		t.Errorf("DeniedTools = %v, want [Write]", result.DeniedTools)
	}
	// FinalTools should include the original 2 + added Write.
	if len(result.FinalTools) != 3 {
		t.Errorf("FinalTools len = %d, want 3", len(result.FinalTools))
	}

	// Second call should have Write in --allowedTools.
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(runner.calls))
	}
	secondArgs := strings.Join(runner.calls[1].args, " ")
	if !strings.Contains(secondArgs, "Write") {
		t.Error("second attempt should include Write in args")
	}
}

func TestValidate_FailAfterMaxAttempts(t *testing.T) {
	// Each attempt denies a different tool; eventually hit MaxAttempts.
	runner := &mockRunner{
		attempts: []mockAttempt{
			{exitCode: 0, logContent: permissionDenialLog(`{"tool_name":"Write"}`)},
			{exitCode: 0, logContent: permissionDenialLog(`{"tool_name":"Glob"}`)},
			{exitCode: 0, logContent: permissionDenialLog(`{"tool_name":"Grep"}`)},
		},
	}

	cfg := ValidateConfig{
		RepoDir:      "/tmp/testrepo",
		AllowedTools: []string{"Read"},
		LogDir:       t.TempDir(),
	}

	result, err := Validate(context.Background(), cfg, runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("should not pass when max attempts exhausted")
	}
	if result.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", result.Attempts)
	}
	if len(result.DeniedTools) != 3 {
		t.Errorf("DeniedTools len = %d, want 3", len(result.DeniedTools))
	}
	// FinalTools should have original + 3 added.
	if len(result.FinalTools) != 4 {
		t.Errorf("FinalTools len = %d, want 4", len(result.FinalTools))
	}
}

func TestValidate_NonPermissionFailure(t *testing.T) {
	runner := &mockRunner{
		attempts: []mockAttempt{
			{exitCode: 1, logContent: errorLog("network timeout")},
		},
	}

	cfg := ValidateConfig{
		RepoDir:      "/tmp/testrepo",
		AllowedTools: []string{"Read"},
		LogDir:       t.TempDir(),
	}

	result, err := Validate(context.Background(), cfg, runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("should not pass on error")
	}
	if result.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1 (should not retry non-permission failure)", result.Attempts)
	}
	if len(result.Failures) == 0 {
		t.Error("should have failure descriptions")
	}
}

func TestValidate_RunnerError(t *testing.T) {
	runner := &mockRunner{
		attempts: []mockAttempt{
			{err: fmt.Errorf("claude not found")},
		},
	}

	cfg := ValidateConfig{
		RepoDir:      "/tmp/testrepo",
		AllowedTools: []string{"Read"},
		LogDir:       t.TempDir(),
	}

	result, err := Validate(context.Background(), cfg, runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("should not pass when runner fails")
	}
	if len(result.Failures) == 0 {
		t.Error("should record failure")
	}
}

func TestValidate_AlreadyAllowedTool(t *testing.T) {
	// Denied tool is already in the allowed list — should stop retrying.
	runner := &mockRunner{
		attempts: []mockAttempt{
			{exitCode: 0, logContent: permissionDenialLog(`"Read"`)},
		},
	}

	cfg := ValidateConfig{
		RepoDir:      "/tmp/testrepo",
		AllowedTools: []string{"Read", "Edit"},
		LogDir:       t.TempDir(),
	}

	result, err := Validate(context.Background(), cfg, runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("should not pass")
	}
	if result.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1 (should not retry already-allowed tools)", result.Attempts)
	}
}

func TestValidate_UnidentifiableDenial(t *testing.T) {
	// Permission denial with empty/unparseble tool entries.
	runner := &mockRunner{
		attempts: []mockAttempt{
			{exitCode: 0, logContent: permissionDenialLog(`{}`)},
		},
	}

	cfg := ValidateConfig{
		RepoDir:      "/tmp/testrepo",
		AllowedTools: []string{"Read"},
		LogDir:       t.TempDir(),
	}

	result, err := Validate(context.Background(), cfg, runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("should not pass when can't identify denied tools")
	}
	if result.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", result.Attempts)
	}
}

func TestValidate_DefaultLogDir(t *testing.T) {
	runner := &mockRunner{
		attempts: []mockAttempt{
			{exitCode: 0, logContent: successLog(1, 0.01)},
		},
	}

	cfg := ValidateConfig{
		RepoDir:      "/tmp/testrepo",
		AllowedTools: []string{"Read"},
	}

	result, err := Validate(context.Background(), cfg, runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected pass")
	}
}

// --- ApplyResult tests ---

func TestApplyResult_Pass(t *testing.T) {
	f := New(Inventory{Languages: []string{"go"}})
	result := &ValidateResult{
		Passed:     true,
		Attempts:   1,
		FinalTools: []string{"Bash(go *)", "Read", "Edit"},
	}

	ApplyResult(f, result)

	if f.Validation.Status != "pass" {
		t.Errorf("Status = %q, want %q", f.Validation.Status, "pass")
	}
	if f.Validation.LastRun == nil {
		t.Error("LastRun should be set")
	}
	if len(f.Validation.Failures) != 0 {
		t.Errorf("Failures = %v, want empty", f.Validation.Failures)
	}
	if len(f.Permissions.AllowedTools) != 3 {
		t.Errorf("AllowedTools len = %d, want 3", len(f.Permissions.AllowedTools))
	}
}

func TestApplyResult_Fail(t *testing.T) {
	f := New(Inventory{Languages: []string{"go"}})
	f.Permissions.AllowedTools = []string{"Read"}

	result := &ValidateResult{
		Passed:      false,
		Attempts:    3,
		Failures:    []string{"attempt 1: added tools: Write", "attempt 2: added tools: Glob", "attempt 3: permissions"},
		FinalTools:  []string{"Read", "Write", "Glob"},
		DeniedTools: []string{"Write", "Glob"},
	}

	ApplyResult(f, result)

	if f.Validation.Status != "fail" {
		t.Errorf("Status = %q, want %q", f.Validation.Status, "fail")
	}
	if len(f.Validation.Failures) != 3 {
		t.Errorf("Failures len = %d, want 3", len(f.Validation.Failures))
	}
	if len(f.Permissions.AllowedTools) != 3 {
		t.Errorf("AllowedTools len = %d, want 3 (should include refinements)", len(f.Permissions.AllowedTools))
	}
}

func TestApplyResult_EmptyFinalTools(t *testing.T) {
	f := New(Inventory{Languages: []string{"go"}})
	f.Permissions.AllowedTools = []string{"Read", "Edit"}

	result := &ValidateResult{
		Passed: false,
	}

	ApplyResult(f, result)

	// Should not clobber existing tools when FinalTools is empty.
	if len(f.Permissions.AllowedTools) != 2 {
		t.Errorf("AllowedTools len = %d, want 2 (should preserve existing)", len(f.Permissions.AllowedTools))
	}
}

func TestValidate_BashCommandRefinement(t *testing.T) {
	// Verify that Bash command patterns are properly extracted and added.
	runner := &mockRunner{
		attempts: []mockAttempt{
			{exitCode: 0, logContent: permissionDenialLog(
				`{"tool_name":"Bash","command":"npm run test"}`,
			)},
			{exitCode: 0, logContent: successLog(3, 0.10)},
		},
	}

	cfg := ValidateConfig{
		RepoDir:      "/tmp/testrepo",
		AllowedTools: []string{"Read"},
		TestCommand:  "npm test",
		LogDir:       t.TempDir(),
	}

	result, err := Validate(context.Background(), cfg, runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected pass after refinement")
	}
	if !containsTool(result.FinalTools, "Bash(npm *)") {
		t.Errorf("FinalTools = %v, should contain Bash(npm *)", result.FinalTools)
	}
}

func TestValidate_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	runner := &mockRunner{
		attempts: []mockAttempt{
			{err: fmt.Errorf("context canceled")},
		},
	}

	cfg := ValidateConfig{
		RepoDir:      "/tmp/testrepo",
		AllowedTools: []string{"Read"},
		LogDir:       t.TempDir(),
	}

	result, err := Validate(ctx, cfg, runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("should not pass on context cancellation")
	}
}
