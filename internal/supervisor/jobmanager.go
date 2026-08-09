package supervisor

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/eventbus"
	gitpkg "github.com/aptx-health/agent-minder/internal/git"
	ghpkg "github.com/aptx-health/agent-minder/internal/github"
	"github.com/aptx-health/agent-minder/internal/lesson"
	runtimepkg "github.com/aptx-health/agent-minder/internal/runtime"
	"github.com/jmoiron/sqlx"
)

// JobManager is the interface that all agent types implement.
// Each job gets its own JobManager goroutine.
type JobManager interface {
	// Run executes the full job lifecycle (all stages).
	// It should update the job's status, cost, PR, etc. via the SlotContext.
	Run(ctx context.Context) error
}

// JobResult holds the final outcome of a job.
type JobResult struct {
	Status  string // final status (e.g., "review", "done", "bailed")
	PRNum   int    // PR number if one was opened
	Cost    float64
	Risk    string // review risk assessment
	Summary string // one-line summary
}

// stageResult captures the outcome of running a single stage.
type stageResult struct {
	success    bool              // true if stage completed successfully
	bailed     bool              // true if agent deliberately bailed
	manual     bool              // true if work is ready but needs human follow-up
	failed     bool              // true if runtime/process/classifier failed
	exhausted  bool              // true if turn/budget limit hit
	usageLimit bool              // true if Claude Code usage/rate limit hit
	sessionID  string            // session ID for --resume on recovery
	reason     string            // failure reason for failed stages
	detail     string            // failure detail for failed stages
	prDetected int               // PR number if one was opened during this stage
	assessment *ReviewAssessment // review assessment if this was a review stage
}

// TestHooks allows tests to override SlotContext methods that call external
// systems (git, GitHub API, review extraction, runtime execution). Hooks are
// nil in production. Tests substitute the runtime execution with RunStageFn
// rather than overriding individual claude-CLI plumbing — this keeps the
// supervisor-side translation layer (invocation building, adapter, live status)
// under test alongside the surrounding pipeline logic.
type TestHooks struct {
	RunStageFn                func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (exitCode int, result *runtimepkg.Result, usageLimit bool, err error)
	ResumeFn                  func(ctx context.Context, sessionID string, logFile *os.File) (result *runtimepkg.Result, usageLimit bool, err error)
	DetectPRFn                func(ctx context.Context) int
	SetupWorktreeFn           func() error
	RunSetupHookFn            func(ctx context.Context) (bool, error)
	EnsureAgentDefFn          func(name AgentName) (AgentDefSource, error)
	ExtractReviewAssessmentFn func(ctx context.Context, logPath string, job *db.Job) ReviewAssessment
	CreateReviewCommentFn     func(ctx context.Context, prNumber int, body string) (int64, error)
	GetPRBaseFn               func(ctx context.Context, prNumber int) (string, error)
	EnableAutoMergeFn         func(ctx context.Context, prNumber int, method string) error
}

// SlotContext provides primitives that job managers use to interact
// with the system (worktrees, agent execution, events, GitHub, lessons).
type SlotContext struct {
	Store   *db.Store
	Deploy  *db.Deployment
	Job     *db.Job
	RepoDir string
	Owner   string
	Repo    string
	GHToken string

	// Paths.
	WorktreePath string
	Branch       string
	LogPath      string

	// Resolved config.
	BaseBranch   string
	TestCommand  string
	TestTimeout  string // e.g., "3m" — agents should wrap test commands with timeout
	BuildTimeout string // e.g., "2m" — agents should wrap build commands with timeout
	SetupTimeout string // e.g., "5m" — setup hook execution timeout
	AllowedTools []string

	// Internal reference to supervisor.
	sup *Supervisor

	// Test hooks — nil in production, set by tests to override external calls.
	Hooks *TestHooks

	provisioningLogged bool

	// hasReviewerFeedback tracks whether per-lesson feedback was processed.
	// When true, binary RecordLessonOutcome is skipped (reviewer feedback is more precise).
	hasReviewerFeedback bool
}

// EmitEvent emits an ephemeral supervisor event for this job: live-only
// telemetry and advisory chatter, never persisted (Expedition IV §5). State
// transitions and decisions go through EmitDurable/EmitDurableWith.
func (sc *SlotContext) EmitEvent(typ EventType, summary string) {
	sc.sup.emitEvent(typ, summary, sc.Job.ID)
}

// EmitDurable persists a durable event for this job and publishes it to the
// live bus after commit (Expedition IV R-1/R-2). Use for decisions whose only
// state change is the event itself.
func (sc *SlotContext) EmitDurable(typ EventType, summary string) {
	sc.sup.emitDurableEvent(typ, summary, sc.Job.ID, nil)
}

// EmitDurableWith commits write and the durable event describing it in one
// transaction, then publishes after commit. This is the store-first pairing
// for state transitions: before commit neither the write nor the event is
// visible anywhere; after commit both are.
func (sc *SlotContext) EmitDurableWith(typ EventType, summary string, write func(*sqlx.Tx) error) {
	sc.sup.emitDurableEvent(typ, summary, sc.Job.ID, write)
}

// JobLabel returns a human-readable identifier: "#42" for reactive, "job-name" for proactive.
func (sc *SlotContext) JobLabel() string {
	return jobLabel(sc.Job)
}

func jobLabel(job *db.Job) string {
	if job.IssueNumber > 0 {
		return fmt.Sprintf("#%d", job.IssueNumber)
	}
	return job.Name
}

// NewGHClient creates a GitHub client.
func (sc *SlotContext) NewGHClient() *ghpkg.Client {
	return sc.sup.newGHClient()
}

// getPRBase returns the base branch of a PR, dispatching to a test hook when set.
func (sc *SlotContext) getPRBase(ctx context.Context, ghClient *ghpkg.Client, prNumber int) (string, error) {
	if sc.Hooks != nil && sc.Hooks.GetPRBaseFn != nil {
		return sc.Hooks.GetPRBaseFn(ctx, prNumber)
	}
	return ghClient.GetPRBase(ctx, sc.Owner, sc.Repo, prNumber)
}

// enableAutoMerge enables auto-merge on a PR, dispatching to a test hook when set.
func (sc *SlotContext) enableAutoMerge(ctx context.Context, ghClient *ghpkg.Client, prNumber int, method string) error {
	if sc.Hooks != nil && sc.Hooks.EnableAutoMergeFn != nil {
		return sc.Hooks.EnableAutoMergeFn(ctx, prNumber, method)
	}
	return ghClient.EnableAutoMerge(ctx, sc.Owner, sc.Repo, prNumber, method)
}

// SetupWorktree creates a git worktree for this job.
// Cleans up any stale worktree/branch from a previous run first.
// Safe to call concurrently — uses the supervisor's gitSetupMu.
func (sc *SlotContext) SetupWorktree() error {
	if sc.Hooks != nil && sc.Hooks.SetupWorktreeFn != nil {
		return sc.Hooks.SetupWorktreeFn()
	}

	_ = os.MkdirAll(filepath.Dir(sc.WorktreePath), 0755)

	sc.sup.gitSetupMu.Lock()
	// Remove any worktree using this branch — it may be under a different
	// deploy ID from a previous run.
	_ = gitpkg.WorktreeRemoveByBranch(sc.RepoDir, sc.Branch)
	_ = gitpkg.WorktreePrune(sc.RepoDir)
	_ = gitpkg.DeleteBranch(sc.RepoDir, sc.Branch)
	err := gitpkg.WorktreeAdd(sc.RepoDir, sc.WorktreePath, sc.Branch, "origin/"+sc.BaseBranch)
	sc.sup.gitSetupMu.Unlock()

	if err != nil {
		return fmt.Errorf("worktree setup: %w", err)
	}

	_ = sc.Store.UpdateJobWorktree(sc.Job.ID, sc.WorktreePath, sc.Branch)
	return nil
}

