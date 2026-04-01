// Package supervisor manages concurrent Claude Code agents working on GitHub issues.
package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
	gitpkg "github.com/aptx-health/agent-minder/internal/git"
	ghpkg "github.com/aptx-health/agent-minder/internal/github"
	"github.com/aptx-health/agent-minder/internal/lesson"
)

// debugLogger is a structured JSON logger for supervisor tracing.
var debugLogger *slog.Logger

func init() {
	if os.Getenv("MINDER_DEBUG") == "" {
		return
	}
	logPath := os.Getenv("MINDER_LOG")
	if logPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		logPath = filepath.Join(home, ".agent-minder", "debug.log")
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	debugLogger = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func debugLog(msg string, attrs ...any) {
	if debugLogger == nil {
		return
	}
	debugLogger.Info(msg, attrs...)
}

// Event is emitted by the supervisor for the TUI/API to consume.
type Event struct {
	Time    time.Time
	Type    string // "info", "started", "completed", "bailed", "stopped", "finished", "error", "warning"
	Summary string
	TaskID  int64
}

// SlotInfo describes the current state of an agent slot.
type SlotInfo struct {
	SlotNum     int
	IssueNumber int
	IssueTitle  string
	Branch      string
	RunningFor  time.Duration
	Status      string // "running" or "idle"
	Paused      bool
	IsReview    bool
	CurrentTool string
	ToolInput   string
	StepCount   int
}

type slotState struct {
	task          *db.Task
	startedAt     time.Time
	cmd           *exec.Cmd
	cancelFunc    context.CancelFunc
	stoppedByUser bool
	liveStatus    LiveStatus
}

// Supervisor manages concurrent autopilot agents.
type Supervisor struct {
	store   *db.Store
	deploy  *db.Deployment
	repoDir string
	owner   string
	repo    string
	ghToken string

	mu                 sync.Mutex
	gitSetupMu         sync.Mutex
	slots              []*slotState
	active             bool
	daemonMode         bool
	watchMode          bool
	paused             bool
	budgetPaused       bool
	budgetWarned       bool
	waitingHintEmitted bool
	events             chan Event
	parentCtx          context.Context
	cancel             context.CancelFunc
	done               chan struct{}
	reviewRetries      map[int64]int
	ghClientFactory    func(token string) *ghpkg.Client
}

// New creates a new Supervisor.
func New(store *db.Store, deploy *db.Deployment, repoDir, owner, repo, ghToken string) *Supervisor {
	maxAgents := deploy.MaxAgents
	if maxAgents < 1 {
		maxAgents = 3
	}
	return &Supervisor{
		store:         store,
		deploy:        deploy,
		repoDir:       repoDir,
		owner:         owner,
		repo:          repo,
		ghToken:       ghToken,
		slots:         make([]*slotState, maxAgents),
		events:        make(chan Event, 64),
		reviewRetries: make(map[int64]int),
	}
}

func (s *Supervisor) newGHClient() *ghpkg.Client {
	if s.ghClientFactory != nil {
		return s.ghClientFactory(s.ghToken)
	}
	return ghpkg.NewClient(s.ghToken)
}

// SetDaemonMode keeps the supervisor alive when all work completes.
func (s *Supervisor) SetDaemonMode(daemon bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.daemonMode = daemon
}

// Events returns the event channel.
func (s *Supervisor) Events() <-chan Event { return s.events }

// Done returns a channel that closes when the supervisor exits.
func (s *Supervisor) Done() <-chan struct{} { return s.done }

// IsActive returns whether the supervisor is running.
func (s *Supervisor) IsActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// IsBudgetPaused returns true if paused due to budget ceiling.
func (s *Supervisor) IsBudgetPaused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budgetPaused
}

// ResumeBudget clears the budget-paused state.
func (s *Supervisor) ResumeBudget() {
	s.mu.Lock()
	s.budgetPaused = false
	s.budgetWarned = false
	s.mu.Unlock()
}

// Pause prevents new agents from launching.
func (s *Supervisor) Pause() {
	s.mu.Lock()
	s.paused = true
	s.mu.Unlock()
}

// Resume allows new agents to launch.
func (s *Supervisor) Resume() {
	s.mu.Lock()
	s.paused = false
	s.mu.Unlock()
}

