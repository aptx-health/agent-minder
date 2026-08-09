// Package codex is the Codex CLI implementation of runtime.AgentRuntime.
//
// It wraps `codex exec --json` for non-interactive doer runs in Minder-owned
// worktrees. Codex owns authentication through its normal CLI login state; this
// runtime only selects cwd, sandbox/approval policy, prompt, env, and logging.
//
// The runtime mirrors raw JSONL to the agent log, pushes normalized progress
// events to the supervisor, and synthesizes Minder's runtime.Result from the
// logged stream after the process exits.
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

// Name is the canonical identifier for this runtime.
const Name = runtime.NameCodex

// CodexRuntime implements runtime.AgentRuntime for the Codex CLI.
//
// All fields are optional. The zero value uses the `codex` binary on PATH and
// the system environment.
type CodexRuntime struct {
	// Bin overrides the path to the codex binary (default: "codex").
	Bin string

	execFunc func(context.Context, []string, string, map[string]string, runtime.Limits, runtime.EventSink, io.Writer) (int, error)
}

// New constructs a CodexRuntime with default settings.
func New() *CodexRuntime { return &CodexRuntime{} }

func init() {
	runtime.Register(Name, func() runtime.AgentRuntime { return New() })
}

// Name returns the runtime identifier.
func (c *CodexRuntime) Name() string { return Name }

// ResolveRunMetadata reports the normalized fresh-run model and best-effort
// Codex CLI version before process launch.
func (c *CodexRuntime) ResolveRunMetadata(ctx context.Context, inv runtime.Invocation) (runtime.RunMetadata, error) {
	version, err := runtime.CommandVersion(ctx, c.binPath(), "--version")
	return runtime.RunMetadata{
		RuntimeName:    c.Name(),
		Model:          runtime.NormalizeModelName(inv.Model),
		RuntimeVersion: version,
	}, err
}

// Capabilities reports the local Codex CLI and model catalog accepted by
// Minder's preflight.
func (c *CodexRuntime) Capabilities(ctx context.Context) (runtime.RuntimeCapabilities, error) {
	caps := runtime.ProbeCLIVersion(ctx, Name, c.binPath(), "--version")
	caps.Models = []string{
		"gpt-5",
		"gpt-5-codex",
		"gpt-5-mini",
		"gpt-5-nano",
		"o3",
		"o4-mini",
	}
	caps.ModelPrefixes = []string{"gpt-", "o3-", "o4-"}
	caps.Features = runtime.RuntimeFeatures{
		StructuredOutput: true,
		BudgetFlag:       false,
		SessionResume:    true,
	}
	return caps, nil
}

// binPath returns the configured binary path or the default.
func (c *CodexRuntime) binPath() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "codex"
}

// PrepareAgentDef writes the selected Minder agent contract to
// `<workspace>/AGENTS.md`, which Codex auto-loads from the project root down to
// cwd. Worktrees are ephemeral per job, so overwriting an existing file here is
// safe and keeps the contract scoped to the run.
func (c *CodexRuntime) PrepareAgentDef(_ context.Context, ws runtime.Workspace, def runtime.AgentDefinition) error {
	if ws.Dir == "" {
		return nil
	}
	if def.Name == "" {
		return fmt.Errorf("codex: PrepareAgentDef: empty agent name")
	}
	if len(def.Body) == 0 {
		return fmt.Errorf("codex: PrepareAgentDef: empty body for %q", def.Name)
	}
	path := filepath.Join(ws.Dir, "AGENTS.md")
	if err := os.WriteFile(path, def.Body, 0o644); err != nil {
		return fmt.Errorf("codex: write AGENTS.md: %w", err)
	}
	return nil
}

// Run executes `codex exec --json ...`, streaming JSONL stdout and stderr to
// logFile. Codex has no Claude-style allowed-tools flag; the runtime relies on
// workspace-write sandboxing so it can edit only the provided worktree.
func (c *CodexRuntime) Run(ctx context.Context, inv runtime.Invocation, sink runtime.EventSink, logFile io.Writer) (int, error) {
	args := buildArgs(inv)
	return c.exec(ctx, args, inv.Workspace.Dir, inv.Env, inv.Limits, sink, logFile)
}

// Resume restarts a prior Codex session using the documented non-interactive
// resume form: `codex exec resume <session_id> --json`.
func (c *CodexRuntime) Resume(ctx context.Context, sessionID string, sink runtime.EventSink, logFile io.Writer) (int, error) {
	if sessionID == "" {
		return 0, fmt.Errorf("codex: Resume: empty session id")
	}
	args := []string{"exec", "resume", sessionID, "--json"}
	return c.exec(ctx, args, "", nil, runtime.Limits{}, sink, logFile)
}

