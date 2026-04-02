package supervisor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aptx-health/agent-minder/internal/db"
	gitpkg "github.com/aptx-health/agent-minder/internal/git"
	ghpkg "github.com/aptx-health/agent-minder/internal/github"
	"github.com/aptx-health/agent-minder/internal/lesson"
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
	AllowedTools []string

	// Internal reference to supervisor.
	sup *Supervisor
}

// EmitEvent emits a supervisor event for this job.
func (sc *SlotContext) EmitEvent(typ, summary string) {
	sc.sup.emitEvent(typ, summary, sc.Job.ID)
}

// JobLabel returns a human-readable identifier: "#42" for reactive, "job-name" for proactive.
func (sc *SlotContext) JobLabel() string {
	if sc.Job.IssueNumber > 0 {
		return fmt.Sprintf("#%d", sc.Job.IssueNumber)
	}
	return sc.Job.Name
}

// NewGHClient creates a GitHub client.
func (sc *SlotContext) NewGHClient() *ghpkg.Client {
	return sc.sup.newGHClient()
}

// SetupWorktree creates a git worktree for this job.
// Cleans up any stale worktree/branch from a previous run first.
// Safe to call concurrently — uses the supervisor's gitSetupMu.
func (sc *SlotContext) SetupWorktree() error {
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

// EnsureAgentDef ensures the agent definition exists in the worktree.
func (sc *SlotContext) EnsureAgentDef(name AgentName) (AgentDefSource, error) {
	return ensureAgentDefByName(sc.WorktreePath, name)
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

// RunClaudeAgent executes a claude agent process and streams output.
// Returns the exit code and any error.
func (sc *SlotContext) RunClaudeAgent(ctx context.Context, args []string, logFile *os.File) (int, error) {
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = sc.WorktreePath
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+sc.GHToken)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("stdout pipe: %w", err)
	}

	// Register cmd for cancellation.
	sc.sup.mu.Lock()
	if rs, ok := sc.sup.running[sc.Job.ID]; ok {
		rs.cmd = cmd
	}
	sc.sup.mu.Unlock()

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start: %w", err)
	}

	// Scanner goroutine for live status.
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanStream(stdout, logFile, sc.Job.ID, sc.sup)
	}()

	err = cmd.Wait()
	<-scanDone

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	return exitCode, nil
}

// DetectPR looks for a PR in the log or via GitHub API.
func (sc *SlotContext) DetectPR(ctx context.Context) int {
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
func (sc *SlotContext) RecordLessonOutcome(success bool) {
	_ = lesson.RecordOutcome(sc.Store, sc.Job.ID, success)
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
		AllowedTools: resolveAllowedTools(s.repoDir),
		sup:          s,
	}
}

// --- DefaultJobManager ---

// DefaultJobManager implements the standard autopilot→review flow.
type DefaultJobManager struct {
	sc       *SlotContext
	contract *AgentContract
}

// NewDefaultJobManager creates a job manager for the standard code→review flow.
func NewDefaultJobManager(sc *SlotContext, contract *AgentContract) *DefaultJobManager {
	return &DefaultJobManager{sc: sc, contract: contract}
}

