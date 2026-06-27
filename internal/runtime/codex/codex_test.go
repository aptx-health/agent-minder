package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

func TestName(t *testing.T) {
	if got := New().Name(); got != "codex" {
		t.Errorf("Name() = %q, want %q", got, "codex")
	}
}

func TestPrepareAgentDefWritesAgentsMD(t *testing.T) {
	dir := t.TempDir()
	body := []byte("---\nname: autopilot\n---\nship carefully")

	err := New().PrepareAgentDef(context.Background(), runtime.Workspace{Dir: dir}, runtime.AgentDefinition{
		Name: "autopilot",
		Body: body,
	})
	if err != nil {
		t.Fatalf("PrepareAgentDef: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("AGENTS.md = %q, want %q", got, body)
	}
}

func TestPrepareAgentDefOverwritesExistingAgentsMD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := []byte("new contract")
	if err := New().PrepareAgentDef(context.Background(), runtime.Workspace{Dir: dir}, runtime.AgentDefinition{Name: "reviewer", Body: body}); err != nil {
		t.Fatalf("PrepareAgentDef: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("AGENTS.md = %q, want %q", got, body)
	}
}

func TestPrepareAgentDefNoWorkspace(t *testing.T) {
	err := New().PrepareAgentDef(context.Background(), runtime.Workspace{}, runtime.AgentDefinition{Name: "x", Body: []byte("y")})
	if err != nil {
		t.Errorf("expected nil for empty workspace, got %v", err)
	}
}

func TestPrepareAgentDefErrors(t *testing.T) {
	dir := t.TempDir()
	if err := New().PrepareAgentDef(context.Background(), runtime.Workspace{Dir: dir}, runtime.AgentDefinition{Name: "", Body: []byte("x")}); err == nil {
		t.Error("expected error for empty name")
	}
	if err := New().PrepareAgentDef(context.Background(), runtime.Workspace{Dir: dir}, runtime.AgentDefinition{Name: "x"}); err == nil {
		t.Error("expected error for empty body")
	}
}

func TestBuildArgs(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	args := buildArgs(runtime.Invocation{
		Workspace:    runtime.Workspace{Dir: dir},
		AgentName:    "autopilot",
		Prompt:       "do the thing",
		SystemPrompt: "lessons here",
		AllowedTools: []string{"Read", "Edit"},
		Limits:       runtime.Limits{MaxTurns: 50, MaxBudgetUSD: 2.50},
	})

	want := []string{
		"--ask-for-approval", "never",
		"exec",
		"--json",
		"--cd", dir,
		"--sandbox", "workspace-write",
		"-c", "sandbox_workspace_write.network_access=true",
		"--add-dir", gitDir,
		"-c", `developer_instructions="lessons here"`,
		"do the thing",
	}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("buildArgs() = %#v\nwant %#v", args, want)
	}
}

func TestBuildArgsAddsLinkedWorktreeGitDirs(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, "..", "repo", ".git", "worktrees", "issue-2")
	commonDir := filepath.Join(dir, "..", "repo", ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	args := buildArgs(runtime.Invocation{Workspace: runtime.Workspace{Dir: dir}, Prompt: "p"})
	got := strings.Join(args, "\x00")
	for _, want := range []string{filepath.Clean(gitDir), filepath.Clean(commonDir)} {
		if !strings.Contains(got, "--add-dir\x00"+want) {
			t.Errorf("args missing --add-dir %q: %#v", want, args)
		}
	}
}

func TestBuildArgs_OmitsWorkspaceAndSystemPrompt(t *testing.T) {
	args := buildArgs(runtime.Invocation{Prompt: "p"})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--cd") {
		t.Errorf("empty workspace should not emit --cd: %v", args)
	}
	if strings.Contains(joined, "developer_instructions") {
		t.Errorf("empty SystemPrompt should not emit config: %v", args)
	}
	if args[len(args)-1] != "p" {
		t.Errorf("prompt should be last: %v", args)
	}
}

func TestRunUsesExecCommandShape(t *testing.T) {
	var gotArgs []string
	var gotWorkDir string
	var gotEnv map[string]string
	var gotLog string

	r := New()
	r.execFunc = func(_ context.Context, args []string, workDir string, env map[string]string, _ runtime.Limits, _ runtime.EventSink, logFile io.Writer) (int, error) {
		gotArgs = append([]string(nil), args...)
		gotWorkDir = workDir
		gotEnv = env
		_, _ = logFile.Write([]byte(`{"type":"thread.started"}` + "\n"))
		return 0, nil
	}

	var log bytes.Buffer
	exitCode, err := r.Run(context.Background(), runtime.Invocation{
		Workspace: runtime.Workspace{Dir: "/tmp/worktree"},
		Prompt:    "ship it",
		Env:       map[string]string{"GITHUB_TOKEN": "token"},
	}, nil, &log)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if gotWorkDir != "/tmp/worktree" {
		t.Errorf("workDir = %q, want /tmp/worktree", gotWorkDir)
	}
	if gotEnv["GITHUB_TOKEN"] != "token" {
		t.Errorf("env not passed through: %v", gotEnv)
	}
	if gotLog = log.String(); gotLog == "" {
		t.Error("expected fake exec to write log output")
	}

	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"--ask-for-approval never", "exec", "--json", "--cd /tmp/worktree", "--sandbox workspace-write", "sandbox_workspace_write.network_access=true", "ship it"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, gotArgs)
		}
	}
}

