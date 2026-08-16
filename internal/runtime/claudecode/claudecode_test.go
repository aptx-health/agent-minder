package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

func TestName(t *testing.T) {
	if got := (&ClaudeRuntime{}).Name(); got != "claude-code" {
		t.Errorf("Name() = %q, want %q", got, "claude-code")
	}
}

func TestPrepareAgentDef_WritesFile(t *testing.T) {
	dir := t.TempDir()
	r := New()
	body := []byte("---\nname: autopilot\n---\nhello")
	err := r.PrepareAgentDef(context.Background(), runtime.Workspace{Dir: dir}, runtime.AgentDefinition{
		Name: "autopilot",
		Body: body,
	})
	if err != nil {
		t.Fatalf("PrepareAgentDef: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".claude", "agents", "autopilot.md"))
	if err != nil {
		t.Fatalf("read written def: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("written body = %q, want %q", got, body)
	}
}

func TestPrepareAgentDef_NoWorkspace(t *testing.T) {
	r := New()
	err := r.PrepareAgentDef(context.Background(), runtime.Workspace{}, runtime.AgentDefinition{Name: "x", Body: []byte("y")})
	if err != nil {
		t.Errorf("expected nil for empty workspace, got %v", err)
	}
}

func TestPrepareAgentDef_Errors(t *testing.T) {
	r := New()
	ctx := context.Background()
	dir := t.TempDir()
	if err := r.PrepareAgentDef(ctx, runtime.Workspace{Dir: dir}, runtime.AgentDefinition{Name: "", Body: []byte("x")}); err == nil {
		t.Error("expected error for empty name")
	}
	if err := r.PrepareAgentDef(ctx, runtime.Workspace{Dir: dir}, runtime.AgentDefinition{Name: "x", Body: nil}); err == nil {
		t.Error("expected error for empty body")
	}
}

func TestBuildArgs(t *testing.T) {
	args := buildArgs(runtime.Invocation{
		AgentName:    "autopilot",
		Model:        "opus",
		Prompt:       "do the thing",
		SystemPrompt: "lessons here",
		AllowedTools: []string{"Read", "Edit", "Bash(git:*)"},
		Limits:       runtime.Limits{MaxTurns: 50, MaxBudgetUSD: 2.50},
	})
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--agent autopilot",
		"-p",
		"--output-format stream-json",
		"--verbose",
		"--model opus",
		"--max-turns 50",
		"--max-budget-usd 2.50",
		"--allowedTools Read,Edit,Bash(git:*)",
		"--append-system-prompt lessons here",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	// Prompt must come last after the `--` separator.
	if args[len(args)-1] != "do the thing" || args[len(args)-2] != "--" {
		t.Errorf("expected prompt to follow `--` at end: %v", args[len(args)-3:])
	}
}

func TestModelOverrideReachesCLIInvocation(t *testing.T) {
	args := buildArgs(runtime.Invocation{
		AgentName: "autopilot",
		Model:     " claude-sonnet-4-20250514 ",
		Prompt:    "do the thing",
	})
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--model" && args[i+1] == "claude-sonnet-4-20250514" {
			return
		}
	}
	t.Fatalf("model override did not reach Claude CLI args: %v", args)
}

func TestBuildArgs_OmitsZeroLimits(t *testing.T) {
	args := buildArgs(runtime.Invocation{AgentName: "x", Prompt: "p"})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--max-turns") {
		t.Errorf("zero MaxTurns should not emit flag: %v", args)
	}
	if strings.Contains(joined, "--max-budget-usd") {
		t.Errorf("zero MaxBudgetUSD should not emit flag: %v", args)
	}
	if strings.Contains(joined, "--allowedTools") {
		t.Errorf("empty AllowedTools should not emit flag: %v", args)
	}
	if strings.Contains(joined, "--append-system-prompt") {
		t.Errorf("empty SystemPrompt should not emit flag: %v", args)
	}
}