// Run executes the code stage and optional review stage.
func (m *DefaultJobManager) Run(ctx context.Context) error {
	sc := m.sc
	job := sc.Job
	contract := m.contract

	sc.EmitEvent("started", fmt.Sprintf("Agent started on %s: %s", sc.JobLabel(), job.IssueTitle.String))
	_ = sc.Store.UpdateJobStage(job.ID, "code", "")

	// Setup worktree for write agents, or use repo dir for read-only.
	if contract.NeedsWorktree() {
		if err := sc.SetupWorktree(); err != nil {
			sc.EmitEvent("error", fmt.Sprintf("Worktree setup failed for %s: %v", sc.JobLabel(), err))
			_ = sc.Store.UpdateJobFailure(job.ID, "worktree", err.Error())
			return err
		}
	}

	// Resolve agent def.
	agentName := AgentName(job.Agent)
	agentDefSrc, err := sc.EnsureAgentDef(agentName)
	if err != nil {
		sc.EmitEvent("error", fmt.Sprintf("Agent def error for %s: %v", sc.JobLabel(), err))
		_ = sc.Store.UpdateJobFailure(job.ID, "agent_def", err.Error())
		return err
	}
	debugLog("agent def resolved", "agent", job.Agent, "source", string(agentDefSrc))

	// Label in-progress for reactive jobs.
	ghClient := sc.NewGHClient()
	if job.IssueNumber > 0 {
		_ = ghClient.AddLabel(ctx, sc.Owner, sc.Repo, job.IssueNumber, "in-progress")
	}

	// Open log.
	logFile, err := sc.OpenLogFile(false)
	if err != nil {
		sc.EmitEvent("error", fmt.Sprintf("Log file error for %s: %v", sc.JobLabel(), err))
		_ = sc.Store.UpdateJobFailure(job.ID, "log", err.Error())
		return err
	}
	defer func() { _ = logFile.Close() }()

	// Assemble prompt from context providers.
	prompt := AssembleContext(ctx, sc, contract.Context)
	lessonsPrompt := sc.SelectAndRecordLessons()

	// Build claude CLI args.
	args := buildAgentArgs(job, sc.Deploy, job.Agent, sc.AllowedTools, prompt, lessonsPrompt)

	debugLog("claude command", "agent", job.Agent, "label", sc.JobLabel())

	exitCode, runErr := sc.RunClaudeAgent(ctx, args, logFile)

	// Check user-initiated stop.
	if sc.WasStoppedByUser() {
		_ = sc.Store.UpdateJobStatus(job.ID, db.StatusStopped)
		sc.EmitEvent("stopped", fmt.Sprintf("Agent stopped by user on %s", sc.JobLabel()))
		return nil
	}

	// Extract cost.
	if cost := sc.ParseCost(); cost > 0 {
		_ = sc.Store.UpdateJobCost(job.ID, cost)
	}

	// Handle outcome based on output type.
	switch contract.Output {
	case "pr":
		return m.handlePROutcome(ctx, logFile, exitCode, runErr)
	default:
		// issue, report, comment, none — job is done when agent finishes.
		_ = sc.Store.CompleteJob(job.ID, db.StatusDone)
		sc.EmitEvent("completed", fmt.Sprintf("Agent completed %s", sc.JobLabel()))
		sc.RecordLessonOutcome(true)
		return runErr
	}
}

// handlePROutcome handles the outcome for PR-producing agents.
func (m *DefaultJobManager) handlePROutcome(ctx context.Context, logFile *os.File, exitCode int, runErr error) error {
	sc := m.sc
	job := sc.Job
	ghClient := sc.NewGHClient()

	prNum := sc.DetectPR(ctx)
	if prNum > 0 {
		_ = sc.Store.UpdateJobPR(job.ID, prNum)
		job.PRNumber = sql.NullInt64{Int64: int64(prNum), Valid: true}

		if job.IssueNumber > 0 {
			ghClient.RemoveLabel(ctx, sc.Owner, sc.Repo, job.IssueNumber, "in-progress")
			_ = ghClient.AddLabel(ctx, sc.Owner, sc.Repo, job.IssueNumber, "needs-review")
		}
		_ = sc.Store.UpdateJobStatus(job.ID, db.StatusReview)
		sc.EmitEvent("completed", fmt.Sprintf("Agent completed %s — PR #%d opened", sc.JobLabel(), prNum))
		sc.RecordLessonOutcome(true)

		// Stage 2: Review.
		if sc.Deploy.ReviewEnabled && m.hasReviewStage() {
			_ = sc.Store.UpdateJobStage(job.ID, "review", "")
			m.runReviewStage(ctx, logFile)
		}
		return nil
	}

	// No PR — classify failure.
	if job.IssueNumber > 0 {
		ghClient.RemoveLabel(ctx, sc.Owner, sc.Repo, job.IssueNumber, "in-progress")
	}
	result, _ := parseAgentLog(sc.LogPath)
	maxTurns := job.EffectiveMaxTurns(sc.Deploy)
	maxBudget := job.EffectiveMaxBudget(sc.Deploy)
	_, reason, detail := classifyOutcome(result, maxTurns, maxBudget)

	_ = sc.Store.UpdateJobFailure(job.ID, reason, detail)
	sc.EmitEvent("bailed", fmt.Sprintf("Agent bailed on %s (exit %d)", sc.JobLabel(), exitCode))
	sc.RecordLessonOutcome(false)

	// Extract and handle structured bail report from agent output.
	// Try result text first; handleBailReport falls back to scanning the log.
	resultText := ""
	if result != nil {
		resultText = result.Result
	}
	m.handleBailReport(ctx, resultText)

	if runErr != nil {
		return runErr
	}
	return nil
}