func TestResumeUsesExecResume(t *testing.T) {
	var gotArgs []string
	r := New()
	r.execFunc = func(_ context.Context, args []string, _ string, _ map[string]string, _ runtime.Limits, _ runtime.EventSink, _ io.Writer) (int, error) {
		gotArgs = append([]string(nil), args...)
		return 0, nil
	}

	if _, err := r.Resume(context.Background(), "thread-123", nil, nil); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	want := []string{"exec", "resume", "thread-123", "--json"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("Resume args = %#v, want %#v", gotArgs, want)
	}
}

func TestResumeEmptySessionID(t *testing.T) {
	if _, err := New().Resume(context.Background(), "", nil, nil); err == nil {
		t.Fatal("expected error for empty session id")
	}
}

func TestParseResultSuccess(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "codex.jsonl")
	lines := []string{
		`{"type":"thread.started","thread_id":"thread-1","model":"gpt-5.4-mini"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"all done"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1000000,"cached_input_tokens":100000,"output_tokens":100000}}`,
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := New().ParseResult(logPath)
	if err != nil {
		t.Fatalf("ParseResult: %v", err)
	}
	if got == nil {
		t.Fatal("expected result, got nil")
	}
	if got.SessionID != "thread-1" {
		t.Errorf("SessionID = %q, want thread-1", got.SessionID)
	}
	if got.NumTurns != 1 {
		t.Errorf("NumTurns = %d, want 1", got.NumTurns)
	}
	if got.FinalText != "all done" {
		t.Errorf("FinalText = %q, want all done", got.FinalText)
	}
	if got.IsError {
		t.Error("IsError = true, want false")
	}
	if got.TotalCostUSD != 1.1325 {
		t.Errorf("TotalCostUSD = %.4f, want 1.1325", got.TotalCostUSD)
	}
	if len(got.Native) == 0 {
		t.Error("Native raw is empty")
	}
}

func TestParseResultErrorEvent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "codex.jsonl")
	if err := os.WriteFile(logPath, []byte(`{"type":"turn.failed","error":{"message":"boom"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New().ParseResult(logPath)
	if err != nil {
		t.Fatalf("ParseResult: %v", err)
	}
	if got == nil || !got.IsError {
		t.Fatalf("expected error result, got %+v", got)
	}
	if !strings.Contains(got.StopReason, "boom") {
		t.Errorf("StopReason = %q, want boom", got.StopReason)
	}
}

func TestParseResultEmptyPathReturnsNil(t *testing.T) {
	got, err := New().ParseResult("")
	if err != nil || got != nil {
		t.Errorf("ParseResult(\"\") = (%v, %v), want (nil, nil)", got, err)
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
			name:   "nil",
			result: nil,
			want:   runtime.Outcome{},
		},
		{
			name:   "ok",
			result: &runtime.Result{NumTurns: 1, TotalCostUSD: 0.01},
			limits: runtime.Limits{MaxTurns: 10, MaxBudgetUSD: 1},
			want:   runtime.Outcome{},
		},
		{
			name:   "max turns",
			result: &runtime.Result{NumTurns: 10},
			limits: runtime.Limits{MaxTurns: 10},
			want:   runtime.Outcome{Status: "failed", FailureReason: "max_turns", FailureDetail: "used 10 of 10 turns"},
		},
		{
			name:   "max budget",
			result: &runtime.Result{TotalCostUSD: 0.95},
			limits: runtime.Limits{MaxBudgetUSD: 1},
			want:   runtime.Outcome{Status: "failed", FailureReason: "max_budget", FailureDetail: "spent $0.95 of $1.00 budget"},
		},
		{
			name:   "error",
			result: &runtime.Result{IsError: true, StopReason: "turn.failed: boom"},
			want:   runtime.Outcome{Status: "failed", FailureReason: "error", FailureDetail: "turn.failed: boom"},
		},
		{
			name:   "usage limit",
			result: &runtime.Result{IsError: true, StopReason: "turn.failed: rate limit exceeded"},
			want:   runtime.Outcome{Status: "failed", FailureReason: "usage_limit", FailureDetail: "turn.failed: rate limit exceeded"},
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

func TestExtractBailReport(t *testing.T) {
	bailJSON := `{"reason":"too large","files_examined":["a.go"],"plan":"split into smaller issues","complexity":"large"}`
	finalText := "cannot finish\n<bail-report>\n" + bailJSON + "\n</bail-report>"

	rep := New().ExtractBailReport(&runtime.Result{FinalText: finalText}, "")
	if rep == nil {
		t.Fatal("expected report")
	}
	if rep.Reason != "too large" {
		t.Errorf("Reason = %q, want too large", rep.Reason)
	}
	if rep.Detail != "split into smaller issues" {
		t.Errorf("Detail = %q", rep.Detail)
	}
}

func TestExtractBailReportLogFallback(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "codex.jsonl")
	finalText := `<bail-report>{"reason":"blocked","files_examined":[],"plan":"ask human","complexity":"small"}</bail-report>`
	line := `{"type":"item.completed","item":{"type":"agent_message","text":` + encodeJSONString(t, finalText) + `}}`
	if err := os.WriteFile(logPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := New().ExtractBailReport(&runtime.Result{}, logPath)
	if rep == nil {
		t.Fatal("expected fallback report")
	}
	if rep.Reason != "blocked" {
		t.Errorf("Reason = %q, want blocked", rep.Reason)
	}
}

func encodeJSONString(t *testing.T, s string) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(buf.String())
}