// SlotStatus returns the current state of all agent slots.
func (s *Supervisor) SlotStatus() []SlotInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	infos := make([]SlotInfo, len(s.slots))
	for i, slot := range s.slots {
		if slot == nil {
			infos[i] = SlotInfo{SlotNum: i, Status: "idle", Paused: s.paused || s.budgetPaused}
			continue
		}
		infos[i] = SlotInfo{
			SlotNum:     i,
			IssueNumber: slot.task.IssueNumber,
			IssueTitle:  slot.task.IssueTitle.String,
			Branch:      slot.task.Branch.String,
			RunningFor:  time.Since(slot.startedAt),
			Status:      "running",
			IsReview:    slot.task.Status == db.StatusReviewing,
			CurrentTool: slot.liveStatus.CurrentTool,
			ToolInput:   slot.liveStatus.ToolInput,
			StepCount:   slot.liveStatus.StepCount,
		}
	}
	return infos
}

// StopAgent stops a specific agent by slot index.
func (s *Supervisor) StopAgent(slotIdx int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slotIdx < 0 || slotIdx >= len(s.slots) || s.slots[slotIdx] == nil {
		return
	}
	s.slots[slotIdx].stoppedByUser = true
	if s.slots[slotIdx].cancelFunc != nil {
		s.slots[slotIdx].cancelFunc()
	}
}

// Launch starts the main supervisor loop.
func (s *Supervisor) Launch(ctx context.Context) {
	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		return
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.parentCtx = ctx
	s.active = true
	s.done = make(chan struct{})
	s.mu.Unlock()

	go func() {
		defer close(s.done)
		s.fillSlots(ctx)

		reviewTicker := time.NewTicker(30 * time.Second)
		defer reviewTicker.Stop()

		watchTicker := time.NewTicker(2 * time.Minute)
		defer watchTicker.Stop()
		// Initial watch poll if filter is configured.
		if s.addWatchTickerToLoop() {
			s.WatchTick(ctx)
		}

		for {
			select {
			case <-ctx.Done():
				s.mu.Lock()
				s.active = false
				s.mu.Unlock()
				s.emitEvent("finished", "Supervisor cancelled", 0)
				return

			case <-watchTicker.C:
				if s.addWatchTickerToLoop() {
					s.WatchTick(ctx)
				}

			case <-reviewTicker.C:
				s.enhancedCheckReviewTasks(ctx)
				if s.hasIdleSlot() {
					s.fillSlots(ctx)
				}

			default:
				if s.hasIdleSlot() {
					s.fillSlots(ctx)
				}
			}

			// Check if there's work remaining.
			if !s.hasWork() {
				s.mu.Lock()
				isDaemon := s.daemonMode
				s.mu.Unlock()
				if !isDaemon {
					break
				}
			}

			time.Sleep(2 * time.Second)
		}

		s.mu.Lock()
		s.active = false
		s.mu.Unlock()
		s.emitEvent("finished", "All agents finished", 0)
	}()
}

// Stop cancels all agents and waits for completion.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()

	if s.done != nil {
		<-s.done
	}
}

func (s *Supervisor) emitEvent(typ, summary string, taskID int64) {
	evt := Event{Time: time.Now(), Type: typ, Summary: summary, TaskID: taskID}
	select {
	case s.events <- evt:
	default:
		// Drop if channel full — non-blocking.
	}
}

func (s *Supervisor) hasIdleSlot() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, slot := range s.slots {
		if slot == nil {
			return true
		}
	}
	return false
}

func (s *Supervisor) hasWork() bool {
	s.mu.Lock()
	anyRunning := false
	for _, slot := range s.slots {
		if slot != nil {
			anyRunning = true
			break
		}
	}
	isPaused := s.paused
	s.mu.Unlock()

	if anyRunning {
		return true
	}

	tasks, _ := s.store.GetTasks(s.deploy.ID)
	waitingOnMerge := false
	for _, t := range tasks {
		switch t.Status {
		case db.StatusQueued, db.StatusBlocked, db.StatusReviewing, db.StatusManual:
			return true
		case db.StatusReview, db.StatusReviewed:
			waitingOnMerge = true
		}
	}
	if waitingOnMerge {
		s.emitWaitingForMerge()
		return true
	}
	return isPaused
}

// emitWaitingForMerge emits a one-time hint that the supervisor is waiting for PRs to be merged.
func (s *Supervisor) emitWaitingForMerge() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waitingHintEmitted {
		return
	}
	s.waitingHintEmitted = true
	// Emit outside lock via goroutine to avoid deadlock with channel.
	go s.emitEvent("info", "Waiting for PR(s) to be merged (checking every 30s, ctrl+c to exit)", 0)
}