const (
	setupHookRelPath          = ".agent-minder/setup.sh"
	setupHookFailureReason    = "setup_hook"
	setupHookOutputTailBytes  = 32 * 1024
	setupHookDetailTailBytes  = 4 * 1024
	setupHookDefaultTimeout   = 5 * time.Minute
	setupHookLogStartTemplate = "=== agent-minder setup hook started: %s ===\n"
	setupHookLogEndTemplate   = "=== agent-minder setup hook finished: %s ===\n"
)

// RunSetupHook runs a repo-provided setup hook after a fresh worktree is
// created. It returns true when it wrote provisioning output to the job log.
func (sc *SlotContext) RunSetupHook(ctx context.Context) (bool, error) {
	if sc.Hooks != nil && sc.Hooks.RunSetupHookFn != nil {
		wroteLog, err := sc.Hooks.RunSetupHookFn(ctx)
		sc.provisioningLogged = wroteLog
		return wroteLog, err
	}

	hookPath := filepath.Join(sc.WorktreePath, setupHookRelPath)
	info, err := os.Stat(hookPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat setup hook: %w", err)
	}
	if info.IsDir() {
		return false, fmt.Errorf("setup hook is a directory: %s", setupHookRelPath)
	}

	timeout := sc.setupHookTimeout()
	sc.EmitDurable(EventInfo, fmt.Sprintf("Setup hook started for %s: %s", sc.JobLabel(), setupHookRelPath))
	logFile, err := sc.OpenLogFile(false)
	if err != nil {
		return false, fmt.Errorf("open setup hook log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	sc.provisioningLogged = true
	startedAt := time.Now().UTC().Format(time.RFC3339)
	_, _ = fmt.Fprintf(logFile, setupHookLogStartTemplate, startedAt)

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "env", "bash", setupHookRelPath)
	cmd.Dir = sc.WorktreePath
	tail := &limitedBuffer{limit: setupHookOutputTailBytes}
	writer := io.MultiWriter(logFile, tail)
	cmd.Stdout = writer
	cmd.Stderr = writer

	err = cmd.Run()
	finishedAt := time.Now().UTC().Format(time.RFC3339)
	_, _ = fmt.Fprintf(logFile, setupHookLogEndTemplate, finishedAt)
	if err == nil {
		sc.EmitDurable(EventInfo, fmt.Sprintf("Setup hook succeeded for %s", sc.JobLabel()))
		return true, nil
	}

	return true, setupHookError(err, runCtx.Err(), timeout, tail.String())
}

func (sc *SlotContext) setupHookTimeout() time.Duration {
	if sc.SetupTimeout == "" {
		return setupHookDefaultTimeout
	}
	d, err := time.ParseDuration(sc.SetupTimeout)
	if err != nil || d <= 0 {
		return setupHookDefaultTimeout
	}
	return d
}

func setupHookError(err error, ctxErr error, timeout time.Duration, output string) error {
	status := fmt.Sprintf("setup hook failed: %v", err)
	if ctxErr == context.DeadlineExceeded {
		status = fmt.Sprintf("setup hook timed out after %s", timeout)
	} else if exitErr, ok := err.(*exec.ExitError); ok {
		if code := exitErr.ExitCode(); code >= 0 {
			status = fmt.Sprintf("setup hook failed with exit code %d", code)
		} else if exitErr.ProcessState != nil {
			status = fmt.Sprintf("setup hook failed: %s", exitErr.String())
		}
	}
	tail := tailString(output, setupHookDetailTailBytes)
	if strings.TrimSpace(tail) == "" {
		return fmt.Errorf("%s", status)
	}
	return fmt.Errorf("%s\n\noutput tail:\n%s", status, tail)
}

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.limit <= 0 {
		return n, nil
	}
	if n >= b.limit {
		b.buf.Reset()
		_, _ = b.buf.Write(p[n-b.limit:])
		return n, nil
	}
	if b.buf.Len()+n > b.limit {
		data := append([]byte(nil), b.buf.Bytes()...)
		keep := b.limit - n
		if keep < 0 {
			keep = 0
		}
		if len(data) > keep {
			data = data[len(data)-keep:]
		}
		b.buf.Reset()
		_, _ = b.buf.Write(data)
	}
	_, _ = b.buf.Write(p)
	return n, nil
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}

func tailString(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return s[len(s)-maxBytes:]
}

// branchClaimedByActiveJob returns another non-terminal job that already owns
// this job's branch, or nil if the branch is free to claim. It scopes the check
// to the same owner/repo and excludes this job.
func (sc *SlotContext) branchClaimedByActiveJob() (*db.Job, error) {
	if sc.Store == nil || sc.Branch == "" {
		return nil, nil
	}
	return sc.Store.ActiveJobOwningBranch(sc.Owner, sc.Repo, sc.Branch, sc.Job.ID)
}

// EnsureAgentDef ensures the agent definition exists in the worktree.
func (sc *SlotContext) EnsureAgentDef(name AgentName) (AgentDefSource, error) {
	if sc.Hooks != nil && sc.Hooks.EnsureAgentDefFn != nil {
		return sc.Hooks.EnsureAgentDefFn(name)
	}
	source, body, err := resolveAgentDefByName(sc.WorktreePath, name)
	if err != nil {
		return "", err
	}
	if rt, err := sc.sup.RuntimeForJob(sc.Job); err != nil {
		return "", err
	} else if rt != nil {
		if err := rt.PrepareAgentDef(context.Background(), runtimepkg.Workspace{Dir: sc.WorktreePath}, runtimepkg.AgentDefinition{
			Name:   string(name),
			Source: source.Description(),
			Body:   body,
		}); err != nil {
			return "", err
		}
	}
	return source, nil
}