func TestClassifyOutcome(t *testing.T) {
	r := New()

	tests := []struct {
		name   string
		result *runtime.Result
		limits runtime.Limits
		want   runtime.Outcome
	}{
		{
			name:   "nil result",
			result: nil,
			want:   runtime.Outcome{},
		},
		{
			name:   "no failure",
			result: &runtime.Result{NumTurns: 5, TotalCostUSD: 0.10},
			limits: runtime.Limits{MaxTurns: 50, MaxBudgetUSD: 2.50},
			want:   runtime.Outcome{},
		},
		{
			name:   "turn limit hit",
			result: &runtime.Result{NumTurns: 50},
			limits: runtime.Limits{MaxTurns: 50},
			want:   runtime.Outcome{Status: "failed", FailureReason: "max_turns", FailureDetail: "used 50 of 50 turns"},
		},
		{
			name:   "budget hit at 95%",
			result: &runtime.Result{TotalCostUSD: 2.40},
			limits: runtime.Limits{MaxBudgetUSD: 2.50},
			want:   runtime.Outcome{Status: "failed", FailureReason: "max_budget", FailureDetail: "spent $2.40 of $2.50 budget"},
		},
		{
			name:   "error flag",
			result: &runtime.Result{IsError: true, FinalText: "boom"},
			want:   runtime.Outcome{Status: "failed", FailureReason: "error", FailureDetail: "boom"},
		},
		{
			name:   "permission denials warning",
			result: &runtime.Result{PermissionDenials: []string{`"tool":"X"`}},
			want:   runtime.Outcome{Status: "warning", FailureReason: "permissions", FailureDetail: `["\"tool\":\"X\""]`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.ClassifyOutcome(tt.result, tt.limits)
			if got != tt.want {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestParseResult(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log.jsonl")
	// One assistant event, then a result.
	lines := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"num_turns":3,"total_cost_usd":0.42,"session_id":"abc-123","model":"claude-sonnet-4-20250514","result":"all done"}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := New()
	got, err := r.ParseResult(logPath)
	if err != nil {
		t.Fatalf("ParseResult: %v", err)
	}
	if got == nil {
		t.Fatal("expected result, got nil")
	}
	if got.SessionID != "abc-123" {
		t.Errorf("SessionID = %q, want abc-123", got.SessionID)
	}
	if got.NumTurns != 3 {
		t.Errorf("NumTurns = %d, want 3", got.NumTurns)
	}
	if got.TotalCostUSD != 0.42 {
		t.Errorf("TotalCostUSD = %v, want 0.42", got.TotalCostUSD)
	}
	if got.FinalText != "all done" {
		t.Errorf("FinalText = %q", got.FinalText)
	}
	if got.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want claude-sonnet-4-20250514", got.Model)
	}
	if len(got.Native) == 0 {
		t.Error("Native raw is empty")
	}
}

func TestParseResult_EmptyPathReturnsNil(t *testing.T) {
	got, err := New().ParseResult("")
	if err != nil || got != nil {
		t.Errorf("ParseResult(\"\") = (%v, %v), want (nil, nil)", got, err)
	}
}

// TestExtractBailReport_StreamJSONLog is the issue's required acceptance test:
// bail-report extraction from a representative stream-json log fixture.
//
// The fixture mimics the real shape: a system init, an assistant tool call that
// is unrelated, then a final assistant text block containing the bail-report
// tags, and finally a result event whose `result` field also carries the bail
// text. We assert extraction works both from the parsed Result and from the
// raw-log fallback (after stripping result.result to force the fallback path).
func TestExtractBailReport_StreamJSONLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.jsonl")

	bailJSON := `{
  "reason": "Issue requires design decisions not specified",
  "files_examined": ["internal/auth/handler.go", "internal/auth/middleware.go"],
  "plan": "1. Define new auth flow\n2. Update middleware\n3. Add tests",
  "sub_issues": ["Refactor middleware", "Add JWT validation tests"],
  "complexity": "large"
}`
	finalText := "I explored the codebase but cannot complete this.\n\n<bail-report>\n" + bailJSON + "\n</bail-report>"

	// Write a stream-json log including a final assistant text block, then the
	// result event with the bail report in `result`. We hand-build the lines
	// with json.Marshal so escaping stays valid.
	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
		Name string `json:"name,omitempty"`
	}
	type message struct {
		Content []contentBlock `json:"content"`
	}
	type event struct {
		Type     string  `json:"type"`
		Subtype  string  `json:"subtype,omitempty"`
		Message  message `json:"message,omitempty"`
		IsError  bool    `json:"is_error,omitempty"`
		NumTurns int     `json:"num_turns,omitempty"`
		Cost     float64 `json:"total_cost_usd,omitempty"`
		Result   string  `json:"result,omitempty"`
		Session  string  `json:"session_id,omitempty"`
	}

	// Use a non-HTML-escaping encoder so `<bail-report>` survives literally in
	// the raw log — matching real Claude CLI output.
	encode := func(v any) string {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			t.Fatal(err)
		}
		return strings.TrimRight(buf.String(), "\n")
	}
	encodeString := func(s string) string {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(s); err != nil {
			t.Fatal(err)
		}
		return strings.TrimRight(buf.String(), "\n")
	}

	lines := []string{
		encode(event{Type: "system", Subtype: "init"}),
		encode(event{Type: "assistant", Message: message{Content: []contentBlock{{Type: "text", Text: "thinking..."}}}}),
		encode(event{Type: "assistant", Message: message{Content: []contentBlock{{Type: "text", Text: finalText}}}}),
		// Result event carries the bail report in result, like Claude does.
		`{"type":"result","subtype":"success","is_error":false,"num_turns":4,"total_cost_usd":0.31,"session_id":"sess-9","result":` + encodeString(finalText) + `}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := New()

	// Path 1: extract from parsed result FinalText.
	parsed, err := r.ParseResult(logPath)
	if err != nil {
		t.Fatalf("ParseResult: %v", err)
	}
	if parsed == nil {
		t.Fatal("expected parsed result")
	}
	rep := r.ExtractBailReport(parsed, logPath)
	if rep == nil {
		t.Fatal("expected bail report from FinalText, got nil")
	}
	if rep.Reason != "Issue requires design decisions not specified" {
		t.Errorf("Reason = %q", rep.Reason)
	}
	if !strings.Contains(rep.Detail, "Update middleware") {
		t.Errorf("Detail missing plan content: %q", rep.Detail)
	}
	if len(rep.Native) == 0 {
		t.Error("Native raw bail JSON is empty")
	}
	// Native should round-trip back to a JSON object with reason set.
	var probe map[string]any
	if err := json.Unmarshal(rep.Native, &probe); err != nil {
		t.Errorf("Native JSON invalid: %v", err)
	} else if probe["reason"] != "Issue requires design decisions not specified" {
		t.Errorf("Native reason mismatch: %v", probe["reason"])
	}

	// Path 2: force the raw-log fallback by clearing FinalText.
	parsed.FinalText = ""
	rep2 := r.ExtractBailReport(parsed, logPath)
	if rep2 == nil {
		t.Fatal("expected bail report via raw-log fallback, got nil")
	}
	if rep2.Reason != rep.Reason {
		t.Errorf("fallback Reason = %q, want %q", rep2.Reason, rep.Reason)
	}
}

func TestExtractBailReport_NoReport(t *testing.T) {
	r := New()
	if got := r.ExtractBailReport(&runtime.Result{FinalText: "no bail here"}, ""); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
	if got := r.ExtractBailReport(nil, ""); got != nil {
		t.Errorf("expected nil for nil result + empty path, got %+v", got)
	}
}

func TestExtractBailReport_LastMatchedPairWins(t *testing.T) {
	// A backtick-quoted mention of <bail-report> earlier in the message must
	// not shadow a real, well-formed report at the end.
	final := "Discussing the `<bail-report>` protocol earlier. " +
		"<bail-report>not valid json</bail-report> " +
		"\n\n<bail-report>\n" +
		`{"reason":"real bail","files_examined":[],"plan":"do x","complexity":"small"}` +
		"\n</bail-report>"
	rep := extractBailReportFromText(final)
	if rep == nil {
		t.Fatal("expected report, got nil")
	}
	if rep.Reason != "real bail" {
		t.Errorf("Reason = %q, want real bail", rep.Reason)
	}
}

// stubSink records the calls made by scanStream so we can assert event
// normalization end-to-end.
type stubSink struct {
	steps       int
	toolStarts  []string
	toolEnds    int
	usageLimits int
}

func (s *stubSink) OnAssistantStep()           { s.steps++ }
func (s *stubSink) OnToolStart(name, _ string) { s.toolStarts = append(s.toolStarts, name) }
func (s *stubSink) OnToolEnd()                 { s.toolEnds++ }
func (s *stubSink) OnUsageLimit()              { s.usageLimits++ }

func TestScanStream_NormalizesEvents(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"system","subtype":"api_retry","error":"rate_limit","error_status":429}`,
		`{"type":"result","subtype":"success","is_error":false,"num_turns":2}`,
	}
	input := strings.Join(lines, "\n") + "\n"

	var logBuf bytes.Buffer
	sink := &stubSink{}
	scanStream(strings.NewReader(input), &logBuf, sink)

	if sink.steps != 2 {
		t.Errorf("assistant steps = %d, want 2", sink.steps)
	}
	if len(sink.toolStarts) != 1 || sink.toolStarts[0] != "Bash" {
		t.Errorf("toolStarts = %v, want [Bash]", sink.toolStarts)
	}
	// One OnToolEnd for the no-tool assistant message, one for the result.
	if sink.toolEnds != 2 {
		t.Errorf("toolEnds = %d, want 2", sink.toolEnds)
	}
	if sink.usageLimits != 1 {
		t.Errorf("usageLimits = %d, want 1", sink.usageLimits)
	}
	if logBuf.Len() != len(input) {
		t.Errorf("log buffer len = %d, want %d (lossless mirror)", logBuf.Len(), len(input))
	}
}

func TestScanStream_NilSink(t *testing.T) {
	// nil sink must not panic; raw lines still mirror to logFile.
	input := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"
	var buf bytes.Buffer
	scanStream(strings.NewReader(input), &buf, nil)
	if buf.String() != input {
		t.Errorf("log buf = %q, want %q", buf.String(), input)
	}
}

func TestResume_EmptySessionID(t *testing.T) {
	_, err := New().Resume(context.Background(), "", nil, nil)
	if err == nil {
		t.Error("expected error for empty session id")
	}
}