func (s *Supervisor) fillSlots(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.paused || s.budgetPaused || ctx.Err() != nil {
		return
	}

	// Check budget ceiling.
	if s.deploy.TotalBudgetUSD > 0 {
		s.mu.Unlock()
		exceeded := s.checkBudgetCeiling()
		s.mu.Lock()
		if exceeded || s.paused || s.budgetPaused || ctx.Err() != nil {
			return
		}
	}

	// Single fetch before launching any agents.
	hasEmpty := false
	for _, slot := range s.slots {
		if slot == nil {
			hasEmpty = true
			break
		}
	}
	if hasEmpty {
		if err := gitpkg.Fetch(s.repoDir); err != nil {
			s.emitEvent("warning", fmt.Sprintf("Fetch failed: %v", err), 0)
		}
	}

	for i, slot := range s.slots {
		if slot != nil {
			continue
		}
		tasks, err := s.store.QueuedUnblockedTasks(s.deploy.ID)
		if err != nil || len(tasks) == 0 {
			break
		}
		s.launchAgent(ctx, i, tasks[0])
	}
}

func (s *Supervisor) checkBudgetCeiling() bool {
	spent, err := s.store.TotalSpend(s.deploy.ID)
	if err != nil {
		return false
	}
	ceiling := s.deploy.TotalBudgetUSD
	if ceiling <= 0 {
		return false
	}

	if spent >= ceiling {
		s.mu.Lock()
		s.budgetPaused = true
		s.mu.Unlock()
		s.emitEvent("warning", fmt.Sprintf("Budget ceiling reached: $%.2f / $%.2f", spent, ceiling), 0)
		return true
	}

	if !s.budgetWarned && spent >= ceiling*0.8 {
		s.mu.Lock()
		s.budgetWarned = true
		s.mu.Unlock()
		s.emitEvent("warning", fmt.Sprintf("Budget at 80%%: $%.2f / $%.2f", spent, ceiling), 0)
	}
	return false
}

func (s *Supervisor) launchAgent(ctx context.Context, slotIdx int, task *db.Task) {
	home, _ := os.UserHomeDir()
	worktreeBase := filepath.Join(home, ".agent-minder", "worktrees", s.deploy.ID)
	worktreePath := filepath.Join(worktreeBase, fmt.Sprintf("issue-%d", task.IssueNumber))
	branch := fmt.Sprintf("agent/issue-%d", task.IssueNumber)
	logDir := filepath.Join(home, ".agent-minder", "agents")
	logPath := filepath.Join(logDir, fmt.Sprintf("%s-issue-%d.log", s.deploy.ID, task.IssueNumber))

	_ = s.store.UpdateTaskWorktree(task.ID, worktreePath, branch)
	_ = s.store.UpdateTaskRunning(task.ID)
	// Update the task object too for later use.
	task.Status = db.StatusRunning

	slotCtx, slotCancel := context.WithCancel(ctx)
	s.slots[slotIdx] = &slotState{
		task:       task,
		startedAt:  time.Now(),
		cancelFunc: slotCancel,
	}

	go s.runAgent(slotCtx, slotIdx, task, worktreePath, branch, logPath, false)
}