// OpenLogFile opens (or creates) the agent log file.
// If append is true, appends to existing log.
func (sc *SlotContext) OpenLogFile(appendMode bool) (*os.File, error) {
	_ = os.MkdirAll(filepath.Dir(sc.LogPath), 0755)
	if appendMode {
		return os.OpenFile(sc.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}
	return os.Create(sc.LogPath)
}

// DetectPR looks for a PR in the log or via GitHub API.
func (sc *SlotContext) DetectPR(ctx context.Context) int {
	if sc.Hooks != nil && sc.Hooks.DetectPRFn != nil {
		return sc.Hooks.DetectPRFn(ctx)
	}

	// Check log first.
	prNum := detectPRFromLog(sc.LogPath, sc.Owner, sc.Repo)
	if prNum > 0 {
		return prNum
	}

	// Try GitHub API.
	ghClient := sc.NewGHClient()
	prs, err := ghClient.ListPRsForBranch(ctx, sc.Owner, sc.Repo, sc.Branch)
	if err == nil && len(prs) > 0 {
		return prs[0]
	}

	return 0
}

// FetchIssueComments fetches issue comments from GitHub.
func (sc *SlotContext) FetchIssueComments(ctx context.Context) string {
	if sc.sup == nil {
		return ""
	}
	ghClient := sc.NewGHClient()
	content, err := ghClient.FetchItemContent(ctx, sc.Owner, sc.Repo, sc.Job.IssueNumber, "issue")
	if err != nil {
		return ""
	}
	return strings.Join(content.Comments, "\n\n")
}

// SelectAndRecordLessons selects relevant lessons and records injection.
func (sc *SlotContext) SelectAndRecordLessons() string {
	lessons, err := lesson.SelectLessons(sc.Store, sc.Owner, sc.Repo)
	if err != nil || len(lessons) == 0 {
		return ""
	}
	_ = lesson.RecordInjection(sc.Store, sc.Job.ID, lessons)
	debugLog("lessons injected", "issue", sc.Job.IssueNumber, "count", len(lessons))
	return lesson.FormatForPrompt(lessons)
}

// RecordLessonOutcome records whether the job outcome was helpful.
// This is the binary fallback — skipped if per-lesson reviewer feedback was already processed.
func (sc *SlotContext) RecordLessonOutcome(success bool) {
	if sc.hasReviewerFeedback {
		debugLog("skipping binary lesson outcome — reviewer feedback already processed",
			"job", sc.Job.ID)
		return
	}
	_ = lesson.RecordOutcome(sc.Store, sc.Job.ID, success)
}

// processLessonFeedback applies per-lesson feedback from the reviewer.
func processLessonFeedback(sc *SlotContext, feedback []LessonFeedback) {
	applied := 0
	for _, fb := range feedback {
		if fb.ID <= 0 {
			continue
		}
		if err := sc.Store.UpdateLessonFeedback(fb.ID, fb.Helpful); err != nil {
			debugLog("lesson feedback update failed", "lesson_id", fb.ID, "error", err.Error())
			continue
		}
		applied++
		debugLog("lesson feedback recorded",
			"lesson_id", fb.ID,
			"helpful", fb.Helpful,
			"reason", fb.Reason,
		)
	}
	if applied > 0 {
		sc.hasReviewerFeedback = true
		sc.EmitEvent(EventInfo, fmt.Sprintf("Recorded reviewer feedback for %d lessons", applied))
	}
}

// WasStoppedByUser checks if this job was stopped by user action.
func (sc *SlotContext) WasStoppedByUser() bool {
	sc.sup.mu.Lock()
	defer sc.sup.mu.Unlock()
	if rs, ok := sc.sup.running[sc.Job.ID]; ok {
		return rs.stoppedByUser
	}
	return false
}

// HitUsageLimit checks if the agent was stopped by a usage/rate limit.
func (sc *SlotContext) HitUsageLimit() bool {
	sc.sup.mu.Lock()
	defer sc.sup.mu.Unlock()
	if rs, ok := sc.sup.running[sc.Job.ID]; ok {
		return rs.hitUsageLimit
	}
	return false
}

// ClearUsageLimitFlag resets the usage limit flag for retry.
func (sc *SlotContext) ClearUsageLimitFlag() {
	sc.sup.mu.Lock()
	defer sc.sup.mu.Unlock()
	if rs, ok := sc.sup.running[sc.Job.ID]; ok {
		rs.hitUsageLimit = false
	}
}

// TriggerLabels returns the labels that triggered this job, if any.
// Looks up the job's agent in the supervisor's trigger routes.
func (sc *SlotContext) TriggerLabels() []string {
	sc.sup.mu.Lock()
	defer sc.sup.mu.Unlock()
	for _, route := range sc.sup.triggerRoutes {
		if route.Agent == sc.Job.Agent {
			return route.Labels
		}
	}
	return nil
}

// ParseCost extracts cost from the agent log.
func (sc *SlotContext) ParseCost() float64 {
	return parseCostFromLog(sc.LogPath)
}

// newSlotContext creates a SlotContext for a running job.
func (s *Supervisor) newSlotContext(jobID int64, job *db.Job) *SlotContext {
	home, _ := os.UserHomeDir()
	worktreeBase := filepath.Join(home, ".agent-minder", "worktrees", s.deploy.ID)
	logDir := filepath.Join(home, ".agent-minder", "agents")

	// Branch and worktree naming depends on whether we have an issue.
	var worktreePath, branch, logPath string
	if job.IssueNumber > 0 {
		worktreePath = filepath.Join(worktreeBase, fmt.Sprintf("issue-%d", job.IssueNumber))
		branch = fmt.Sprintf("agent/issue-%d", job.IssueNumber)
		logPath = filepath.Join(logDir, fmt.Sprintf("%s-issue-%d.log", s.deploy.ID, job.IssueNumber))
	} else {
		// Proactive job — use job name for paths.
		worktreePath = filepath.Join(worktreeBase, job.Name)
		branch = fmt.Sprintf("agent/%s", job.Name)
		logPath = filepath.Join(logDir, fmt.Sprintf("%s-%s.log", s.deploy.ID, job.Name))
	}

	return &SlotContext{
		Store:        s.store,
		Deploy:       s.deploy,
		Job:          job,
		RepoDir:      s.repoDir,
		Owner:        s.owner,
		Repo:         s.repo,
		GHToken:      s.ghToken,
		WorktreePath: worktreePath,
		Branch:       branch,
		LogPath:      logPath,
		BaseBranch:   resolveBaseBranch(s.repoDir, s.deploy),
		TestCommand:  resolveTestCommand(s.repoDir),
		TestTimeout:  resolveTimeout(s.repoDir, "test"),
		BuildTimeout: resolveTimeout(s.repoDir, "build"),
		SetupTimeout: resolveTimeout(s.repoDir, "setup"),
		AllowedTools: resolveAllowedTools(s.repoDir),
		sup:          s,
	}
}

// --- DefaultJobManager ---

// DefaultJobManager executes jobs by iterating through declared pipeline stages.
type DefaultJobManager struct {
	sc       *SlotContext
	contract *AgentContract
	attempts map[string]int // stage name → executions so far (for run attempt numbering)
}

// NewDefaultJobManager creates a job manager that executes the contract's stage pipeline.
func NewDefaultJobManager(sc *SlotContext, contract *AgentContract) *DefaultJobManager {
	return &DefaultJobManager{sc: sc, contract: contract, attempts: map[string]int{}}
}

// beginAgentRun inserts a durable agent_runs row for a starting stage execution
// and registers it so live-status updates persist step progress. Returns the
// run ID, or 0 if the row could not be written (run tracking is best-effort and
// never fails the stage).
func (m *DefaultJobManager) beginAgentRun(stageName, agentName string, inv runtimepkg.Invocation) int64 {
	sc := m.sc
	if sc.Store == nil {
		return 0
	}
	m.attempts[stageName]++
	run := &db.AgentRun{
		JobID:        sc.Job.ID,
		Stage:        stageName,
		Attempt:      m.attempts[stageName],
		Agent:        agentName,
		Runtime:      sql.NullString{String: sc.Job.EffectiveRuntime(sc.Deploy), Valid: true},
		Model:        toNullStr(inv.Model),
		MaxTurns:     sql.NullInt64{Int64: int64(inv.Limits.MaxTurns), Valid: inv.Limits.MaxTurns > 0},
		MaxBudgetUSD: sql.NullFloat64{Float64: inv.Limits.MaxBudgetUSD, Valid: inv.Limits.MaxBudgetUSD > 0},
		LogPath:      toNullStr(sc.LogPath),
	}
	if err := sc.Store.StartAgentRun(run); err != nil {
		debugLog("agent run start failed", "job", sc.Job.ID, "stage", stageName, "error", err.Error())
		return 0
	}
	if sc.sup != nil {
		sc.sup.beginAgentRunTracking(sc.Job.ID, run.ID)
	}
	return run.ID
}

// finishAgentRun writes the terminal state of a run row from the stage outcome
// and the runtime result, and stops live-status tracking for it.
func (m *DefaultJobManager) finishAgentRun(runID int64, result *runtimepkg.Result, res stageResult) {
	if runID == 0 {
		return
	}
	sc := m.sc
	steps := 0
	if sc.sup != nil {
		steps = sc.sup.endAgentRunTracking(sc.Job.ID)
	}
	if sc.Store == nil {
		return
	}
	fin := db.AgentRunResult{
		Status:        runStatusFor(res),
		StopReason:    res.reason,
		FailureDetail: res.detail,
		StepCount:     steps,
	}
	if result != nil {
		fin.SessionID = result.SessionID
		fin.FinalText = result.FinalText
		fin.FinalTurns = result.NumTurns
		fin.CostUSD = result.TotalCostUSD
		if fin.StopReason == "" {
			fin.StopReason = result.StopReason
		}
	}
	if err := sc.Store.CompleteAgentRun(runID, fin); err != nil {
		debugLog("agent run complete failed", "job", sc.Job.ID, "run", runID, "error", err.Error())
	}
}

// runStatusFor maps a stageResult onto a durable agent-run status.
func runStatusFor(res stageResult) string {
	switch {
	case res.usageLimit:
		return db.RunStatusUsageLimit
	case res.manual:
		return db.RunStatusManual
	case res.bailed:
		return db.RunStatusBailed
	case res.failed:
		return db.RunStatusFailed
	case res.success:
		return db.RunStatusSuccess
	default:
		return db.RunStatusFailed
	}
}

// toNullStr maps an empty string to a NULL sql.NullString.
func toNullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// Run executes the job's stage pipeline.
// Stages are iterated in order. Each stage runs its agent, and the outcome
// determines whether to continue, retry, skip, or bail the pipeline.
func (m *DefaultJobManager) Run(ctx context.Context) error {
	sc := m.sc
	job := sc.Job
	contract := m.contract

	// The started event is emitted by markJobLaunched, atomically with the
	// queued→running transition (Expedition IV R-1).

	// --- One-time setup ---

	if contract.NeedsWorktree() {
		// Guard against branch collision: two jobs on the same issue with
		// different agents derive the same branch (agent/issue-<N>). Without
		// this check the second job's SetupWorktree would remove and recreate
		// the first job's live worktree/branch, silently destroying its work.
		// Block the second job instead. The atomic claim transaction is the
		// full fix (integration-target issue 3); this is the interim guard.
		if owner, err := sc.branchClaimedByActiveJob(); err != nil {
			debugLog("branch claim check failed", "job", job.ID, "branch", sc.Branch, "error", err.Error())
		} else if owner != nil {
			detail := fmt.Sprintf("branch %s is already owned by active job #%d (status %s); refusing to reset its worktree",
				sc.Branch, owner.ID, owner.Status)
			sc.EmitDurableWith(EventError, fmt.Sprintf("Branch in use for %s: %s", sc.JobLabel(), detail),
				func(tx *sqlx.Tx) error { return db.UpdateJobFailureTx(tx, job.ID, "branch_in_use", detail) })
			return fmt.Errorf("branch in use: %s", detail)
		}
		if err := sc.SetupWorktree(); err != nil {
			sc.EmitDurableWith(EventError, fmt.Sprintf("Worktree setup failed for %s: %v", sc.JobLabel(), err),
				func(tx *sqlx.Tx) error { return db.UpdateJobFailureTx(tx, job.ID, "worktree", err.Error()) })
			return err
		}
		if _, err := sc.RunSetupHook(ctx); err != nil {
			sc.EmitDurableWith(EventError, fmt.Sprintf("Setup hook failed for %s: %v", sc.JobLabel(), err),
				func(tx *sqlx.Tx) error {
					return db.UpdateJobBlockedFailureTx(tx, job.ID, setupHookFailureReason, err.Error())
				})
			return err
		}
	}

	// Ensure all agent defs referenced by stages exist.
	for _, stage := range contract.Stages {
		agentName := stage.Agent
		if agentName == "" {
			agentName = job.Agent
		}
		if _, err := sc.EnsureAgentDef(AgentName(agentName)); err != nil {
			sc.EmitDurableWith(EventError, fmt.Sprintf("Agent def error for %s/%s: %v", sc.JobLabel(), stage.Name, err),
				func(tx *sqlx.Tx) error { return db.UpdateJobFailureTx(tx, job.ID, "agent_def", err.Error()) })
			return err
		}
	}

	// Label in-progress for reactive jobs.
	ghClient := sc.NewGHClient()
	if job.IssueNumber > 0 {
		_ = ghClient.AddLabel(ctx, sc.Owner, sc.Repo, job.IssueNumber, "in-progress")
	}

	logFile, err := sc.OpenLogFile(sc.provisioningLogged)
	if err != nil {
		sc.EmitDurableWith(EventError, fmt.Sprintf("Log file error for %s: %v", sc.JobLabel(), err),
			func(tx *sqlx.Tx) error { return db.UpdateJobFailureTx(tx, job.ID, "log", err.Error()) })
		return err
	}
	defer func() { _ = logFile.Close() }()

	// Build the stage pipeline. If no stages declared, create default code stage.
	// If review is enabled and the pipeline produces PRs but has no review stage,
	// append one automatically. This ensures all PR-producing agents get reviewed
	// regardless of whether they declare explicit stages.
	stages := contract.Stages
	if len(stages) == 0 {
		stages = []StageContract{{Name: "run", Agent: job.Agent, OnFailure: "bail"}}
	}
	if sc.Deploy.ReviewEnabled && contract.Output == "pr" && !hasReviewStage(stages) {
		stages = append(stages, StageContract{
			Name: "review", Agent: "reviewer", OnFailure: "skip", Retries: 1,
		})
	}

	// --- Stage loop ---

	var lastReviewAssessment *ReviewAssessment
	var lastReviewRisk string
	retryCount := map[string]int{} // stage name → retries so far

	for i := 0; i < len(stages); i++ {
		stage := stages[i]
		stageName := stage.Name
		agentName := stage.Agent
		if agentName == "" {
			agentName = job.Agent
		}

		sc.EmitDurableWith(EventInfo, fmt.Sprintf("Stage %q started on %s (agent: %s)", stageName, sc.JobLabel(), agentName),
			func(tx *sqlx.Tx) error { return db.UpdateJobStageTx(tx, job.ID, stageName, "") })

		var result stageResult

		// Route to review-specific execution only when the agent is "reviewer".
		// Stage names are user-defined (e.g., "review", "verify", "audit") and
		// should not affect routing — only the agent type matters.
		switch agentName {
		case "reviewer":
			result = m.executeReviewStage(ctx, stage, logFile)
			if result.assessment != nil {
				lastReviewAssessment = result.assessment
				lastReviewRisk = result.assessment.Risk
			}
		default:
			// Code stage or any generic stage.
			var feedbackPrompt string
			if lastReviewAssessment != nil && len(lastReviewAssessment.Issues) > 0 {
				feedbackPrompt = formatReviewFeedback(lastReviewAssessment)
				lastReviewAssessment = nil // consumed
			}
			result = m.executeCodeStage(ctx, stage, agentName, logFile, feedbackPrompt)
		}

		// Capture lessons from non-reviewer stages that declare captures_lessons.
		// The reviewer path handles this internally via executeReviewStage.
		if stage.CapturesLessons && agentName != "reviewer" && result.success {
			assessment := extractReviewAssessmentFromLog(ctx, sc.LogPath, job, sc.sup, sc.Hooks)
			if len(assessment.Lessons) > 0 {
				captured := captureLessonsFromAssessment(sc.Store, sc.Owner, sc.Repo, assessment)
				if len(captured) > 0 {
					sc.EmitEvent(EventInfo, fmt.Sprintf("Captured %d lessons from %s stage on %s",
						len(captured), stageName, sc.JobLabel()))
				}
			}
		}

		// Check for user stop.
		if sc.WasStoppedByUser() {
			sc.EmitDurableWith(EventStopped, fmt.Sprintf("Agent stopped by user on %s", sc.JobLabel()),
				func(tx *sqlx.Tx) error { return db.UpdateJobStatusTx(tx, job.ID, db.StatusStopped) })
			return nil
		}

		// Extract cost after each stage.
		if cost := sc.ParseCost(); cost > 0 {
			_ = sc.Store.UpdateJobCost(job.ID, cost)
		}

		// Usage limit recovery: wait and resume the session.
		if result.usageLimit {
			usageLimitAttempts := 0
			for result.usageLimit && usageLimitAttempts < maxUsageLimitRetries {
				usageLimitAttempts++
				if err := waitForUsageLimitReset(ctx, sc, usageLimitAttempts, result.sessionID); err != nil {
					// Context cancelled (daemon shutting down).
					return nil
				}

				// Resume the same session with the runtime; on ErrNotSupported
				// (or when no session ID is available) fall back to a fresh
				// stage run.
				if result.sessionID == "" {
					result = m.executeCodeStage(ctx, stage, agentName, logFile, "")
					continue
				}
				resumeResult, resumeUsageLimit, err := sc.resumeThroughRuntime(ctx, result.sessionID, logFile)
				if err == runtimepkg.ErrNotSupported {
					result = m.executeCodeStage(ctx, stage, agentName, logFile, "")
					continue
				}

				// Re-check: did the resumed session also hit a limit?
				adapted := adaptRuntimeResult(resumeResult)
				if resumeUsageLimit || sc.HitUsageLimit() || isUsageLimitError(adapted) {
					sessionID := ""
					if resumeResult != nil {
						sessionID = resumeResult.SessionID
					}
					result = stageResult{usageLimit: true, sessionID: sessionID}
					continue
				}

				// Resumed successfully — re-evaluate the stage outcome.
				if m.contract.Output == "pr" {
					prNum := sc.DetectPR(ctx)
					if prNum > 0 {
						_ = sc.Store.UpdateJobPR(job.ID, prNum)
						job.PRNumber = sql.NullInt64{Int64: int64(prNum), Valid: true}
						result = stageResult{success: true, prDetected: prNum}
					} else {
						result = stageResult{success: false}
					}
				} else {
					result = stageResult{success: true}
				}
			}

			// Exhausted retries — still hitting limit.
			if result.usageLimit {
				sc.EmitEvent(EventError, fmt.Sprintf("Usage limit: exhausted %d retries on %s, failing",
					maxUsageLimitRetries, sc.JobLabel()))
				return m.finalizeFailure(ctx, "usage_limit", fmt.Sprintf("exhausted %d usage-limit retries", maxUsageLimitRetries))
			}
		}

		if result.success {
			continue // next stage
		}

		if result.manual {
			return m.finalizeManual(ctx, result.reason, result.detail)
		}

		if result.failed {
			return m.finalizeFailure(ctx, result.reason, result.detail)
		}

		// Stage failed — handle on_failure.
		onFailure := stage.OnFailure
		if onFailure == "" {
			onFailure = "bail"
		}

		switch onFailure {
		case "skip":
			// A stage-finish routing decision: durable, no paired table write.
			sc.EmitDurable(EventInfo, formatStageSkippedSummary(stageName, sc.JobLabel(), result))
			continue

		case "retry":
			// Retry: re-run the review's *previous* stage (code) with feedback.
			// Only if the failure is from a review finding issues (not bail/exhaust).
			if result.bailed || result.exhausted {
				sc.EmitEvent(EventInfo, fmt.Sprintf("Stage %q failed on %s (bail/exhaust), not retrying", stageName, sc.JobLabel()))
				return m.finalizeBail(ctx)
			}
			maxRetries := stage.Retries
			if maxRetries <= 0 {
				maxRetries = 1
			}
			if retryCount[stageName] >= maxRetries {
				sc.EmitEvent(EventInfo, fmt.Sprintf("Stage %q max retries (%d) reached on %s", stageName, maxRetries, sc.JobLabel()))
				continue // move to next stage
			}
			retryCount[stageName]++

			// Jump back to the previous stage (code) to apply review feedback.
			if i > 0 && result.assessment != nil && len(result.assessment.Issues) > 0 {
				sc.EmitEvent(EventInfo, fmt.Sprintf("Review found issues on %s, retrying code stage (attempt %d/%d)",
					sc.JobLabel(), retryCount[stageName], maxRetries))
				lastReviewAssessment = result.assessment
				i -= 2 // will be incremented to i-1 by the loop
				continue
			}
			// No actionable feedback — just continue.
			continue

		default: // "bail"
			return m.finalizeBail(ctx)
		}
	}

	// --- Pipeline complete ---
	return m.finalizePipeline(ctx, lastReviewRisk)
}

// executeCodeStage runs a code/generic agent through the configured runtime
// and detects the PR outcome. The runtime owns process execution, stream
// parsing, classification, and bail extraction; the supervisor only adapts
// results to its legacy structs and routes the stageResult.
func (m *DefaultJobManager) executeCodeStage(ctx context.Context, stage StageContract, agentName string, logFile *os.File, feedbackPrompt string) (res stageResult) {
	sc := m.sc
	job := sc.Job

	// Build prompt.
	prompt := AssembleContext(ctx, sc, m.contract.Context)
	if feedbackPrompt != "" {
		prompt += "\n\n" + feedbackPrompt
	}
	lessonsPrompt := sc.SelectAndRecordLessons()

	debugLog("stage execute", "stage", stage.Name, "agent", agentName, "label", sc.JobLabel())

	rt, rtErr := sc.sup.RuntimeForJob(job)
	if rtErr != nil {
		return stageResult{success: false, failed: true, reason: "runtime_config", detail: rtErr.Error()}
	}
	runtimeAvailable := rt != nil || (sc.Hooks != nil && sc.Hooks.RunStageFn != nil)
	if !runtimeAvailable {
		sc.EmitEvent(EventError, fmt.Sprintf("No runtime configured for %s — bailing", sc.JobLabel()))
		return stageResult{success: false, bailed: true}
	}

	inv := runtimeInvocationFor(sc, agentName, prompt, lessonsPrompt)

	// Record a durable run row for this stage attempt. finishAgentRun reads the
	// final stageResult (named return) and the runtime result on the way out.
	runID := m.beginAgentRun(stage.Name, agentName, inv)
	var runResult *runtimepkg.Result
	defer func() { m.finishAgentRun(runID, runResult, res) }()

	exitCode, runResultRet, sink, runErr := sc.runStageThroughRuntime(ctx, inv, logFile)
	runResult = runResultRet
	result := adaptRuntimeResult(runResult)
	usageLimit := sink.usageLimit || sc.HitUsageLimit() || isUsageLimitError(result)
	sessionID := ""
	if runResult != nil {
		sessionID = runResult.SessionID
	}

	if runErr != nil {
		return stageResult{
			success: false,
			failed:  true,
			reason:  "runtime_error",
			detail:  runErr.Error(),
		}
	}

	if usageLimit {
		return stageResult{usageLimit: true, sessionID: sessionID}
	}

	// For PR-producing agents, detect the PR.
	if m.contract.Output == "pr" {
		prNum := sc.DetectPR(ctx)
		if prNum > 0 {
			_ = sc.Store.UpdateJobPR(job.ID, prNum)
			job.PRNumber = sql.NullInt64{Int64: int64(prNum), Valid: true}
			return stageResult{success: true, prDetected: prNum}
		}
	} else if exitCode == 0 {
		return stageResult{success: true}
	}

	// No PR / non-zero exit — classify via the runtime when available.
	maxTurns := job.EffectiveMaxTurns(sc.Deploy)
	maxBudget := job.EffectiveMaxBudget(sc.Deploy)
	var outcome runtimepkg.Outcome
	var bailReport *runtimepkg.BailReport
	if rt != nil {
		outcome = rt.ClassifyOutcome(runResult, runtimepkg.Limits{MaxTurns: maxTurns, MaxBudgetUSD: maxBudget})
		bailReport = rt.ExtractBailReport(runResult, sc.LogPath)
	}
	if bailReport != nil {
		m.handleBailReport(ctx, bailReport)
		return stageResult{
			success: false,
			bailed:  true,
		}
	}

	if outcome.Status == "failed" {
		return stageResult{
			success:   false,
			failed:    true,
			exhausted: outcome.FailureReason == "max_turns" || outcome.FailureReason == "max_budget",
			reason:    outcome.FailureReason,
			detail:    outcome.FailureDetail,
		}
	}

	if exitCode != 0 {
		detail := fmt.Sprintf("agent process exited with code %d before producing a successful result", exitCode)
		if inv.Model != "" && rt != nil {
			detail = modelProcessExitDetail(rt.Name(), inv.Model, exitCode, sc.LogPath)
		}
		return stageResult{
			success: false,
			failed:  true,
			reason:  "process_exit",
			detail:  detail,
		}
	}

	detail := "agent exited successfully but did not open a PR"
	if runResult != nil && strings.TrimSpace(runResult.FinalText) != "" {
		detail = truncateFailureDetail(runResult.FinalText, 2000)
	}
	if branchPushed(sc.WorktreePath) {
		return stageResult{
			success: false,
			manual:  true,
			reason:  "pr_required",
			detail:  detail,
		}
	}
	return stageResult{
		success: false,
		failed:  true,
		reason:  "no_pr",
		detail:  detail,
	}
}

func branchPushed(worktreePath string) bool {
	current, err := gitpkg.CurrentBranch(worktreePath)
	if err != nil || current == "" || current == "HEAD" {
		return false
	}
	upstream, err := gitpkg.UpstreamBranch(worktreePath)
	if err != nil {
		return false
	}
	return upstream == "origin/"+current || strings.HasSuffix(upstream, "/"+current)
}

func truncateFailureDetail(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// formatJobStartedSummary renders the started-event line. rt is the resolved
// runtime (nil when none is configured; the configured name is used then) —
// resolved by the caller because the launch path holds the supervisor mutex.
func formatJobStartedSummary(rt runtimepkg.AgentRuntime, deploy *db.Deployment, job *db.Job) string {
	title := job.IssueTitle.String
	if title == "" {
		title = job.Name
	}
	runtimeName := job.EffectiveRuntime(deploy)
	if rt != nil {
		runtimeName = rt.Name()
	}
	parts := []string{fmt.Sprintf("runtime: %s", runtimeName)}
	if model := job.EffectiveModel(); model != "" {
		parts = append(parts, fmt.Sprintf("model: %s", model))
	}
	return fmt.Sprintf("Agent started on %s (%s): %s", jobLabel(job), strings.Join(parts, ", "), title)
}

func formatStageSkippedSummary(stageName, jobLabel string, result stageResult) string {
	if result.assessment != nil {
		risk := result.assessment.Risk
		if risk == "" {
			risk = "unknown"
		}
		return fmt.Sprintf("Stage %q review gate reported %s on %s, skipping", stageName, risk, jobLabel)
	}
	return fmt.Sprintf("Stage %q failed on %s, skipping", stageName, jobLabel)
}

func modelProcessExitDetail(runtimeName, model string, exitCode int, logPath string) string {
	model = runtimepkg.NormalizeModelName(model)
	detail := fmt.Sprintf(
		"agent process exited with code %d while using model %q on runtime %q. Check that the job's model value is valid for that runtime, or remove model: to use the runtime default.",
		exitCode, model, runtimeName,
	)
	if tail := readLogTail(logPath, 1200); tail != "" {
		detail += " Log tail: " + tail
	}
	return detail
}

func readLogTail(path string, maxLen int) string {
	if path == "" || maxLen <= 0 {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	return strings.TrimSpace(s[len(s)-maxLen:])
}

// usageLimitWaitDuration is how long to sleep before retrying after a usage
// limit. Declared as a var (not const) so tests that exercise the resume loop
// can override it; production never mutates it.
var usageLimitWaitDuration = 1 * time.Hour

// maxUsageLimitRetries is the maximum number of usage limit recovery attempts per stage.
const maxUsageLimitRetries = 3

// isUsageLimitError checks the agent result for usage/rate limit signals.
// Looks for common patterns in the error text from Claude Code CLI.
func isUsageLimitError(result *AgentResult) bool {
	if result == nil {
		return false
	}

	// Only check error results — successful completions that mention "rate limit"
	// in their content (e.g., an agent fixing rate limiting code) are not errors.
	if !result.IsError {
		return false
	}

	text := strings.ToLower(result.Result)
	// Match patterns from Claude Code error messages.
	for _, pattern := range []string{
		"hit your limit",
		"session limit",
		"usage limit",
		"rate limit reached",
		"rate_limit",
		"billing_error",
	} {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

// waitForUsageLimitReset sleeps until the usage limit window resets.
// Returns an error if the context is cancelled during the wait. Both the
// waiting and resuming transitions commit their status write and durable
// event in one transaction (Expedition IV R-1/R-2 — this was the flagship
// emit-before-write site the contract calls out).
func waitForUsageLimitReset(ctx context.Context, sc *SlotContext, attempt int, sessionID string) error {
	wait := usageLimitWaitDuration
	// Back off slightly on repeated hits.
	if attempt > 1 {
		wait = time.Duration(attempt) * usageLimitWaitDuration
	}

	sc.EmitDurableWith(EventWaiting, fmt.Sprintf(
		"Usage limit hit on %s — waiting %s before retry (attempt %d/%d)",
		sc.JobLabel(), wait.Truncate(time.Minute), attempt, maxUsageLimitRetries),
		func(tx *sqlx.Tx) error { return db.UpdateJobStatusTx(tx, sc.Job.ID, db.StatusWaiting) })

	select {
	case <-time.After(wait):
		// Reset the flag for the next attempt.
		sc.ClearUsageLimitFlag()
		sc.EmitDurableWith(EventInfo, fmt.Sprintf("Resuming %s after usage limit wait (session: %s)",
			sc.JobLabel(), sessionID),
			func(tx *sqlx.Tx) error { return db.UpdateJobStatusTx(tx, sc.Job.ID, db.StatusRunning) })
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// executeReviewStage runs the review agent and extracts the assessment.
func (m *DefaultJobManager) executeReviewStage(ctx context.Context, stage StageContract, logFile *os.File) (res stageResult) {
	sc := m.sc
	job := sc.Job

	if !job.PRNumber.Valid {
		// No PR to review — skip.
		return stageResult{success: true}
	}

	// Ensure reviewer agent def.
	agentName := stage.Agent
	if agentName == "" {
		agentName = "reviewer"
	}
	if _, err := sc.EnsureAgentDef(AgentName(agentName)); err != nil {
		sc.EmitEvent(EventError, fmt.Sprintf("Reviewer agent def error: %v", err))
		return stageResult{success: false}
	}

	// The other flagship emit-before-write site from Expedition IV §2: the
	// status write and the review-started event now commit atomically.
	sc.EmitDurableWith(EventInfo, fmt.Sprintf("Review started on %s (PR #%d)", sc.JobLabel(), job.PRNumber.Int64),
		func(tx *sqlx.Tx) error { return db.UpdateJobStatusTx(tx, job.ID, db.StatusReviewing) })

	_, _ = fmt.Fprintf(logFile, "\n\n--- REVIEW AGENT (%s) ---\n\n", agentName)

	prompt := renderReviewContext(ctx, sc)
	if rt, err := sc.sup.RuntimeForJob(job); err != nil {
		sc.EmitEvent(EventError, fmt.Sprintf("Runtime config error for review of %s: %v", sc.JobLabel(), err))
		return stageResult{success: false}
	} else if rt == nil && (sc.Hooks == nil || sc.Hooks.RunStageFn == nil) {
		sc.EmitEvent(EventError, fmt.Sprintf("No runtime configured for review of %s", sc.JobLabel()))
		return stageResult{success: false}
	}
	inv := runtimeInvocationFor(sc, agentName, prompt, "")

	runID := m.beginAgentRun(stage.Name, agentName, inv)
	var runResult *runtimepkg.Result
	defer func() { m.finishAgentRun(runID, runResult, res) }()

	_, runResult, _, _ = sc.runStageThroughRuntime(ctx, inv, logFile)

	// Extract structured assessment.
	assessment := extractReviewAssessmentFromLog(ctx, sc.LogPath, job, sc.sup, sc.Hooks)

	risk := assessment.Risk
	if risk == "" {
		risk = "needs-testing"
	}

	reviewCommentID := int64(0)
	commentBody := formatReviewComment(assessment)
	var commentID int64
	var err error
	if sc.Hooks != nil && sc.Hooks.CreateReviewCommentFn != nil {
		commentID, err = sc.Hooks.CreateReviewCommentFn(ctx, int(job.PRNumber.Int64), commentBody)
	} else {
		ghClient := sc.NewGHClient()
		commentID, err = ghClient.CreateComment(ctx, sc.Owner, sc.Repo, int(job.PRNumber.Int64), commentBody)
	}
	if err != nil {
		sc.EmitEvent(EventWarning, fmt.Sprintf("Review comment failed for %s PR #%d: %v", sc.JobLabel(), job.PRNumber.Int64, err))
	} else {
		reviewCommentID = commentID
	}

	sc.EmitDurableWith(EventInfo, fmt.Sprintf("Review of %s complete (risk: %s)", sc.JobLabel(), risk),
		func(tx *sqlx.Tx) error { return db.UpdateJobReviewTx(tx, job.ID, risk, reviewCommentID) })
	if assessment.Summary != "" {
		sc.EmitEvent(EventInfo, fmt.Sprintf("Review: %s", assessment.Summary))
	}

	// Auto-capture lessons.
	if len(assessment.Lessons) > 0 {
		captured := captureLessonsFromAssessment(sc.Store, sc.Owner, sc.Repo, assessment)
		if len(captured) > 0 {
			sc.EmitEvent(EventInfo, fmt.Sprintf("Captured %d lessons from review of %s", len(captured), sc.JobLabel()))
		}
	}

	// Process per-lesson feedback from the reviewer.
	if len(assessment.LessonFeedback) > 0 {
		processLessonFeedback(sc, assessment.LessonFeedback)
	}

	// Determine success: low-risk or needs-testing = success, suspect = failure (retryable).
	success := risk == "low-risk" || risk == "needs-testing"
	return stageResult{
		success:    success,
		assessment: &assessment,
	}
}

// finalizePipeline handles the successful end of the stage pipeline.
func (m *DefaultJobManager) finalizePipeline(ctx context.Context, reviewRisk string) error {
	sc := m.sc
	job := sc.Job
	ghClient := sc.NewGHClient()

	if job.IssueNumber > 0 {
		ghClient.RemoveLabel(ctx, sc.Owner, sc.Repo, job.IssueNumber, "in-progress")

		// Remove the trigger labels that started this job (e.g., "bug", "agent-ready,ux").
		for _, triggerLabel := range sc.TriggerLabels() {
			ghClient.RemoveLabel(ctx, sc.Owner, sc.Repo, job.IssueNumber, triggerLabel)
		}
	}

	if job.PRNumber.Valid {
		if job.IssueNumber > 0 {
			_ = ghClient.AddLabel(ctx, sc.Owner, sc.Repo, job.IssueNumber, "needs-review")
		}
		sc.EmitDurableWith(EventCompleted, fmt.Sprintf("Pipeline complete for %s — PR #%d", sc.JobLabel(), job.PRNumber.Int64),
			func(tx *sqlx.Tx) error { return db.UpdateJobStatusTx(tx, job.ID, db.StatusReviewed) })

		// Auto-merge if configured and low-risk.
		if sc.Deploy.AutoMerge && reviewRisk == "low-risk" {
			prNum := int(job.PRNumber.Int64)
			// Verify the PR targets the deployment default branch before enabling
			// auto-merge. A non-default base (e.g. a stacked child PR under an
			// integration-target plan) must not be silently squashed into an
			// in-flight parent branch on a low-risk verdict. See #611.
			prBase, baseErr := sc.getPRBase(ctx, ghClient, prNum)
			switch {
			case baseErr != nil:
				sc.EmitEvent(EventWarning, fmt.Sprintf("Auto-merge skipped for PR #%d: could not verify base branch: %v", prNum, baseErr))
			case prBase != sc.BaseBranch:
				sc.EmitEvent(EventInfo, fmt.Sprintf("Auto-merge skipped for PR #%d: base branch %q is not the deployment default %q", prNum, prBase, sc.BaseBranch))
			default:
				if err := sc.enableAutoMerge(ctx, ghClient, prNum, "merge"); err == nil {
					// A pipeline decision worth keeping in history; the effect
					// itself is GitHub-side (R-10), so no paired table write.
					sc.EmitDurable(EventInfo, fmt.Sprintf("Auto-merge enabled for PR #%d (will merge when CI passes)", prNum))
				} else {
					sc.EmitEvent(EventWarning, fmt.Sprintf("Auto-merge failed for PR #%d: %v", prNum, err))
				}
			}
		}
	} else {
		sc.EmitDurableWith(EventCompleted, fmt.Sprintf("Agent completed %s", sc.JobLabel()),
			func(tx *sqlx.Tx) error { return db.CompleteJobTx(tx, job.ID, db.StatusDone) })
	}

	sc.RecordLessonOutcome(true)
	return nil
}

// finalizeBail handles a bailed pipeline.
func (m *DefaultJobManager) finalizeBail(ctx context.Context) error {
	sc := m.sc
	job := sc.Job
	ghClient := sc.NewGHClient()

	if job.IssueNumber > 0 {
		ghClient.RemoveLabel(ctx, sc.Owner, sc.Repo, job.IssueNumber, "in-progress")
	}

	rt, _ := sc.sup.RuntimeForJob(job)
	maxTurns := job.EffectiveMaxTurns(sc.Deploy)
	maxBudget := job.EffectiveMaxBudget(sc.Deploy)
	reason, detail := "", ""
	if rt != nil {
		runResult, _ := rt.ParseResult(sc.LogPath)
		outcome := rt.ClassifyOutcome(runResult, runtimepkg.Limits{MaxTurns: maxTurns, MaxBudgetUSD: maxBudget})
		reason = outcome.FailureReason
		detail = outcome.FailureDetail
	}

	// Only write classifier output when it produced a non-empty reason.
	// handleBailReport (called earlier in the bail path) may have already
	// written authoritative failure_reason/failure_detail from a structured
	// <bail-report> block; an unconditional UpdateJobFailure here would
	// clobber those fields with empty strings when ClassifyOutcome has
	// nothing to report (e.g. clean exit with no max_turns/max_budget/
	// is_error signal). When classifier has nothing, fall back to just
	// marking the job bailed so status still transitions. See #517.
	sc.EmitDurableWith(EventBailed, fmt.Sprintf("Agent bailed on %s", sc.JobLabel()),
		func(tx *sqlx.Tx) error {
			if reason != "" {
				return db.UpdateJobFailureTx(tx, job.ID, reason, detail)
			}
			return db.CompleteJobTx(tx, job.ID, db.StatusBailed)
		})
	sc.RecordLessonOutcome(false)

	return nil
}

// finalizeFailure handles runtime/process failures that are not deliberate
// structured agent bails.
func (m *DefaultJobManager) finalizeFailure(ctx context.Context, reason, detail string) error {
	sc := m.sc
	job := sc.Job
	ghClient := sc.NewGHClient()

	if job.IssueNumber > 0 {
		ghClient.RemoveLabel(ctx, sc.Owner, sc.Repo, job.IssueNumber, "in-progress")
	}

	if reason == "" {
		reason = "failed"
	}
	if detail == "" {
		detail = "agent failed without additional detail"
	}

	sc.EmitDurableWith(EventError, fmt.Sprintf("Agent failed on %s: %s", sc.JobLabel(), detail),
		func(tx *sqlx.Tx) error {
			if err := db.UpdateJobFailureTx(tx, job.ID, reason, detail); err != nil {
				return err
			}
			return db.CompleteJobTx(tx, job.ID, db.StatusFailed)
		})
	sc.RecordLessonOutcome(false)

	return nil
}

// finalizeManual handles runs where the agent completed local work and pushed a
// branch, but a human/token action is required to finish publication.
func (m *DefaultJobManager) finalizeManual(ctx context.Context, reason, detail string) error {
	sc := m.sc
	job := sc.Job
	ghClient := sc.NewGHClient()

	if job.IssueNumber > 0 {
		ghClient.RemoveLabel(ctx, sc.Owner, sc.Repo, job.IssueNumber, "in-progress")
	}

	if reason == "" {
		reason = "manual"
	}
	if detail == "" {
		detail = "agent completed work but manual follow-up is required"
	}

	sc.EmitDurableWith(EventManual, fmt.Sprintf("Agent needs manual follow-up on %s: %s", sc.JobLabel(), detail),
		func(tx *sqlx.Tx) error {
			if err := db.UpdateJobFailureTx(tx, job.ID, reason, detail); err != nil {
				return err
			}
			return db.CompleteJobTx(tx, job.ID, db.StatusManual)
		})
	sc.RecordLessonOutcome(false)

	return nil
}

// formatReviewFeedback formats review issues as additional context for a retry.
func formatReviewFeedback(assessment *ReviewAssessment) string {
	var b strings.Builder
	b.WriteString("## Review Feedback (from previous attempt)\n\n")
	b.WriteString("The review agent found the following issues with your previous attempt. ")
	b.WriteString("Please address these in your next iteration:\n\n")
	for i, issue := range assessment.Issues {
		fmt.Fprintf(&b, "%d. %s\n", i+1, issue)
	}
	if assessment.Summary != "" {
		fmt.Fprintf(&b, "\n**Summary:** %s\n", assessment.Summary)
	}
	return b.String()
}

// hasReviewStage checks if any stage uses the reviewer agent.
func hasReviewStage(stages []StageContract) bool {
	for _, s := range stages {
		if s.Agent == "reviewer" {
			return true
		}
	}
	return false
}

// extractReviewAssessmentFromLog is a package-level version of the review assessment extraction.
// If the SlotContext has a test hook, it uses that instead of the real LLM call.
func extractReviewAssessmentFromLog(ctx context.Context, logPath string, job *db.Job, sup *Supervisor, hooks *TestHooks) ReviewAssessment {
	if hooks != nil && hooks.ExtractReviewAssessmentFn != nil {
		return hooks.ExtractReviewAssessmentFn(ctx, logPath, job)
	}
	return sup.extractReviewAssessment(ctx, job, logPath)
}

// captureLessonsFromAssessment is a package-level version of lesson capture.
func captureLessonsFromAssessment(store *db.Store, owner, repo string, assessment ReviewAssessment) []*db.Lesson {
	var created []*db.Lesson
	scope := owner + "/" + repo
	existing, _ := store.GetActiveLessons(scope)

	for _, content := range assessment.Lessons {
		if len(content) < 10 || len(content) > 500 {
			continue
		}
		if lesson.IsDuplicate(content, existing) {
			continue
		}
		l := &db.Lesson{
			RepoScope: sql.NullString{String: scope, Valid: true},
			Content:   content,
			Source:    "review",
			Active:    true,
		}
		if err := store.CreateLesson(l); err == nil {
			created = append(created, l)
			existing = append(existing, l)
		}
	}
	return created
}

// -- Start: helper to create a slot context for testing --

// SlotContextForTest creates a SlotContext with minimal wiring (for unit tests).
func SlotContextForTest(store *db.Store, deploy *db.Deployment, job *db.Job) *SlotContext {
	return &SlotContext{
		Store:  store,
		Deploy: deploy,
		Job:    job,
	}
}

// NewTestSupervisor creates a Supervisor suitable for pipeline tests.
// It sets up the event channel, running map, and a no-op GH client factory.
func NewTestSupervisor(store *db.Store, deploy *db.Deployment, repoDir string) *Supervisor {
	return &Supervisor{
		store:     store,
		deploy:    deploy,
		repoDir:   repoDir,
		owner:     deploy.Owner,
		repo:      deploy.Repo,
		running:   make(map[int64]*runState),
		maxAgents: deploy.MaxAgents,
		events:    eventbus.New[Envelope](256),
		ghClientFactory: func(token string) *ghpkg.Client {
			return ghpkg.NewClient("") // no-op client (no token = all calls fail gracefully)
		},
	}
}

// RegisterTestJob registers a job in the supervisor's running map so that
// EmitEvent and WasStoppedByUser can reference it.
func (s *Supervisor) RegisterTestJob(job *db.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running[job.ID] = &runState{
		job:       job,
		startedAt: time.Now(),
	}
}

// DrainEvents returns all events currently retained by the supervisor bus.
// It exists for test harnesses; production consumers should use Subscribe.
func (s *Supervisor) DrainEvents() []Event {
	s.eventDrainMu.Lock()
	defer s.eventDrainMu.Unlock()
	replayed, err := s.eventBus().Replay(s.eventDrainCursor)
	if err != nil {
		return nil
	}
	events := make([]Event, len(replayed))
	for i, event := range replayed {
		events[i] = event.Value.Legacy()
		s.eventDrainCursor = event.Cursor
	}
	return events
}

// dirExists returns true if the path exists and is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
