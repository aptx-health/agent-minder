// Package claudecode is the Claude Code implementation of runtime.AgentRuntime.
//
// It wraps the `claude` CLI (`claude --agent <name> -p --output-format stream-json`),
// materializes agent definitions as `.claude/agents/<name>.md`, scans stream-json
// for live tool/step events, parses the final result event, classifies the outcome
// against the run's Limits, and extracts the Minder-defined <bail-report> JSON from
// the final assistant message (with raw-log fallback).
//
// No supervisor types leak into this package — translation happens at the seam.
package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

// Name is the canonical identifier for this runtime.
const Name = "claude-code"

// ClaudeRuntime implements runtime.AgentRuntime for the Claude Code CLI.
//
// All fields are optional. The zero value uses the `claude` binary on PATH and
// the system environment.
type ClaudeRuntime struct {
	// Bin overrides the path to the claude binary (default: "claude").
	Bin string
}

// New constructs a ClaudeRuntime with default settings.
func New() *ClaudeRuntime { return &ClaudeRuntime{} }

func init() {
	runtime.Register(Name, func() runtime.AgentRuntime { return New() })
}

// Name returns the runtime identifier.
func (c *ClaudeRuntime) Name() string { return Name }

// binPath returns the configured binary path or the default.
func (c *ClaudeRuntime) binPath() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "claude"
}

// PrepareAgentDef writes the agent definition body to
// `<workspace>/.claude/agents/<name>.md`. Idempotent: overwrites if the file
// already exists so updates to the embedded contract are picked up.
//
// If the workspace directory is empty (non-worktree agents that run from the
// repo root with no PrepareAgentDef step), this is a no-op.
func (c *ClaudeRuntime) PrepareAgentDef(_ context.Context, ws runtime.Workspace, def runtime.AgentDefinition) error {
	if ws.Dir == "" {
		return nil
	}
	if def.Name == "" {
		return fmt.Errorf("claudecode: PrepareAgentDef: empty agent name")
	}
	if len(def.Body) == 0 {
		return fmt.Errorf("claudecode: PrepareAgentDef: empty body for %q", def.Name)
	}
	dir := filepath.Join(ws.Dir, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("claudecode: create agent dir: %w", err)
	}
	path := filepath.Join(dir, def.Name+".md")
	if err := os.WriteFile(path, def.Body, 0o644); err != nil {
		return fmt.Errorf("claudecode: write agent def: %w", err)
	}
	return nil
}

// Run executes `claude --agent <name> -p --output-format stream-json ...`,
// streams stream-json lines to logFile, and forwards normalized events to sink.
//
// The process inherits the parent environment, then applies inv.Env on top.
// GITHUB_TOKEN and similar credentials should be passed via inv.Env.
func (c *ClaudeRuntime) Run(ctx context.Context, inv runtime.Invocation, sink runtime.EventSink, logFile io.Writer) (int, error) {
	args := buildArgs(inv)
	return c.exec(ctx, args, inv.Workspace.Dir, inv.Env, sink, logFile)
}

// Resume restarts a session by ID using `claude --resume <id> --output-format stream-json`.
// Workspace, env, and event-sink semantics match Run.
//
// Resume does not re-apply the agent name, prompt, system prompt, allowed tools,
// or limits — the resumed session already has them. Callers that need fresh
// limits or a different prompt should use Run.
func (c *ClaudeRuntime) Resume(ctx context.Context, sessionID string, sink runtime.EventSink, logFile io.Writer) (int, error) {
	if sessionID == "" {
		return 0, fmt.Errorf("claudecode: Resume: empty session id")
	}
	args := []string{"--resume", sessionID, "--output-format", "stream-json", "--verbose"}
	// Workspace and env are unknown at Resume time; the caller's working dir is
	// used. This matches today's supervisor behavior, which calls --resume from
	// the same SlotContext that ran the original stage.
	return c.exec(ctx, args, "", nil, sink, logFile)
}

// exec is the shared process-runner used by Run and Resume. It pipes stdout
// through the stream scanner (which writes lossless lines to logFile and pushes
// normalized events to sink) and waits for the process to exit.
func (c *ClaudeRuntime) exec(ctx context.Context, args []string, workDir string, env map[string]string, sink runtime.EventSink, logFile io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, c.binPath(), args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Stderr = logFile

	if len(env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("claudecode: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("claudecode: start: %w", err)
	}

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanStream(stdout, logFile, sink)
	}()

	waitErr := cmd.Wait()
	<-scanDone

	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return 0, fmt.Errorf("claudecode: wait: %w", waitErr)
		}
	}
	return exitCode, nil
}