func (s *Supervisor) runAgent(ctx context.Context, slotIdx int, task *db.Task,
	worktreePath, branch, logPath string, isResume bool) {

	defer func() {
		s.mu.Lock()
		s.slots[slotIdx] = nil
		s.mu.Unlock()
		// Re-evaluate and fill slots using parent context.
		s.fillSlots(s.parentCtx)
	}()

	failStatus := db.StatusBailed
	if isResume {
		failStatus = "failed"
	}

	home, _ := os.UserHomeDir()

	// Ensure directories.
	if !isResume {
		_ = os.MkdirAll(filepath.Dir(worktreePath), 0755)
	}
	_ = os.MkdirAll(filepath.Join(home, ".agent-minder", "agents"), 0755)

	// Get base branch (onboarding.yaml > deploy flag > git default).
	baseBranch := resolveBaseBranch(s.repoDir, s.deploy)

	if !isResume {
		// Serialize git worktree operations.
		s.gitSetupMu.Lock()
		_ = gitpkg.DeleteBranch(s.repoDir, branch)
		gitErr := gitpkg.WorktreeAdd(s.repoDir, worktreePath, branch, "origin/"+baseBranch)
		s.gitSetupMu.Unlock()

		if gitErr != nil {
			s.emitEvent("error", fmt.Sprintf("Worktree setup failed for #%d: %v", task.IssueNumber, gitErr), task.ID)
			_ = s.store.UpdateTaskStatus(task.ID, failStatus)
			return
		}
	}

	s.emitEvent("started", fmt.Sprintf("Agent started on #%d: %s", task.IssueNumber, task.IssueTitle.String), task.ID)

	// Add in-progress label.
	ghClient := s.newGHClient()
	_ = ghClient.AddLabel(ctx, s.owner, s.repo, task.IssueNumber, "in-progress")

	// Open log file.
	var logFile *os.File
	var err error
	if isResume {
		logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	} else {
		logFile, err = os.Create(logPath)
	}
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Log file error for #%d: %v", task.IssueNumber, err), task.ID)
		_ = s.store.UpdateTaskStatus(task.ID, failStatus)
		return
	}
	defer func() { _ = logFile.Close() }()

	// Ensure agent definition exists.
	agentDefSource, err := ensureAgentDef(worktreePath)
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Agent def error for #%d: %v", task.IssueNumber, err), task.ID)
		_ = s.store.UpdateTaskStatus(task.ID, failStatus)
		return
	}
	debugLog("agent def resolved", "issue", task.IssueNumber, "source", string(agentDefSource))

	// Build command args.
	allowedTools := resolveAllowedTools(s.repoDir)
	testCommand := resolveTestCommand(s.repoDir)

	// Load related work context.
	var rw *relatedWork
	dg, dgErr := s.store.GetDepGraph(s.deploy.ID)
	siblings, sibErr := s.store.GetTasks(s.deploy.ID)
	if dgErr == nil || sibErr == nil {
		rw = &relatedWork{}
		if dgErr == nil && dg != nil {
			rw.depGraph = dg.GraphJSON
		}
		if sibErr == nil {
			rw.siblingTasks = siblings
		}
	}

	// Pre-fetch issue comments.
	issueComments := s.fetchIssueComments(ctx, task)

	// Select and inject lessons.
	lessonsPrompt := s.selectAndRecordLessons(task)

	var args []string
	if isResume {
		args = buildResumeClaudeArgs(task, s.deploy, baseBranch, testCommand, allowedTools, rw, issueComments, lessonsPrompt)
	} else {
		args = buildClaudeArgs(task, s.deploy, baseBranch, testCommand, allowedTools, rw, issueComments, lessonsPrompt)
	}

	debugLog("claude command", "issue", task.IssueNumber, "args", strings.Join(args[:len(args)-1], " "))

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = worktreePath
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+s.ghToken)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Stdout pipe error for #%d: %v", task.IssueNumber, err), task.ID)
		_ = s.store.UpdateTaskStatus(task.ID, failStatus)
		return
	}

	s.mu.Lock()
	if s.slots[slotIdx] != nil {
		s.slots[slotIdx].cmd = cmd
	}
	s.mu.Unlock()

	if err := cmd.Start(); err != nil {
		s.emitEvent("error", fmt.Sprintf("Agent start failed for #%d: %v", task.IssueNumber, err), task.ID)
		_ = s.store.UpdateTaskStatus(task.ID, failStatus)
		return
	}

	// Scanner goroutine for live status.
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanStream(stdout, logFile, slotIdx, s)
	}()

	err = cmd.Wait()
	<-scanDone

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	// Check user-initiated stop.
	s.mu.Lock()
	stoppedByUser := s.slots[slotIdx] != nil && s.slots[slotIdx].stoppedByUser
	s.mu.Unlock()

	if stoppedByUser {
		_ = s.store.UpdateTaskStatus(task.ID, db.StatusStopped)
		s.emitEvent("stopped", fmt.Sprintf("Agent stopped by user on #%d", task.IssueNumber), task.ID)
		return
	}

	// Inspect outcome.
	status := s.inspectOutcome(ctx, task, logPath, exitCode)
	_ = s.store.UpdateTaskStatus(task.ID, status)

	// Update cost from log.
	if cost := parseCostFromLog(logPath); cost > 0 {
		_ = s.store.UpdateTaskCost(task.ID, cost)
	}

	switch status {
	case db.StatusReview:
		ghClient.RemoveLabel(ctx, s.owner, s.repo, task.IssueNumber, "in-progress")
		_ = ghClient.AddLabel(ctx, s.owner, s.repo, task.IssueNumber, "needs-review")
		s.emitEvent("completed", fmt.Sprintf("Agent completed #%d — PR opened", task.IssueNumber), task.ID)
		// Record lessons as helpful (task produced a PR).
		_ = lesson.RecordOutcome(s.store, task.ID, true)
	case db.StatusBailed:
		ghClient.RemoveLabel(ctx, s.owner, s.repo, task.IssueNumber, "in-progress")
		s.emitEvent("bailed", fmt.Sprintf("Agent bailed on #%d (exit %d)", task.IssueNumber, exitCode), task.ID)
		// Record lessons as unhelpful (task bailed).
		_ = lesson.RecordOutcome(s.store, task.ID, false)
	default:
		ghClient.RemoveLabel(ctx, s.owner, s.repo, task.IssueNumber, "in-progress")
		s.emitEvent("bailed", fmt.Sprintf("Agent finished #%d with status %s", task.IssueNumber, status), task.ID)
		_ = lesson.RecordOutcome(s.store, task.ID, false)
	}
}