func (c *CodexRuntime) exec(ctx context.Context, args []string, workDir string, env map[string]string, limits runtime.Limits, sink runtime.EventSink, logFile io.Writer) (int, error) {
	if c.execFunc != nil {
		return c.execFunc(ctx, args, workDir, env, limits, sink, logFile)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(runCtx, c.binPath(), args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	logWriter := logFile
	if logFile != nil {
		logWriter = &safeWriter{w: logFile}
	}
	cmd.Stderr = logWriter

	if len(env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("codex: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("codex: start: %w", err)
	}

	var scanState codexRunState
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanStream(stdout, logWriter, sink, &scanState, limits, cancel)
	}()

	waitErr := cmd.Wait()
	<-scanDone

	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return 0, fmt.Errorf("codex: wait: %w", waitErr)
		}
	}
	return exitCode, nil
}

// safeWriter serializes stdout/stderr writes into the shared agent log.
type safeWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *safeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// buildArgs constructs the CLI argv for a fresh Run.
//
// Effective flags:
//
//	codex --ask-for-approval never exec --json --cd <worktree>
//	  --sandbox workspace-write -c sandbox_workspace_write.network_access=true
//	  [--model <name>] [--add-dir <git-metadata-dir> ...] [--config key=value] <prompt>
//
// `workspace-write` gives Codex enough access to edit Minder's worktree while
// respecting the execution plane boundary. `--ask-for-approval never` must be
// a top-level Codex flag before `exec`; non-interactive exec cannot service
// approval prompts.
func buildArgs(inv runtime.Invocation) []string {
	args := []string{
		"--ask-for-approval", "never",
		"exec",
		"--json",
	}
	if inv.Workspace.Dir != "" {
		args = append(args, "--cd", inv.Workspace.Dir)
	}
	if model := runtime.NormalizeModelName(inv.Model); model != "" {
		args = append(args, "--model", model)
	}
	args = append(args,
		"--sandbox", "workspace-write",
		"-c", "sandbox_workspace_write.network_access=true",
	)
	for _, dir := range gitMetadataDirs(inv.Workspace.Dir) {
		args = append(args, "--add-dir", dir)
	}
	if inv.SystemPrompt != "" {
		args = append(args, "-c", "developer_instructions="+strconv.Quote(inv.SystemPrompt))
	}
	args = append(args, inv.Prompt)
	return args
}

func gitMetadataDirs(worktree string) []string {
	if worktree == "" {
		return nil
	}

	gitPath := filepath.Join(worktree, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return nil
	}
	if info.IsDir() {
		return []string{gitPath}
	}

	data, err := os.ReadFile(gitPath)
	if err != nil {
		return nil
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return nil
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktree, gitDir)
	}
	gitDir = filepath.Clean(gitDir)

	dirs := []string{gitDir}
	commonDir := resolveCommonGitDir(gitDir)
	if commonDir != "" && commonDir != gitDir {
		dirs = append(dirs, commonDir)
	}
	return dirs
}

func resolveCommonGitDir(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return ""
	}
	commonDir := strings.TrimSpace(string(data))
	if commonDir == "" {
		return ""
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	return filepath.Clean(commonDir)
}

// ParseResult reads a Codex JSONL log and synthesizes the normalized Result.
// Codex does not emit a single final result event, so this aggregates thread,
// assistant-message, turn, usage, and terminal error events.
func (c *CodexRuntime) ParseResult(logPath string) (*runtime.Result, error) {
	state, err := readLogState(logPath)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, nil
	}

	return &runtime.Result{
		SessionID:         state.SessionID,
		Model:             state.Model,
		NumTurns:          state.NumTurns,
		TotalCostUSD:      estimateCostUSD(state.Model, state.InputTok, state.CachedTok, state.OutputTok),
		FinalText:         state.FinalText,
		IsError:           state.IsError,
		StopReason:        state.StopText,
		PermissionDenials: nil,
		Native:            state.Native,
	}, nil
}

// ClassifyOutcome maps a Codex Result + Limits to a normalized Outcome.
func (c *CodexRuntime) ClassifyOutcome(r *runtime.Result, limits runtime.Limits) runtime.Outcome {
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
		if detail == "" {
			detail = r.StopReason
		}
		if len(detail) > 500 {
			detail = detail[:500] + "..."
		}
		reason := "error"
		if looksLikeUsageLimit(detail) {
			reason = "usage_limit"
		}
		return runtime.Outcome{Status: "failed", FailureReason: reason, FailureDetail: detail}
	}

	return runtime.Outcome{}
}

// ExtractBailReport finds a <bail-report> JSON block in the final assistant
// text, then falls back to scanning the raw JSONL log tail.
func (c *CodexRuntime) ExtractBailReport(r *runtime.Result, logPath string) *runtime.BailReport {
	if r != nil && r.FinalText != "" {
		if rep := extractBailReportFromText(r.FinalText); rep != nil {
			return rep
		}
	}
	return extractBailReportFromLog(logPath)
}

func readLogState(logPath string) (*codexRunState, error) {
	if logPath == "" {
		return nil, nil
	}
	f, err := os.Open(logPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	var state codexRunState
	seen := false
	for scanner.Scan() {
		line := scanner.Bytes()
		var evt codexEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}
		if evt.Type == "" {
			continue
		}
		seen = true
		state.apply(evt, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !seen {
		return nil, nil
	}
	return &state, nil
}