// hasReviewStage returns true if the contract includes a review stage.
func (m *DefaultJobManager) hasReviewStage() bool {
	for _, s := range m.contract.Stages {
		if s.Name == "review" {
			return true
		}
	}
	// Also check deploy-level review setting even without explicit stage.
	return m.sc.Deploy.ReviewEnabled
}

// runReviewStage runs the review agent as the second stage.
func (m *DefaultJobManager) runReviewStage(ctx context.Context, parentLogFile *os.File) {
	sc := m.sc
	job := sc.Job

	// Ensure reviewer agent def.
	_, err := sc.EnsureAgentDef(AgentReviewer)
	if err != nil {
		sc.EmitEvent("error", fmt.Sprintf("Reviewer agent def error: %v", err))
		return
	}

	sc.EmitEvent("started", fmt.Sprintf("Review started on %s (PR #%d)", sc.JobLabel(), job.PRNumber.Int64))
	_ = sc.Store.UpdateJobStatus(job.ID, db.StatusReviewing)

	_, _ = fmt.Fprintf(parentLogFile, "\n\n--- REVIEW AGENT ---\n\n")

	// Build review context and args.
	prompt := renderReviewContext(ctx, sc)
	args := buildAgentArgs(job, sc.Deploy, "reviewer", sc.AllowedTools, prompt, "")

	_, _ = sc.RunClaudeAgent(ctx, args, parentLogFile)

	// Extract structured assessment.
	assessment := extractReviewAssessmentFromLog(ctx, sc.LogPath, job, sc.sup)

	risk := assessment.Risk
	if risk == "" {
		risk = "needs-testing"
	}

	_ = sc.Store.UpdateJobReview(job.ID, risk, 0)
	_ = sc.Store.UpdateJobStatus(job.ID, db.StatusReviewed)

	sc.EmitEvent("completed", fmt.Sprintf("Review of %s complete (risk: %s)", sc.JobLabel(), risk))
	if assessment.Summary != "" {
		sc.EmitEvent("info", fmt.Sprintf("Review: %s", assessment.Summary))
	}

	// Auto-capture lessons.
	if len(assessment.Lessons) > 0 {
		captured := captureLessonsFromAssessment(sc.Store, sc.Owner, sc.Repo, assessment)
		if len(captured) > 0 {
			sc.EmitEvent("info", fmt.Sprintf("Captured %d lessons from review of %s", len(captured), sc.JobLabel()))
		}
	}

	// Auto-merge if configured and low-risk — uses GitHub auto-merge (waits for CI).
	if sc.Deploy.AutoMerge && risk == "low-risk" && job.PRNumber.Valid {
		ghClient := sc.NewGHClient()
		if err := ghClient.EnableAutoMerge(ctx, sc.Owner, sc.Repo, int(job.PRNumber.Int64), "merge"); err == nil {
			sc.EmitEvent("info", fmt.Sprintf("Auto-merge enabled for PR #%d (will merge when CI passes)", job.PRNumber.Int64))
		} else {
			sc.EmitEvent("warning", fmt.Sprintf("Auto-merge failed for PR #%d: %v", job.PRNumber.Int64, err))
		}
	}
}

// extractReviewAssessmentFromLog is a package-level version of the review assessment extraction.
func extractReviewAssessmentFromLog(ctx context.Context, logPath string, job *db.Job, sup *Supervisor) ReviewAssessment {
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