// inspectOutcome examines the agent log and exit code to determine task status.
func (s *Supervisor) inspectOutcome(ctx context.Context, task *db.Task, logPath string, exitCode int) string {
	// Parse the agent result from the log.
	result, _ := parseAgentLog(logPath)

	// Classify the outcome.
	maxTurns := task.EffectiveMaxTurns(s.deploy)
	maxBudget := task.EffectiveMaxBudget(s.deploy)
	status, reason, detail := classifyOutcome(result, maxTurns, maxBudget)

	// Check for PR: even if the agent failed, it may have opened a PR.
	prNum := detectPRFromLog(logPath, s.owner, s.repo)
	if prNum > 0 {
		_ = s.store.UpdateTaskPR(task.ID, prNum)
		return db.StatusReview
	}

	// Also try GitHub API.
	ghClient := s.newGHClient()
	branch := fmt.Sprintf("agent/issue-%d", task.IssueNumber)
	prs, err := ghClient.ListPRsForBranch(ctx, s.owner, s.repo, branch)
	if err == nil && len(prs) > 0 {
		_ = s.store.UpdateTaskPR(task.ID, prs[0])
		return db.StatusReview
	}

	// No PR found — record failure info.
	if status == "failed" || status == "warning" {
		_ = s.store.UpdateTaskFailure(task.ID, reason, detail)
		if status == "warning" {
			// Non-fatal (e.g., permission denials) but no PR — still bailed.
			return db.StatusBailed
		}
		return db.StatusBailed
	}

	return db.StatusBailed
}

// fetchIssueComments fetches the latest comments for context injection.
func (s *Supervisor) fetchIssueComments(ctx context.Context, task *db.Task) string {
	ghClient := s.newGHClient()
	content, err := ghClient.FetchItemContent(ctx, s.owner, s.repo, task.IssueNumber, "issue")
	if err != nil {
		return ""
	}
	return strings.Join(content.Comments, "\n\n")
}

// parseCostFromLog extracts cost from the agent log (stream-json format).
func parseCostFromLog(logPath string) float64 {
	// Parse the last result event for total_cost_usd.
	data, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}
	// Simple scan for "total_cost_usd" in the log.
	lines := strings.Split(string(data), "\n")
	var cost float64
	for _, line := range lines {
		if idx := strings.Index(line, `"total_cost_usd"`); idx >= 0 {
			// Extract the number after the colon.
			rest := line[idx+len(`"total_cost_usd"`):]
			rest = strings.TrimLeft(rest, `: `)
			end := strings.IndexAny(rest, ",}")
			if end > 0 {
				if v, err := strconv.ParseFloat(strings.TrimSpace(rest[:end]), 64); err == nil && v > cost {
					cost = v
				}
			}
		}
	}
	return cost
}

// detectPRFromLog scans the log for PR creation output.
func detectPRFromLog(logPath, owner, repo string) int {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}
	// Look for "github.com/owner/repo/pull/N" pattern.
	target := fmt.Sprintf("github.com/%s/%s/pull/", owner, repo)
	s := string(data)
	idx := strings.LastIndex(s, target)
	if idx < 0 {
		return 0
	}
	rest := s[idx+len(target):]
	end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if end <= 0 {
		return 0
	}
	num, _ := strconv.Atoi(rest[:end])
	return num
}

// selectAndRecordLessons selects relevant lessons and records the injection.
func (s *Supervisor) selectAndRecordLessons(task *db.Task) string {
	lessons, err := lesson.SelectLessons(s.store, s.owner, s.repo)
	if err != nil || len(lessons) == 0 {
		return ""
	}

	// Record which lessons were injected.
	_ = lesson.RecordInjection(s.store, task.ID, lessons)

	debugLog("lessons injected", "issue", task.IssueNumber, "count", len(lessons))
	return lesson.FormatForPrompt(lessons)
}

// UpdateLiveStatus is called by the scanner goroutine to update slot live status.
func (s *Supervisor) UpdateLiveStatus(slotIdx int, status LiveStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slotIdx >= 0 && slotIdx < len(s.slots) && s.slots[slotIdx] != nil {
		s.slots[slotIdx].liveStatus = status
	}
}