// buildArgs constructs the CLI argv for a fresh Run.
//
// Effective flags (matching the supervisor's current invocation in
// `internal/supervisor/prompt.go:buildAgentArgs`):
//
//	--agent <name> -p --output-format stream-json --verbose
//	[--model <name>] --max-turns N --max-budget-usd F --allowedTools <csv>
//	[--append-system-prompt <text>]
//	-- <prompt>
func buildArgs(inv runtime.Invocation) []string {
	args := []string{
		"--agent", inv.AgentName,
		"-p",
		"--output-format", "stream-json",
		"--verbose",
	}
	if model := runtime.NormalizeModelName(inv.Model); model != "" {
		args = append(args, "--model", model)
	}
	if inv.Limits.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(inv.Limits.MaxTurns))
	}
	if inv.Limits.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", inv.Limits.MaxBudgetUSD))
	}
	if len(inv.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(inv.AllowedTools, ","))
	}
	if inv.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", inv.SystemPrompt)
	}
	args = append(args, "--", inv.Prompt)
	return args
}

// ParseResult reads a Claude Code stream-json log and extracts the result event,
// returning a normalized runtime.Result.
//
// Returns (nil, nil) if logPath is empty or contains no result event (e.g., the
// agent crashed before emitting one).
func (c *ClaudeRuntime) ParseResult(logPath string) (*runtime.Result, error) {
	evt, raw, err := readResultEvent(logPath)
	if err != nil {
		return nil, err
	}
	if evt == nil {
		return nil, nil
	}

	denials := make([]string, 0, len(evt.PermissionDenials))
	for _, d := range evt.PermissionDenials {
		denials = append(denials, string(d))
	}

	return &runtime.Result{
		SessionID:         evt.SessionID,
		NumTurns:          evt.NumTurns,
		TotalCostUSD:      evt.TotalCost,
		FinalText:         evt.Result,
		IsError:           evt.IsError,
		StopReason:        string(evt.StopReason),
		PermissionDenials: denials,
		Native:            raw,
	}, nil
}

// ClassifyOutcome maps a Result + Limits to a normalized Outcome.
//
// Lifts the existing logic from `internal/supervisor/outcome.go:classifyOutcome`:
// turn-limit exhaustion, budget exhaustion (>=95%), explicit error flag, then
// permission denials as a non-fatal warning.
//
// An empty Outcome.Status means no failure was detected and the caller should
// proceed (e.g., check for a PR).
func (c *ClaudeRuntime) ClassifyOutcome(r *runtime.Result, limits runtime.Limits) runtime.Outcome {
	if r == nil {
		return runtime.Outcome{}
	}

	if limits.MaxTurns > 0 && r.NumTurns >= limits.MaxTurns {
		return runtime.Outcome{
			Status:        "failed",
			FailureReason: "max_turns",
			FailureDetail: fmt.Sprintf("used %d of %d turns", r.NumTurns, limits.MaxTurns),
		}
	}

	if limits.MaxBudgetUSD > 0 && r.TotalCostUSD >= limits.MaxBudgetUSD*0.95 {
		return runtime.Outcome{
			Status:        "failed",
			FailureReason: "max_budget",
			FailureDetail: fmt.Sprintf("spent $%.2f of $%.2f budget", r.TotalCostUSD, limits.MaxBudgetUSD),
		}
	}

	if r.IsError {
		detail := r.FinalText
		if len(detail) > 500 {
			detail = detail[:500] + "..."
		}
		return runtime.Outcome{Status: "failed", FailureReason: "error", FailureDetail: detail}
	}

	if len(r.PermissionDenials) > 0 {
		detail, _ := json.Marshal(r.PermissionDenials)
		return runtime.Outcome{Status: "warning", FailureReason: "permissions", FailureDetail: string(detail)}
	}

	return runtime.Outcome{}
}

// ExtractBailReport finds a <bail-report> JSON block, first in the result's
// FinalText (where the agent is instructed to emit it) and then by scanning the
// raw stream-json log (the agent may have embedded it in a tool call such as
// `gh issue comment --body-file`).
//
// Returns nil if no valid report is present.
func (c *ClaudeRuntime) ExtractBailReport(r *runtime.Result, logPath string) *runtime.BailReport {
	if r != nil && r.FinalText != "" {
		if rep := extractBailReportFromText(r.FinalText); rep != nil {
			return rep
		}
	}
	return extractBailReportFromLog(logPath)
}
