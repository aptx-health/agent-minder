// Package autopilot manages automated Claude Code agents that work on GitHub issues.
package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dustinlange/agent-minder/internal/claudecli"
	"github.com/dustinlange/agent-minder/internal/db"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
	"github.com/dustinlange/agent-minder/internal/onboarding"
)

// debugLogger is a structured JSON logger for autopilot tracing.
// Nil when MINDER_DEBUG is not set.
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

// debugLog logs a structured message to the debug log file.
func debugLog(msg string, attrs ...any) {
	if debugLogger == nil {
		return
	}
	debugLogger.Info(msg, attrs...)
}

// DepOption represents one ranked dependency graph option produced by the LLM.
type DepOption struct {
	Name      string                     // e.g., "Conservative — minimal deps"
	Rationale string                     // why this ordering makes sense
	Graph     map[string]json.RawMessage // issue# → deps array or "skip"
	Unblocked int                        // pre-computed count of unblocked tasks
}

// PrepareResult holds the outcome of a Prepare() call.
type PrepareResult struct {
	Total     int            // total tasks (existing + new)
	Options   []DepOption    // dep graph options (empty if reusing stored graph)
	AgentDef  AgentDefSource // which agent definition tier was detected
	Existing  int            // count of preserved tasks from previous session
	NewIssues int            // count of newly discovered issues not yet in task table
	HasGraph  bool           // true if a stored dep graph was found
	// Status counts for existing tasks.
	Done   int
	Manual int
}

// Event is emitted by the supervisor for the TUI to consume.
type Event struct {
	Time    time.Time
	Type    string // "info", "started", "completed", "bailed", "stopped", "finished", "error", "warning", "discovered"
	Summary string
	Task    *db.AutopilotTask
}

// SlotInfo describes the current state of an agent slot for TUI display.
type SlotInfo struct {
	SlotNum     int
	IssueNumber int
	IssueTitle  string
	Branch      string
	RunningFor  time.Duration
	Status      string // "running" or "idle"
	Paused      bool   // true when slot filling is paused (idle slots show "(paused)")
	IsReview    bool   // true when slot is running a review agent (task status "reviewing")

	// Live status from stream-json parsing.
	CurrentTool string
	ToolInput   string
	StepCount   int
}

type slotState struct {
	task          *db.AutopilotTask
	startedAt     time.Time
	cmd           *exec.Cmd
	cancelFunc    context.CancelFunc // per-slot cancel; nil before launch
	stoppedByUser bool               // set by StopAgent before cancelling
	liveStatus    LiveStatus         // updated by scanner goroutine
}

// Supervisor manages concurrent autopilot agents working on GitHub issues.
type Supervisor struct {
	store     *db.Store
	project   *db.Project
	completer claudecli.Completer
	repoDir   string // primary repo for worktree operations
	owner     string
	repo      string
	ghToken   string

	mu              sync.Mutex
	gitSetupMu      sync.Mutex   // serializes git branch/worktree ops to avoid ref lock contention
	slots           []*slotState // len == maxAgents; nil = idle
	active          bool
	paused          bool // when true, fillSlots returns early
	preparedAt      time.Time
	events          chan Event
	parentCtx       context.Context // parent context for the session (used by slot goroutines for fillSlots)
	cancel          context.CancelFunc
	done            chan struct{}
	reviewRetries   map[int64]int                    // in-memory retry count for review agent transient failures
	ghClientFactory func(token string) *ghpkg.Client // override for testing; nil uses ghpkg.NewClient
}

// New creates a new Supervisor.
func New(store *db.Store, project *db.Project, completer claudecli.Completer, repoDir, owner, repo, ghToken string) *Supervisor {
	maxAgents := project.AutopilotMaxAgents
	if maxAgents < 1 {
		maxAgents = 3
	}
	return &Supervisor{
		store:         store,
		project:       project,
		completer:     completer,
		repoDir:       repoDir,
		owner:         owner,
		repo:          repo,
		ghToken:       ghToken,
		slots:         make([]*slotState, maxAgents),
		events:        make(chan Event, 64),
		reviewRetries: make(map[int64]int),
	}
}

// newGHClient returns a GitHub client, using the test factory if set.
func (s *Supervisor) newGHClient() *ghpkg.Client {
	if s.ghClientFactory != nil {
		return s.ghClientFactory(s.ghToken)
	}
	return ghpkg.NewClient(s.ghToken)
}

// Events returns the channel of events for the TUI.
func (s *Supervisor) Events() <-chan Event {
	return s.events
}

// Done returns a channel that is closed when the supervisor finishes all work.
// Returns nil if the supervisor has not been launched.
func (s *Supervisor) Done() <-chan struct{} {
	return s.done
}

// IsActive returns true if the supervisor has been launched and is running.
func (s *Supervisor) IsActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// StopAgent cancels a single agent slot, leaving other agents running.
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

// ReviewSessionResult holds the outcome of launching a review session.
type ReviewSessionResult struct {
	WorktreePath string
	SessionID    string
	PRNumber     int
	IssueNumber  int
}

// ReviewSession restores a worktree for a review task and launches a Claude session
// pre-loaded with PR context. Returns the session ID so the user can resume it.
func (s *Supervisor) ReviewSession(ctx context.Context, taskID int64) (*ReviewSessionResult, error) {
	tasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return nil, fmt.Errorf("get tasks: %w", err)
	}

	var task *db.AutopilotTask
	for i := range tasks {
		if tasks[i].ID == taskID {
			task = &tasks[i]
			break
		}
	}
	if task == nil {
		return nil, fmt.Errorf("task %d not found", taskID)
	}
	if task.Status != "review" {
		return nil, fmt.Errorf("task #%d has status %q — only review tasks can start a review session", task.IssueNumber, task.Status)
	}
	if task.PRNumber <= 0 {
		return nil, fmt.Errorf("task #%d has no associated PR", task.IssueNumber)
	}

	// Restore worktree if it doesn't exist.
	if _, err := os.Stat(task.WorktreePath); os.IsNotExist(err) {
		// Fetch to ensure branch is up-to-date.
		_ = gitpkg.Fetch(s.repoDir)

		if err := gitpkg.WorktreeAddExisting(s.repoDir, task.WorktreePath, task.Branch); err != nil {
			return nil, fmt.Errorf("restore worktree: %w", err)
		}
	}

	// Build a review prompt.
	prompt := fmt.Sprintf(
		"You are starting a review session for PR #%d (issue #%d) in %s/%s.\n\n"+
			"1. Run `gh pr view %d -R %s/%s` to fetch the PR details\n"+
			"2. Run `gh pr diff %d -R %s/%s` to see the changes\n"+
			"3. Summarize what the PR does and note any concerns\n"+
			"4. Then tell the user you're ready for their review instructions\n\n"+
			"Worktree: %s\nBranch: %s",
		task.PRNumber, task.IssueNumber, s.owner, s.repo,
		task.PRNumber, s.owner, s.repo,
		task.PRNumber, s.owner, s.repo,
		task.WorktreePath, task.Branch,
	)

	// Launch claude -p with stream-json so we can capture the session ID.
	// Use --allowedTools for least-privilege access scoped to review needs.
	allowedTools := resolveAllowedTools(s.repoDir)
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", "3",
		"--allowedTools", toCliAllowedTools(allowedTools),
		"--", prompt,
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = task.WorktreePath
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+s.ghToken)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	// Parse stream-json output for session_id from the init event.
	var sessionID string
	scanner := json.NewDecoder(stdout)
	for scanner.More() {
		var raw json.RawMessage
		if err := scanner.Decode(&raw); err != nil {
			break
		}
		var initEvt struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(raw, &initEvt) == nil && initEvt.Type == "system" && initEvt.Subtype == "init" && initEvt.SessionID != "" {
			sessionID = initEvt.SessionID
		}
	}

	// Wait for the process to finish.
	_ = cmd.Wait()

	if sessionID == "" {
		// Session still usable — user can just cd into the worktree and run claude.
		return &ReviewSessionResult{
			WorktreePath: task.WorktreePath,
			PRNumber:     task.PRNumber,
			IssueNumber:  task.IssueNumber,
		}, nil
	}

	return &ReviewSessionResult{
		WorktreePath: task.WorktreePath,
		SessionID:    sessionID,
		PRNumber:     task.PRNumber,
		IssueNumber:  task.IssueNumber,
	}, nil
}

// RestartTask cleans up a bailed or stopped task and re-queues it.
func (s *Supervisor) RestartTask(ctx context.Context, taskID int64) error {
	tasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return fmt.Errorf("get tasks: %w", err)
	}

	var task *db.AutopilotTask
	for i := range tasks {
		if tasks[i].ID == taskID {
			task = &tasks[i]
			break
		}
	}
	if task == nil {
		return fmt.Errorf("task %d not found", taskID)
	}
	if task.Status != "bailed" && task.Status != "stopped" && task.Status != "failed" {
		return fmt.Errorf("task #%d has status %q — only bailed, stopped, or failed tasks can be restarted", task.IssueNumber, task.Status)
	}

	// Clean up worktree and branch from the previous attempt.
	if task.WorktreePath != "" {
		_ = gitpkg.WorktreeRemove(s.repoDir, task.WorktreePath)
	}
	if task.Branch != "" {
		_ = gitpkg.DeleteBranch(s.repoDir, task.Branch)
	}

	// Reset task fields in DB.
	if err := s.store.ResetAutopilotTask(task.ID); err != nil {
		return fmt.Errorf("reset task: %w", err)
	}

	s.emitEvent("started", fmt.Sprintf("Task #%d re-queued for restart", task.IssueNumber), task)

	// Try to fill slots with the re-queued task.
	s.fillSlots(ctx)
	return nil
}

// ResumeTask resumes a failed or stopped task in its existing worktree.
// Unlike RestartTask which nukes the worktree and starts fresh, ResumeTask
// preserves the prior work and launches the agent with a continuation prompt.
func (s *Supervisor) ResumeTask(ctx context.Context, taskID int64) error {
	tasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return fmt.Errorf("get tasks: %w", err)
	}

	var task *db.AutopilotTask
	for i := range tasks {
		if tasks[i].ID == taskID {
			task = &tasks[i]
			break
		}
	}
	if task == nil {
		return fmt.Errorf("task %d not found", taskID)
	}
	if task.Status != "failed" && task.Status != "stopped" {
		return fmt.Errorf("task #%d has status %q — only failed or stopped tasks can be resumed", task.IssueNumber, task.Status)
	}

	// Validate worktree still exists on disk.
	if task.WorktreePath == "" {
		return fmt.Errorf("task #%d has no worktree path — use restart instead", task.IssueNumber)
	}
	if _, err := os.Stat(task.WorktreePath); os.IsNotExist(err) {
		return fmt.Errorf("worktree for #%d no longer exists at %s — use restart instead", task.IssueNumber, task.WorktreePath)
	}

	// Find an idle slot.
	s.mu.Lock()
	idleSlot := -1
	for i, slot := range s.slots {
		if slot == nil {
			idleSlot = i
			break
		}
	}
	s.mu.Unlock()
	if idleSlot < 0 {
		return fmt.Errorf("no idle slots available — wait for a running agent to finish")
	}

	// Reset failure fields and set to running, preserving worktree/branch/log.
	if err := s.store.ResumeAutopilotTask(task.ID); err != nil {
		return fmt.Errorf("resume task: %w", err)
	}

	s.emitEvent("started", fmt.Sprintf("Resuming task #%d in existing worktree", task.IssueNumber), task)

	// Launch directly in the existing worktree with a continuation prompt.
	s.resumeAgent(ctx, idleSlot, task)
	return nil
}

// BumpTaskLimits applies 1.5x resource overrides to a task.
// Uses the task's current effective limits (override or project default) as the base.
func (s *Supervisor) BumpTaskLimits(taskID int64) (newTurns int, newBudget float64, err error) {
	tasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return 0, 0, fmt.Errorf("get tasks: %w", err)
	}

	var task *db.AutopilotTask
	for i := range tasks {
		if tasks[i].ID == taskID {
			task = &tasks[i]
			break
		}
	}
	if task == nil {
		return 0, 0, fmt.Errorf("task %d not found", taskID)
	}

	baseTurns := task.EffectiveMaxTurns(s.project.AutopilotMaxTurns)
	baseBudget := task.EffectiveMaxBudget(s.project.AutopilotMaxBudgetUSD)

	newTurns = int(float64(baseTurns) * 1.5)
	newBudget = baseBudget * 1.5

	if err := s.store.UpdateAutopilotTaskOverrides(task.ID, &newTurns, &newBudget); err != nil {
		return 0, 0, fmt.Errorf("update overrides: %w", err)
	}

	return newTurns, newBudget, nil
}

// DeleteWorktree removes the worktree and optionally the branch for a completed/failed/bailed task.
// The task record stays in the DB; only the on-disk worktree is removed.
// If deleteBranch is true, the local branch is also deleted.
func (s *Supervisor) DeleteWorktree(_ context.Context, taskID int64, deleteBranch bool) error {
	tasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return fmt.Errorf("get tasks: %w", err)
	}

	var task *db.AutopilotTask
	for i := range tasks {
		if tasks[i].ID == taskID {
			task = &tasks[i]
			break
		}
	}
	if task == nil {
		return fmt.Errorf("task %d not found", taskID)
	}
	if task.Status == "running" || task.Status == "queued" || task.Status == "blocked" {
		return fmt.Errorf("task #%d has status %q — only completed, failed, bailed, or stopped tasks can have worktrees deleted", task.IssueNumber, task.Status)
	}
	if task.WorktreePath == "" {
		return fmt.Errorf("task #%d has no worktree to delete", task.IssueNumber)
	}

	// Remove the worktree directory.
	if err := gitpkg.WorktreeRemove(s.repoDir, task.WorktreePath); err != nil {
		// Best-effort: try direct removal if git command fails.
		_ = os.RemoveAll(task.WorktreePath)
	}

	// Optionally delete the branch.
	if deleteBranch && task.Branch != "" {
		_ = gitpkg.DeleteBranch(s.repoDir, task.Branch)
	}

	// Clear worktree path in DB so the task shows as cleaned up.
	if err := s.store.ClearAutopilotTaskWorktree(task.ID); err != nil {
		return fmt.Errorf("clear worktree path: %w", err)
	}

	s.emitEvent("info", fmt.Sprintf("Deleted worktree for #%d", task.IssueNumber), task)
	return nil
}

// Pause stops new agents from being started while letting running ones finish.
func (s *Supervisor) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused {
		return
	}
	s.paused = true
	s.emitEventLocked("warning", "Slot filling paused", nil)
}

// Resume re-enables slot filling and immediately tries to fill idle slots.
func (s *Supervisor) Resume(ctx context.Context) {
	s.mu.Lock()
	if !s.paused {
		s.mu.Unlock()
		return
	}
	s.paused = false
	s.emitEventLocked("warning", "Slot filling resumed", nil)
	s.mu.Unlock()

	s.fillSlots(ctx)
}

// AddSlot appends one idle slot to the supervisor and immediately tries to fill it.
// Returns the new total slot count.
func (s *Supervisor) AddSlot(ctx context.Context) int {
	s.mu.Lock()
	s.slots = append(s.slots, nil)
	n := len(s.slots)
	s.emitEventLocked("info", fmt.Sprintf("Added slot — now %d total", n), nil)
	s.mu.Unlock()

	s.fillSlots(ctx)
	return n
}

// IsPaused returns whether slot filling is currently paused.
func (s *Supervisor) IsPaused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused
}

// Prepare fetches tracked issues, creates autopilot tasks, and builds a dependency graph.
// If existing tasks are found from a previous session, returns PrepareResult with
// HasGraph=true so the TUI can offer keep/rebuild choice. Otherwise builds fresh.
// Optional guidance is passed to the LLM during dependency analysis.
func (s *Supervisor) Prepare(ctx context.Context, guidance string) (*PrepareResult, error) {
	// Validate configured base branch exists in at least one enrolled repo.
	if s.project.AutopilotBaseBranch != "" {
		repos, err := s.store.GetRepos(s.project.ID)
		if err != nil {
			return nil, fmt.Errorf("get enrolled repos: %w", err)
		}
		found := false
		for _, r := range repos {
			if gitpkg.BranchExists(r.Path, s.project.AutopilotBaseBranch) {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("configured base branch %q not found in any enrolled repo", s.project.AutopilotBaseBranch)
		}
	}

	// Detect which agent definition tier will be used.
	agentDef := DetectAgentDef(s.repoDir)

	// Check for existing tasks from a previous session.
	existingTasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return nil, fmt.Errorf("get existing tasks: %w", err)
	}

	if len(existingTasks) > 0 {
		return s.prepareWithExisting(ctx, existingTasks, agentDef)
	}

	// No existing tasks — fresh prepare.
	return s.prepareFresh(ctx, guidance, agentDef)
}

// prepareWithExisting handles the case where tasks exist from a prior session.
// It counts statuses, finds new issues, checks for a stored graph, and returns
// a PrepareResult so the TUI can offer keep/rebuild choice.
func (s *Supervisor) prepareWithExisting(ctx context.Context, existingTasks []db.AutopilotTask, agentDef AgentDefSource) (*PrepareResult, error) {
	// Count existing task statuses.
	var doneCount, manualCount int
	for _, t := range existingTasks {
		switch t.Status {
		case "done":
			doneCount++
		case "manual":
			manualCount++
		}
	}

	// Find new tracked items not already in the task table.
	knownIssues := make(map[int]bool, len(existingTasks))
	for _, t := range existingTasks {
		knownIssues[t.IssueNumber] = true
	}
	items, err := s.store.GetTrackedItems(s.project.ID)
	if err != nil {
		return nil, fmt.Errorf("get tracked items: %w", err)
	}
	var newIssueCount int
	for _, item := range items {
		if item.ItemType == "issue" && item.State == "open" && !knownIssues[item.Number] {
			newIssueCount++
		}
	}

	// Check for stored dep graph.
	storedGraph, err := s.store.GetDepGraph(s.project.ID)
	if err != nil {
		return nil, fmt.Errorf("get stored dep graph: %w", err)
	}

	return &PrepareResult{
		Total:     len(existingTasks),
		Options:   nil,
		AgentDef:  agentDef,
		Existing:  len(existingTasks),
		NewIssues: newIssueCount,
		HasGraph:  storedGraph != nil,
		Done:      doneCount,
		Manual:    manualCount,
	}, nil
}

// prepareFresh runs the full fresh prepare flow: clear old tasks, convert tracked items, build dep graph.
func (s *Supervisor) prepareFresh(ctx context.Context, guidance string, agentDef AgentDefSource) (*PrepareResult, error) {
	// Clean up any leftovers.
	if err := s.store.ClearAutopilotTasks(s.project.ID); err != nil {
		return nil, fmt.Errorf("clear autopilot tasks: %w", err)
	}
	_ = s.store.DeleteDepGraph(s.project.ID)
	s.cleanOrphanedWorktrees()

	// Convert tracked items to autopilot tasks.
	tasks, err := s.convertTrackedItems(ctx)
	if err != nil {
		return nil, fmt.Errorf("convert tracked items: %w", err)
	}
	if len(tasks) == 0 {
		return &PrepareResult{AgentDef: agentDef}, nil
	}

	// Build dependency graph options.
	options, err := s.buildDepOptions(ctx, tasks, guidance)
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Dependency graph failed: %v — falling back to sequential order", err), nil)
		graph := make(map[string]json.RawMessage, len(tasks))
		var prevAgent int
		firstAgent := true
		for _, t := range tasks {
			key := strconv.Itoa(t.IssueNumber)
			if t.Status == "manual" {
				graph[key] = json.RawMessage(`"manual"`)
				continue
			}
			if firstAgent {
				graph[key] = json.RawMessage("[]")
				firstAgent = false
			} else {
				deps, _ := json.Marshal([]int{prevAgent})
				graph[key] = json.RawMessage(deps)
			}
			prevAgent = t.IssueNumber
		}
		options = []DepOption{{
			Name:      "Sequential (fallback)",
			Rationale: "LLM analysis failed; tasks will run one at a time in order.",
			Graph:     graph,
			Unblocked: 1,
		}}
	}

	return &PrepareResult{
		Total:    len(tasks),
		Options:  options,
		AgentDef: agentDef,
	}, nil
}

// ReprepareKeep preserves all existing tasks and their deps, only resetting
// stale running tasks (process is gone after restart). Discovers new tracked
// items and optionally runs incremental dep analysis for them.
func (s *Supervisor) ReprepareKeep(ctx context.Context) (*PrepareResult, error) {
	agentDef := DetectAgentDef(s.repoDir)

	// Only reset running tasks (stale — process is gone). Everything else stays.
	reset, err := s.store.TransitionStaleRunningTasks(s.project.ID)
	if err != nil {
		return nil, fmt.Errorf("reset stale running tasks: %w", err)
	}
	if reset > 0 {
		s.emitEvent("info", fmt.Sprintf("Reset %d stale running tasks to queued", reset), nil)
	}
	s.cleanOrphanedWorktrees()

	// Discover and add new tracked items as tasks.
	added := s.addNewTrackedItems(ctx)

	// Get final task list.
	allTasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return nil, fmt.Errorf("get tasks: %w", err)
	}

	if len(allTasks) == 0 {
		return &PrepareResult{AgentDef: agentDef}, nil
	}

	// If there are new issues, run incremental dep analysis (including reverse deps).
	if added > 0 {
		s.emitEvent("info", fmt.Sprintf("Analyzing dependencies for %d new issue(s)", added), nil)
		options, err := s.BuildIncrementalDepOptions(ctx, allTasks, "")
		if err != nil {
			s.emitEvent("warning", fmt.Sprintf("Incremental dep analysis failed: %v — new tasks queued without deps", err), nil)
		} else if len(options) > 0 {
			// Merge stored graph into each option so the carousel shows the
			// full picture (existing tasks + new ones), not just the delta.
			s.mergeStoredGraphIntoOptions(options, allTasks)
			return &PrepareResult{
				Total:    len(allTasks),
				Options:  options,
				AgentDef: agentDef,
			}, nil
		}
	}

	// No new issues or incremental failed — go straight to confirm.
	// Re-evaluate blocked status (some deps may have been satisfied since last run).
	s.unblockSatisfiedTasks()

	active := 0
	var doneCount, manualCount int
	for _, t := range allTasks {
		if t.Status != "skipped" {
			active++
		}
		if t.Status == "done" {
			doneCount++
		}
		if t.Status == "manual" {
			manualCount++
		}
	}

	return &PrepareResult{
		Total:     active,
		Options:   nil,
		AgentDef:  agentDef,
		Existing:  len(allTasks) - added,
		HasGraph:  true,
		Done:      doneCount,
		Manual:    manualCount,
		NewIssues: added,
	}, nil
}

// ReprepareRebuild clears remaining queued/blocked tasks, rebuilds the full dep graph.
func (s *Supervisor) ReprepareRebuild(ctx context.Context, guidance string) (*PrepareResult, error) {
	agentDef := DetectAgentDef(s.repoDir)

	// Transition tasks: review/failed/bailed/stopped/running → manual; queued/blocked → cleared.
	if err := s.store.TransitionAutopilotTasksForReprepare(s.project.ID); err != nil {
		return nil, fmt.Errorf("transition tasks: %w", err)
	}
	_ = s.store.DeleteDepGraph(s.project.ID)
	s.cleanOrphanedWorktrees()

	// Convert all tracked items (will skip those already in task table via BulkCreateAutopilotTasks INSERT OR IGNORE).
	tasks, err := s.convertTrackedItems(ctx)
	if err != nil {
		return nil, fmt.Errorf("convert tracked items: %w", err)
	}

	// Get all tasks including preserved ones.
	allTasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return nil, fmt.Errorf("get all tasks: %w", err)
	}

	if len(allTasks) == 0 {
		return &PrepareResult{AgentDef: agentDef}, nil
	}

	// Build dep options for all tasks (including preserved done/manual as context).
	var taskPtrs []*db.AutopilotTask
	for i := range allTasks {
		taskPtrs = append(taskPtrs, &allTasks[i])
	}
	// Only build options if there are non-terminal tasks.
	hasWorkable := false
	for _, t := range allTasks {
		if t.Status == "queued" || t.Status == "blocked" || t.Status == "manual" {
			hasWorkable = true
			break
		}
	}
	if !hasWorkable {
		return &PrepareResult{
			Total:    len(allTasks),
			AgentDef: agentDef,
		}, nil
	}

	options, err := s.buildDepOptions(ctx, taskPtrs, guidance)
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Dependency graph failed: %v — falling back to sequential order", err), nil)
		graph := make(map[string]json.RawMessage, len(taskPtrs))
		var prevAgent int
		firstAgent := true
		for _, t := range taskPtrs {
			key := strconv.Itoa(t.IssueNumber)
			if t.Status == "manual" || t.Status == "done" || t.Status == "skipped" {
				if t.Status == "manual" {
					graph[key] = json.RawMessage(`"manual"`)
				}
				continue
			}
			if firstAgent {
				graph[key] = json.RawMessage("[]")
				firstAgent = false
			} else {
				deps, _ := json.Marshal([]int{prevAgent})
				graph[key] = json.RawMessage(deps)
			}
			prevAgent = t.IssueNumber
		}
		options = []DepOption{{
			Name:      "Sequential (fallback)",
			Rationale: "LLM analysis failed; tasks will run one at a time in order.",
			Graph:     graph,
			Unblocked: 1,
		}}
	}

	_ = tasks // suppress unused warning — tasks were created via convertTrackedItems

	return &PrepareResult{
		Total:    len(allTasks),
		Options:  options,
		AgentDef: agentDef,
	}, nil
}

// mergeStoredGraphIntoOptions enriches incremental dep options with stored graph
// entries so the dep-select carousel shows the full picture (existing + new tasks).
func (s *Supervisor) mergeStoredGraphIntoOptions(options []DepOption, allTasks []db.AutopilotTask) {
	storedGraph, _ := s.store.GetDepGraph(s.project.ID)

	// Build a base graph from stored graph + current task statuses.
	baseGraph := make(map[string]json.RawMessage)

	// Start with stored graph entries.
	if storedGraph != nil {
		var stored map[string]json.RawMessage
		if json.Unmarshal([]byte(storedGraph.GraphJSON), &stored) == nil {
			for k, v := range stored {
				baseGraph[k] = v
			}
		}
	}

	// Fill in any tasks not covered by the stored graph (e.g., tasks that existed
	// before graph persistence was added) using their current DB deps.
	for _, t := range allTasks {
		key := strconv.Itoa(t.IssueNumber)
		if _, exists := baseGraph[key]; exists {
			continue
		}
		switch t.Status {
		case "manual":
			baseGraph[key] = json.RawMessage(`"manual"`)
		case "skipped":
			baseGraph[key] = json.RawMessage(`"skip"`)
		default:
			baseGraph[key] = json.RawMessage(t.Dependencies)
		}
	}

	// Merge base graph into each option (option entries for new tasks take priority).
	for i := range options {
		merged := make(map[string]json.RawMessage, len(baseGraph)+len(options[i].Graph))
		for k, v := range baseGraph {
			merged[k] = v
		}
		for k, v := range options[i].Graph {
			merged[k] = v // new task entries override
		}
		options[i].Graph = merged
		// Recount unblocked with the full graph.
		var taskPtrs []*db.AutopilotTask
		for j := range allTasks {
			taskPtrs = append(taskPtrs, &allTasks[j])
		}
		options[i].Unblocked = countUnblocked(merged, taskPtrs)
	}
}

// BuildIncrementalDepOptions sends only new issues to LLM for dependency analysis,
// including existing tasks as context. Returns options that cover only the new issues.
func (s *Supervisor) BuildIncrementalDepOptions(ctx context.Context, allTasks []db.AutopilotTask, guidance string) ([]DepOption, error) {
	// Identify new tasks (queued with no deps, added by discoverNewTasks).
	var newTasks []*db.AutopilotTask
	var existingTasks []*db.AutopilotTask
	for i := range allTasks {
		t := &allTasks[i]
		if t.Status == "queued" && t.Dependencies == "[]" {
			newTasks = append(newTasks, t)
		} else {
			existingTasks = append(existingTasks, t)
		}
	}

	if len(newTasks) == 0 {
		return nil, nil
	}

	// Single new task — trivial option.
	if len(newTasks) == 1 && len(existingTasks) == 0 {
		graph := make(map[string]json.RawMessage, 1)
		graph[strconv.Itoa(newTasks[0].IssueNumber)] = json.RawMessage("[]")
		return []DepOption{{
			Name:      "No dependencies",
			Rationale: "Only one new task — no dependencies possible.",
			Graph:     graph,
			Unblocked: 1,
		}}, nil
	}

	// Build prompt with new issues as the focus and existing as context.
	var issueList strings.Builder
	issueList.WriteString("## NEW issues to analyze dependencies for:\n")
	for _, t := range newTasks {
		fmt.Fprintf(&issueList, "Issue #%d: %s\n", t.IssueNumber, t.IssueTitle)
		if t.IssueBody != "" {
			body := t.IssueBody
			if len(body) > 200 {
				body = body[:200] + "..."
			}
			fmt.Fprintf(&issueList, "  %s\n", body)
		}
	}

	// Separate existing tasks by mutability for the prompt.
	var mutableTasks, immutableTasks []*db.AutopilotTask
	for _, t := range existingTasks {
		switch t.Status {
		case "queued", "blocked":
			mutableTasks = append(mutableTasks, t)
		default:
			immutableTasks = append(immutableTasks, t)
		}
	}

	if len(mutableTasks) > 0 {
		issueList.WriteString("\n## Existing queued/blocked tasks (deps may be updated if a new issue should block them):\n")
		for _, t := range mutableTasks {
			fmt.Fprintf(&issueList, "Issue #%d [%s]: %s (current deps: %s)\n", t.IssueNumber, t.Status, t.IssueTitle, t.Dependencies)
		}
	}
	if len(immutableTasks) > 0 {
		issueList.WriteString("\n## Existing tasks (context only — deps cannot be changed):\n")
		for _, t := range immutableTasks {
			fmt.Fprintf(&issueList, "Issue #%d [%s]: %s (deps: %s)\n", t.IssueNumber, t.Status, t.IssueTitle, t.Dependencies)
		}
	}

	var guidanceSection string
	if strings.TrimSpace(guidance) != "" {
		guidanceSection = fmt.Sprintf("\nUser guidance:\n  %q\n", guidance)
	}

	prompt := fmt.Sprintf(`Analyze these NEW GitHub issues and determine their dependencies.
Also check if any existing queued/blocked tasks should now depend on a new issue (reverse dependencies).

Rules:
- New issues can depend on existing tasks or other new issues.
- Existing queued/blocked tasks can gain new dependencies on new issues. If so, include that task's FULL updated dependency array (not just the new dep).
- Do NOT include entries for existing running/review/done/manual tasks — their deps are locked.

%s%s
Respond with ONLY a JSON array of exactly 3 dependency graph options. Each has "name", "rationale", and "graph".
The "graph" MUST contain entries for all NEW issues. It MAY also contain entries for existing queued/blocked tasks whose deps changed.
Values are arrays of integer issue numbers (deps) or "skip"/"manual".

Example (issue #50 and #51 are new; existing #42 now depends on new #50):
[
  {"name": "Conservative", "rationale": "...", "graph": {"50": [42], "51": [], "42": [99, 50]}},
  {"name": "Parallel", "rationale": "...", "graph": {"50": [], "51": []}},
  {"name": "Sequential", "rationale": "...", "graph": {"50": [], "51": [50]}}
]`, issueList.String(), guidanceSection)

	depModel := s.project.LLMAnalyzerModel
	if depModel == "" {
		depModel = "opus"
	}

	var options []DepOption
	for attempt := 0; attempt < 2; attempt++ {
		promptText := prompt
		if attempt > 0 {
			promptText = prompt + "\n\nYour previous response was not valid JSON. Please respond with ONLY the JSON array."
		}

		resp, err := s.completer.Complete(ctx, &claudecli.Request{
			SystemPrompt: "You are analyzing issue dependencies. Respond with ONLY valid JSON, no explanation.",
			Prompt:       promptText,
			Model:        depModel,
			DisableTools: true,
		})
		if err != nil {
			return nil, fmt.Errorf("LLM call: %w", err)
		}

		content := strings.TrimSpace(resp.Content())
		if strings.HasPrefix(content, "```") {
			lines := strings.Split(content, "\n")
			if len(lines) > 2 {
				content = strings.Join(lines[1:len(lines)-1], "\n")
			}
		}
		if start := strings.Index(content, "["); start >= 0 {
			if end := strings.LastIndex(content, "]"); end > start {
				content = content[start : end+1]
			}
		}

		var rawOptions []struct {
			Name      string                     `json:"name"`
			Rationale string                     `json:"rationale"`
			Graph     map[string]json.RawMessage `json:"graph"`
		}
		if err := json.Unmarshal([]byte(content), &rawOptions); err != nil {
			if attempt == 0 {
				continue
			}
			return nil, fmt.Errorf("parse incremental dep options: %w", err)
		}

		for _, ro := range rawOptions {
			opt := DepOption{
				Name:      ro.Name,
				Rationale: ro.Rationale,
				Graph:     ro.Graph,
				Unblocked: countUnblocked(ro.Graph, newTasks),
			}
			options = append(options, opt)
		}
		break
	}

	if len(options) == 0 {
		return nil, fmt.Errorf("LLM returned no options")
	}

	return options, nil
}

// ApplyIncrementalDepOption applies deps for new and updated issues, merges into
// the stored graph, and re-evaluates blocked status. Existing tasks in non-mutable
// states (running/review/done/bailed/stopped/failed) are never modified.
func (s *Supervisor) ApplyIncrementalDepOption(ctx context.Context, opt DepOption) error {
	tasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return fmt.Errorf("get tasks: %w", err)
	}

	var newCount, updatedCount int

	for _, t := range tasks {
		key := strconv.Itoa(t.IssueNumber)
		rawDeps, ok := opt.Graph[key]
		if !ok {
			continue
		}

		// Never modify deps for tasks in non-mutable states.
		switch t.Status {
		case "running", "review", "done", "bailed", "stopped", "failed":
			continue
		}

		isNew := t.Dependencies == "[]" && t.Status == "queued"

		// Handle string directives: "skip" or "manual".
		var strVal string
		if json.Unmarshal(rawDeps, &strVal) == nil {
			switch strVal {
			case "skip":
				_ = s.store.UpdateAutopilotTaskStatus(t.ID, "skipped")
				if isNew {
					s.emitEvent("graph-update", fmt.Sprintf("#%d marked as skip", t.IssueNumber), nil)
				}
			case "manual":
				_ = s.store.UpdateAutopilotTaskStatus(t.ID, "manual")
				if isNew {
					s.emitEvent("graph-update", fmt.Sprintf("#%d marked as manual", t.IssueNumber), nil)
				}
			}
			continue
		}

		// Parse dependency array.
		var deps []int
		if err := json.Unmarshal(rawDeps, &deps); err != nil {
			var strDeps []string
			if json.Unmarshal(rawDeps, &strDeps) == nil {
				for _, sd := range strDeps {
					if n, err2 := strconv.Atoi(sd); err2 == nil {
						deps = append(deps, n)
					}
				}
			}
		}

		depsJSON, _ := json.Marshal(deps)
		newDepsStr := string(depsJSON)

		if isNew {
			newCount++
			if len(deps) > 0 {
				_ = s.store.UpdateAutopilotTaskDeps(t.ID, newDepsStr)
				_ = s.store.UpdateAutopilotTaskStatus(t.ID, "blocked")
				s.emitEvent("graph-update", fmt.Sprintf("#%d added with deps %s", t.IssueNumber, newDepsStr), nil)
			} else {
				s.emitEvent("graph-update", fmt.Sprintf("#%d added with no deps (queued)", t.IssueNumber), nil)
			}
		} else if newDepsStr != t.Dependencies {
			// Existing queued/blocked task with updated deps (reverse dependency injection).
			updatedCount++
			_ = s.store.UpdateAutopilotTaskDeps(t.ID, newDepsStr)
			if len(deps) > 0 {
				_ = s.store.UpdateAutopilotTaskStatus(t.ID, "blocked")
			}
			s.emitEvent("graph-update", fmt.Sprintf("#%d deps updated: %s -> %s", t.IssueNumber, t.Dependencies, newDepsStr), nil)
		}
	}

	// Summary event.
	if newCount > 0 || updatedCount > 0 {
		parts := make([]string, 0, 2)
		if newCount > 0 {
			parts = append(parts, fmt.Sprintf("%d new", newCount))
		}
		if updatedCount > 0 {
			parts = append(parts, fmt.Sprintf("%d updated", updatedCount))
		}
		s.emitEvent("info", fmt.Sprintf("Dep graph applied: %s tasks", strings.Join(parts, ", ")), nil)
	}

	// Merge new entries into stored graph.
	storedGraph, _ := s.store.GetDepGraph(s.project.ID)
	if storedGraph != nil {
		var existingGraph map[string]json.RawMessage
		if json.Unmarshal([]byte(storedGraph.GraphJSON), &existingGraph) == nil {
			for k, v := range opt.Graph {
				existingGraph[k] = v
			}
			mergedJSON, _ := json.Marshal(existingGraph)
			_ = s.store.SaveDepGraph(s.project.ID, string(mergedJSON), storedGraph.OptionName+" + incremental")
		}
	} else {
		graphJSON, _ := json.Marshal(opt.Graph)
		_ = s.store.SaveDepGraph(s.project.ID, string(graphJSON), opt.Name)
	}

	// Re-evaluate blocked status — some tasks may now be unblocked.
	s.unblockSatisfiedTasks()

	s.mu.Lock()
	s.preparedAt = time.Now()
	s.mu.Unlock()

	return nil
}

// ApplyDepOption applies the selected dependency option to the DB tasks.
// Call this after the user picks an option from the dep-select screen.
func (s *Supervisor) ApplyDepOption(ctx context.Context, opt DepOption) error {
	tasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return fmt.Errorf("get tasks: %w", err)
	}

	for _, t := range tasks {
		key := strconv.Itoa(t.IssueNumber)
		rawDeps, ok := opt.Graph[key]
		if !ok {
			continue
		}

		// Check for string directives: "skip" or "manual".
		var strVal string
		if json.Unmarshal(rawDeps, &strVal) == nil {
			switch strVal {
			case "skip":
				_ = s.store.UpdateAutopilotTaskStatus(t.ID, "skipped")
				s.emitEvent("warning", fmt.Sprintf("Task #%d excluded from autopilot", t.IssueNumber), &t)
			case "manual":
				_ = s.store.UpdateAutopilotTaskStatus(t.ID, "manual")
				s.emitEvent("warning", fmt.Sprintf("Task #%d is manual (non-AI) — watching for completion", t.IssueNumber), &t)
			}
			continue
		}

		// Parse deps array.
		var deps []int
		if err := json.Unmarshal(rawDeps, &deps); err != nil {
			deps = nil // reset: failed unmarshal may have partially initialized the slice
			var strDeps []string
			if err2 := json.Unmarshal(rawDeps, &strDeps); err2 != nil {
				continue
			}
			for _, sd := range strDeps {
				if n, err3 := strconv.Atoi(sd); err3 == nil {
					deps = append(deps, n)
				}
			}
		}

		if len(deps) > 0 {
			depsJSON, _ := json.Marshal(deps)
			if err := s.store.UpdateAutopilotTaskDeps(t.ID, string(depsJSON)); err != nil {
				return fmt.Errorf("update deps for #%d: %w", t.IssueNumber, err)
			}
			// Block tasks that have unsatisfied deps.
			_ = s.store.UpdateAutopilotTaskStatus(t.ID, "blocked")
		}
	}

	// Re-evaluate: unblock tasks whose deps are all satisfied.
	freshTasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return fmt.Errorf("refresh tasks: %w", err)
	}
	statusMap := make(map[int]string, len(freshTasks))
	for _, t := range freshTasks {
		statusMap[t.IssueNumber] = t.Status
	}
	trackedItems, _ := s.store.GetTrackedItems(s.project.ID)
	trackedState := make(map[int]string, len(trackedItems))
	for _, item := range trackedItems {
		trackedState[item.Number] = item.State
	}
	for _, t := range freshTasks {
		if t.Status != "blocked" {
			continue
		}
		deps := parseDeps(t.Dependencies)
		allSatisfied := true
		for _, dep := range deps {
			if taskStatus, ok := statusMap[dep]; ok {
				if taskStatus != "done" {
					allSatisfied = false
					break
				}
			} else if state, ok := trackedState[dep]; ok {
				if state == "open" {
					allSatisfied = false
					break
				}
			}
		}
		if allSatisfied {
			_ = s.store.UpdateAutopilotTaskStatus(t.ID, "queued")
		}
	}

	// Store the selected graph for persistence across restarts.
	graphJSON, _ := json.Marshal(opt.Graph)
	_ = s.store.SaveDepGraph(s.project.ID, string(graphJSON), opt.Name)

	s.mu.Lock()
	s.preparedAt = time.Now()
	s.mu.Unlock()

	return nil
}

// cleanOrphanedWorktrees removes worktrees left behind by interrupted agents.
// Returns the number of worktrees cleaned up.
func (s *Supervisor) cleanOrphanedWorktrees() int {
	worktreeBase := filepath.Join(os.Getenv("HOME"), ".agent-minder", "worktrees", s.project.Name)
	entries, err := os.ReadDir(worktreeBase)
	if err != nil {
		return 0 // directory may not exist
	}
	cleaned := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(worktreeBase, e.Name())
		if err := gitpkg.WorktreeRemove(s.repoDir, path); err != nil {
			// Best-effort: try direct removal if git worktree remove fails.
			_ = os.RemoveAll(path)
		}
		cleaned++
	}
	if cleaned > 0 {
		s.emitEvent("info", fmt.Sprintf("Cleaning up %d retained worktrees from previous session", cleaned), nil)
	}
	return cleaned
}

// Launch starts filling slots with agents. Call after Prepare + user confirmation.
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

		// Main loop: wait for agents, check review PRs for merges,
		// discover new tracked items, fill new slots.
		reviewCheckTicker := time.NewTicker(30 * time.Second)
		defer reviewCheckTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.mu.Lock()
				s.active = false
				s.mu.Unlock()
				s.emitEvent("finished", "Autopilot cancelled", nil)
				return
			case <-reviewCheckTicker.C:
				s.checkReviewTasks(ctx)
				s.checkManualTasks(ctx)
				unblocked := s.unblockSatisfiedTasks()
				if unblocked > 0 {
					s.fillSlots(ctx)
				}
			default:
				// Refill slots on every iteration — handles externally re-queued
				// tasks (e.g. restart from TUI viewer) that no other event covers.
				if s.hasIdleSlot() {
					s.fillSlots(ctx)
				}
			}

			s.mu.Lock()
			anyRunning := false
			for _, slot := range s.slots {
				if slot != nil {
					anyRunning = true
					break
				}
			}
			s.mu.Unlock()

			// Check if there's any work left (running agents, queued tasks, tasks in review, or manual tasks being watched).
			// Critical: paused + queued tasks must keep the loop alive — otherwise the
			// supervisor exits when running agents finish during pause, losing queued work.
			// Manual tasks keep the loop alive because dependents may unblock when they close.
			hasWork := anyRunning
			if !hasWork {
				tasks, _ := s.store.GetAutopilotTasks(s.project.ID)
				for _, t := range tasks {
					if t.Status == "queued" || t.Status == "review" || t.Status == "reviewing" || t.Status == "reviewed" || t.Status == "manual" || t.Status == "blocked" {
						hasWork = true
						break
					}
				}
			}
			if !hasWork {
				s.mu.Lock()
				isPaused := s.paused
				s.mu.Unlock()
				if isPaused {
					hasWork = true
				}
			}

			if !hasWork {
				break
			}
			time.Sleep(2 * time.Second)
		}

		s.mu.Lock()
		s.active = false
		s.mu.Unlock()
		s.emitEvent("finished", "All agents finished", nil)
	}()
}

// Stop cancels all agent processes and cleans up.
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

	// Wait for done signal.
	if s.done != nil {
		<-s.done
	}
}

// SlotStatus returns the current state of all agent slots.
func (s *Supervisor) SlotStatus() []SlotInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	infos := make([]SlotInfo, len(s.slots))
	for i, slot := range s.slots {
		if slot == nil {
			infos[i] = SlotInfo{
				SlotNum: i + 1,
				Status:  "idle",
				Paused:  s.paused,
			}
		} else {
			infos[i] = SlotInfo{
				SlotNum:     i + 1,
				IssueNumber: slot.task.IssueNumber,
				IssueTitle:  slot.task.IssueTitle,
				Branch:      slot.task.Branch,
				RunningFor:  time.Since(slot.startedAt),
				Status:      "running",
				IsReview:    slot.task.Status == "reviewing",
				CurrentTool: slot.liveStatus.CurrentTool,
				ToolInput:   slot.liveStatus.ToolInput,
				StepCount:   slot.liveStatus.StepCount,
			}
		}
	}
	return infos
}

// StatusBlock returns a formatted text block for injection into the tier 2 analyzer prompt.
func (s *Supervisor) StatusBlock() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.active {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Autopilot Status\n")

	for i, slot := range s.slots {
		if slot == nil {
			fmt.Fprintf(&b, "- Slot %d: idle\n", i+1)
		} else {
			elapsed := time.Since(slot.startedAt).Round(time.Second)
			toolInfo := ""
			if slot.liveStatus.CurrentTool != "" {
				toolInfo = fmt.Sprintf(", using %s", slot.liveStatus.CurrentTool)
			}
			fmt.Fprintf(&b, "- Slot %d: #%d %s (%s, running %s, step %d%s)\n",
				i+1, slot.task.IssueNumber, slot.task.IssueTitle, slot.task.Branch,
				elapsed, slot.liveStatus.StepCount, toolInfo)
		}
	}

	// Summary counts.
	tasks, _ := s.store.GetAutopilotTasks(s.project.ID)
	var queued, running, review, reviewing, reviewed, done, bailed, stopped, manual int
	for _, t := range tasks {
		switch t.Status {
		case "queued":
			queued++
		case "running":
			running++
		case "review":
			review++
		case "reviewing":
			reviewing++
		case "reviewed":
			reviewed++
		case "done":
			done++
		case "bailed":
			bailed++
		case "stopped":
			stopped++
		case "manual":
			manual++
		}
	}
	summary := fmt.Sprintf("\nTask summary: %d queued, %d running, %d in review, %d reviewing, %d reviewed, %d done, %d bailed, %d stopped", queued, running, review, reviewing, reviewed, done, bailed, stopped)
	if manual > 0 {
		summary += fmt.Sprintf(", %d manual (watching)", manual)
	}
	fmt.Fprintf(&b, "%s\n", summary)

	return b.String()
}

// DepGraph returns a compact dependency graph for injection into the tier 2 analyzer prompt.
// Format: adjacency list with statuses and a generation timestamp.
func (s *Supervisor) DepGraph() string {
	s.mu.Lock()
	preparedAt := s.preparedAt
	active := s.active
	s.mu.Unlock()

	if !active || preparedAt.IsZero() {
		return ""
	}

	tasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil || len(tasks) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Autopilot Dependency Graph\n")
	fmt.Fprintf(&b, "Generated: %s ago\n", time.Since(preparedAt).Round(time.Second))

	// Summary counts.
	var dQueued, dRunning, dReview, dDone, dBailed, dManual int
	for _, t := range tasks {
		switch t.Status {
		case "queued":
			dQueued++
		case "running":
			dRunning++
		case "review":
			dReview++
		case "done":
			dDone++
		case "bailed":
			dBailed++
		case "manual":
			dManual++
		}
	}
	depSummary := fmt.Sprintf("Tasks: %d queued, %d running, %d review, %d done, %d bailed", dQueued, dRunning, dReview, dDone, dBailed)
	if dManual > 0 {
		depSummary += fmt.Sprintf(", %d manual", dManual)
	}
	fmt.Fprintf(&b, "%s\n", depSummary)

	// Compact adjacency list: #N (status) → [deps]
	for _, t := range tasks {
		if t.Status == "done" || t.Status == "skipped" {
			continue
		}
		var deps []int
		if t.Dependencies != "" && t.Dependencies != "[]" {
			_ = json.Unmarshal([]byte(t.Dependencies), &deps)
		}
		if len(deps) > 0 {
			depStrs := make([]string, len(deps))
			for i, d := range deps {
				depStrs[i] = fmt.Sprintf("#%d", d)
			}
			fmt.Fprintf(&b, "- #%d (%s) → waits on %s\n", t.IssueNumber, t.Status, strings.Join(depStrs, ", "))
		} else {
			fmt.Fprintf(&b, "- #%d (%s)\n", t.IssueNumber, t.Status)
		}
	}

	return b.String()
}

// convertTrackedItems reads the user's already-tracked issues and creates autopilot tasks.
// Issues with the skip label are included as "manual" tasks — they won't get agent slots
// but can block automated tasks and are watched for completion.
func (s *Supervisor) convertTrackedItems(ctx context.Context) ([]*db.AutopilotTask, error) {
	items, err := s.store.GetTrackedItems(s.project.ID)
	if err != nil {
		return nil, fmt.Errorf("get tracked items: %w", err)
	}

	matcher := newSkipMatcher(s.project.AutopilotSkipLabel)
	ghClient := s.newGHClient()

	var tasks []*db.AutopilotTask
	for _, item := range items {
		// Only process open issues.
		if item.ItemType != "issue" || item.State != "open" {
			continue
		}

		// Fetch live status from GitHub (cached labels may be stale).
		liveStatus, err := ghClient.FetchItem(ctx, item.Owner, item.Repo, item.Number)
		if err != nil {
			continue
		}
		if liveStatus.State != "open" {
			continue
		}

		// Issues with the skip label, or already being worked on externally
		// (in-progress, needs-review, blocked), become manual tasks.
		// They won't get agent slots but appear in the dep graph and are
		// watched for completion so dependents auto-unblock.
		isManual := matcher.matches(liveStatus.Labels) ||
			hasLabel(liveStatus.Labels, "in-progress") ||
			hasLabel(liveStatus.Labels, "needs-review") ||
			hasLabel(liveStatus.Labels, "blocked")
		if isManual {
			body := ""
			content, err := ghClient.FetchItemContent(ctx, item.Owner, item.Repo, item.Number, "issue")
			if err == nil {
				body = content.Body
			}
			tasks = append(tasks, &db.AutopilotTask{
				ProjectID:    s.project.ID,
				Owner:        item.Owner,
				Repo:         item.Repo,
				IssueNumber:  item.Number,
				IssueTitle:   liveStatus.Title,
				IssueBody:    body,
				Dependencies: "[]",
				Status:       "manual",
			})
			continue
		}

		// Fetch issue body.
		body := ""
		content, err := ghClient.FetchItemContent(ctx, item.Owner, item.Repo, item.Number, "issue")
		if err == nil {
			body = content.Body
		}

		tasks = append(tasks, &db.AutopilotTask{
			ProjectID:    s.project.ID,
			Owner:        item.Owner,
			Repo:         item.Repo,
			IssueNumber:  item.Number,
			IssueTitle:   liveStatus.Title,
			IssueBody:    body,
			Dependencies: "[]",
			Status:       "queued",
		})
	}

	if len(tasks) == 0 {
		return nil, nil
	}

	_, err = s.store.BulkCreateAutopilotTasks(tasks)
	if err != nil {
		return nil, fmt.Errorf("store tasks: %w", err)
	}

	return tasks, nil
}

func hasLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// skipMatcher checks whether a label set matches the configured skip pattern.
// The pattern supports:
//   - Single label: "no-agent"
//   - Comma-separated: "no-agent, manual, human-only"
//   - Regex (future): "/no-agent|manual-.*/"
//
// Returns the first matching label (useful for logging) or "" if no match.
type skipMatcher struct {
	labels []string
}

func newSkipMatcher(pattern string) skipMatcher {
	if pattern == "" {
		return skipMatcher{labels: []string{"no-agent"}}
	}
	var labels []string
	for _, part := range strings.Split(pattern, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			labels = append(labels, part)
		}
	}
	if len(labels) == 0 {
		labels = []string{"no-agent"}
	}
	return skipMatcher{labels: labels}
}

// matches returns true if any label in the set matches a skip label.
func (sm skipMatcher) matches(labels []string) bool {
	for _, l := range labels {
		for _, skip := range sm.labels {
			if l == skip {
				return true
			}
		}
	}
	return false
}

func (s *Supervisor) buildDepOptions(ctx context.Context, tasks []*db.AutopilotTask, guidance string) ([]DepOption, error) {
	// Separate agent tasks from manual tasks.
	var agentTasks, manualTasks []*db.AutopilotTask
	for _, t := range tasks {
		if t.Status == "manual" {
			manualTasks = append(manualTasks, t)
		} else {
			agentTasks = append(agentTasks, t)
		}
	}

	if len(agentTasks) == 0 {
		// Only manual tasks — nothing for agents to do.
		// Still build a graph with manual entries so they're visible.
		graph := make(map[string]json.RawMessage, len(manualTasks))
		for _, t := range manualTasks {
			graph[strconv.Itoa(t.IssueNumber)] = json.RawMessage(`"manual"`)
		}
		return []DepOption{{
			Name:      "No agent tasks",
			Rationale: "Only manual (non-AI) tasks — watching for completion.",
			Graph:     graph,
			Unblocked: 0,
		}}, nil
	}

	if len(agentTasks) <= 1 && len(manualTasks) == 0 {
		// Single agent task, no manual tasks: return trivial option.
		graph := make(map[string]json.RawMessage, 1)
		graph[strconv.Itoa(agentTasks[0].IssueNumber)] = json.RawMessage("[]")
		return []DepOption{{
			Name:      "No dependencies",
			Rationale: "Only one task — no dependencies possible.",
			Graph:     graph,
			Unblocked: 1,
		}}, nil
	}

	// Build task issue numbers set for quick lookup.
	taskIssues := make(map[int]bool, len(tasks))
	for _, t := range tasks {
		taskIssues[t.IssueNumber] = true
	}

	// Build issue list for the LLM — include tasks being worked on.
	var issueList strings.Builder
	issueList.WriteString("## Issues to be worked on by agents:\n")
	for _, t := range agentTasks {
		fmt.Fprintf(&issueList, "Issue #%d: %s\n", t.IssueNumber, t.IssueTitle)
		if t.IssueBody != "" {
			body := t.IssueBody
			if len(body) > 200 {
				body = body[:200] + "..."
			}
			fmt.Fprintf(&issueList, "  %s\n", body)
		}
	}

	// Include manual (non-AI) tasks — these are human-driven but can block agent tasks.
	if len(manualTasks) > 0 {
		issueList.WriteString("\n## Manual tasks (non-AI, human-driven — can block agent tasks, will be watched for completion):\n")
		for _, t := range manualTasks {
			fmt.Fprintf(&issueList, "Issue #%d: %s\n", t.IssueNumber, t.IssueTitle)
			if t.IssueBody != "" {
				body := t.IssueBody
				if len(body) > 200 {
					body = body[:200] + "..."
				}
				fmt.Fprintf(&issueList, "  %s\n", body)
			}
		}
	}

	// Include other tracked issues as context (closed, etc.)
	// so the LLM can identify external dependencies.
	trackedItems, _ := s.store.GetTrackedItems(s.project.ID)
	var contextList strings.Builder
	for _, item := range trackedItems {
		if taskIssues[item.Number] || item.ItemType != "issue" {
			continue
		}
		fmt.Fprintf(&contextList, "Issue #%d [%s]: %s\n", item.Number, item.State, item.Title)
	}
	if contextList.Len() > 0 {
		issueList.WriteString("\n## Other tracked issues (not being worked on by agents, but may be dependencies):\n")
		issueList.WriteString(contextList.String())
	}

	var guidanceSection string
	if strings.TrimSpace(guidance) != "" {
		guidanceSection = fmt.Sprintf("\nUser guidance on dependencies:\n  %q\n\nConsider this feedback when analyzing. If the user asks to skip or exclude an issue, set its value to the string \"skip\".\n", guidance)
	}

	prompt := fmt.Sprintf(`Analyze these GitHub issues and determine dependencies between them.
A dependency means issue B cannot start until issue A is completed (e.g., B builds on A's infrastructure or schema changes).

Dependencies can include issues from ALL sections — an agent task can depend on a manual task or other tracked issue if that issue's work must be done first.

%s%s
Respond with ONLY a JSON array of exactly 3 dependency graph options, ranked from most likely correct to least likely. Each option has a "name", "rationale", and "graph" field.

The "graph" is an object where:
- Keys for AGENT tasks: values are arrays of integer issue numbers that must complete first. Issues with no dependencies get an empty array.
- Keys for MANUAL tasks: value MUST be the string "manual" — these are human-driven tasks that agents may depend on.
- If the user asks to skip or exclude an issue, set its value to the string "skip" instead of an array.

IMPORTANT: Use integer values in dependency arrays, not strings. Agent tasks CAN depend on manual task issue numbers.

The 3 options should represent meaningfully different dependency strategies:
- Option 1: Your best analysis of actual dependencies
- Option 2: An alternative interpretation (e.g., more conservative or more parallel)
- Option 3: A different trade-off (e.g., maximum parallelism or stricter ordering)

Example response (where #99 is a manual task):
[
  {"name": "Conservative — minimal deps", "rationale": "API work must wait for manual schema migration.", "graph": {"99": "manual", "42": [99], "38": [42], "15": []}},
  {"name": "Moderate — logical grouping", "rationale": "Groups related features; UI issues wait on API changes.", "graph": {"99": "manual", "42": [99], "38": [42], "15": [42]}},
  {"name": "Maximum parallelism", "rationale": "All agent issues can start independently.", "graph": {"99": "manual", "42": [], "38": [], "15": []}}
]`, issueList.String(), guidanceSection)

	depModel := s.project.LLMAnalyzerModel
	if depModel == "" {
		depModel = "opus"
	}

	var options []DepOption

	for attempt := 0; attempt < 2; attempt++ {
		promptText := prompt
		if attempt > 0 {
			// Retry with error feedback appended.
			promptText = prompt + "\n\nYour previous response was not valid JSON. Please respond with ONLY the JSON array of 3 options, no text before or after."
		}

		resp, err := s.completer.Complete(ctx, &claudecli.Request{
			SystemPrompt: "You are analyzing issue dependencies. Respond with ONLY valid JSON, no explanation or preamble.",
			Prompt:       promptText,
			Model:        depModel,
			DisableTools: true,
		})
		if err != nil {
			return nil, fmt.Errorf("LLM call: %w", err)
		}

		content := strings.TrimSpace(resp.Content())
		// Strip markdown fencing if present.
		if strings.HasPrefix(content, "```") {
			lines := strings.Split(content, "\n")
			if len(lines) > 2 {
				content = strings.Join(lines[1:len(lines)-1], "\n")
			}
		}
		// Extract JSON array — trim any leading/trailing non-JSON text.
		if start := strings.Index(content, "["); start >= 0 {
			if end := strings.LastIndex(content, "]"); end > start {
				content = content[start : end+1]
			}
		}

		var rawOptions []struct {
			Name      string                     `json:"name"`
			Rationale string                     `json:"rationale"`
			Graph     map[string]json.RawMessage `json:"graph"`
		}

		if err := json.Unmarshal([]byte(content), &rawOptions); err != nil {
			if attempt == 0 {
				continue
			}
			return nil, fmt.Errorf("parse dep options: %w", err)
		}

		for _, ro := range rawOptions {
			opt := DepOption{
				Name:      ro.Name,
				Rationale: ro.Rationale,
				Graph:     ro.Graph,
				Unblocked: countUnblocked(ro.Graph, tasks),
			}
			options = append(options, opt)
		}
		break
	}

	if len(options) == 0 {
		return nil, fmt.Errorf("LLM returned no options")
	}

	return options, nil
}

// countUnblocked counts how many agent tasks have no unsatisfied dependencies in a graph.
// Manual and skipped tasks are excluded from the count.
func countUnblocked(graph map[string]json.RawMessage, tasks []*db.AutopilotTask) int {
	count := 0
	for _, t := range tasks {
		// Manual tasks are not agent tasks — don't count them.
		if t.Status == "manual" {
			continue
		}
		key := strconv.Itoa(t.IssueNumber)
		rawDeps, ok := graph[key]
		if !ok {
			count++ // not in graph = no deps
			continue
		}
		// Check for "skip" or "manual".
		var strVal string
		if json.Unmarshal(rawDeps, &strVal) == nil && (strVal == "skip" || strVal == "manual") {
			continue
		}
		var deps []int
		if err := json.Unmarshal(rawDeps, &deps); err != nil {
			// Try string array.
			var strDeps []string
			if json.Unmarshal(rawDeps, &strDeps) == nil {
				for _, sd := range strDeps {
					if n, err2 := strconv.Atoi(sd); err2 == nil {
						deps = append(deps, n)
					}
				}
			}
		}
		if len(deps) == 0 {
			count++
		}
	}
	return count
}

func (s *Supervisor) fillSlots(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.paused {
		return
	}

	// Don't launch new agents if the context is cancelled (e.g. Stop() was called).
	if ctx.Err() != nil {
		return
	}

	// Single fetch for all agents about to launch — avoids concurrent fetches
	// racing on ref locks when multiple slots fill at once.
	hasEmpty := false
	for _, slot := range s.slots {
		if slot == nil {
			hasEmpty = true
			break
		}
	}
	if hasEmpty {
		if err := gitpkg.Fetch(s.repoDir); err != nil {
			s.emitEvent("warning", fmt.Sprintf("Fetch failed: %v (using cached refs)", err), nil)
		}
	}

	for i, slot := range s.slots {
		if slot != nil {
			continue // Slot occupied.
		}

		// Find next unblocked task.
		tasks, err := s.store.QueuedUnblockedTasks(s.project.ID)
		if err != nil || len(tasks) == 0 {
			break
		}

		task := tasks[0]
		s.launchAgent(ctx, i, &task)
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

// addNewTrackedItems checks tracked items for new open issues not already in the
// autopilot task list and adds them with queued status. Called during ReprepareKeep
// so that incremental dep analysis can determine their ordering.
// Returns the number of new tasks added.
func (s *Supervisor) addNewTrackedItems(ctx context.Context) int {
	existing, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return 0
	}
	knownIssues := make(map[int]bool, len(existing))
	for _, t := range existing {
		knownIssues[t.IssueNumber] = true
	}

	items, err := s.store.GetTrackedItems(s.project.ID)
	if err != nil {
		return 0
	}

	matcher := newSkipMatcher(s.project.AutopilotSkipLabel)
	ghClient := s.newGHClient()

	var newTasks []*db.AutopilotTask
	for _, item := range items {
		if item.ItemType != "issue" || item.State != "open" {
			continue
		}
		if knownIssues[item.Number] {
			continue
		}

		// Fetch live status to check labels.
		liveStatus, err := ghClient.FetchItem(ctx, item.Owner, item.Repo, item.Number)
		if err != nil || liveStatus.State != "open" {
			continue
		}

		// Issues with skip label or already being worked on externally
		// become manual tasks (watched for completion).
		isManual := matcher.matches(liveStatus.Labels) ||
			hasLabel(liveStatus.Labels, "in-progress") ||
			hasLabel(liveStatus.Labels, "needs-review") ||
			hasLabel(liveStatus.Labels, "blocked")
		if isManual {
			body := ""
			content, err := ghClient.FetchItemContent(ctx, item.Owner, item.Repo, item.Number, "issue")
			if err == nil {
				body = content.Body
			}
			newTasks = append(newTasks, &db.AutopilotTask{
				ProjectID:    s.project.ID,
				Owner:        item.Owner,
				Repo:         item.Repo,
				IssueNumber:  item.Number,
				IssueTitle:   liveStatus.Title,
				IssueBody:    body,
				Dependencies: "[]",
				Status:       "manual",
			})
			continue
		}

		body := ""
		content, err := ghClient.FetchItemContent(ctx, item.Owner, item.Repo, item.Number, "issue")
		if err == nil {
			body = content.Body
		}

		newTasks = append(newTasks, &db.AutopilotTask{
			ProjectID:    s.project.ID,
			Owner:        item.Owner,
			Repo:         item.Repo,
			IssueNumber:  item.Number,
			IssueTitle:   liveStatus.Title,
			IssueBody:    body,
			Dependencies: "[]",
			Status:       "queued",
		})
	}

	if len(newTasks) == 0 {
		return 0
	}

	added, err := s.store.BulkCreateAutopilotTasks(newTasks)
	if err != nil {
		return 0
	}

	for _, t := range newTasks {
		if t.Status == "manual" {
			s.emitEvent("info", fmt.Sprintf("New manual task #%d: %s (watching for completion)", t.IssueNumber, t.IssueTitle), nil)
		} else {
			s.emitEvent("info", fmt.Sprintf("New task #%d: %s (pending dep analysis)", t.IssueNumber, t.IssueTitle), nil)
		}
	}

	return added
}

func (s *Supervisor) launchAgent(ctx context.Context, slotIdx int, task *db.AutopilotTask) {
	// Set up worktree path and branch.
	home, _ := os.UserHomeDir()
	worktreeBase := filepath.Join(home, ".agent-minder", "worktrees", s.project.Name)
	worktreePath := filepath.Join(worktreeBase, fmt.Sprintf("issue-%d", task.IssueNumber))
	branch := fmt.Sprintf("agent/issue-%d", task.IssueNumber)
	logDir := filepath.Join(home, ".agent-minder", "agents")
	logPath := filepath.Join(logDir, fmt.Sprintf("%s-issue-%d.log", s.project.Name, task.IssueNumber))

	task.WorktreePath = worktreePath
	task.Branch = branch
	task.AgentLog = logPath

	// Update task in DB to running.
	if err := s.store.UpdateAutopilotTaskRunning(task.ID, worktreePath, branch, logPath); err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to update task #%d: %v", task.IssueNumber, err), task)
		return
	}

	// Create per-slot context so StopAgent can cancel just this agent.
	slotCtx, slotCancel := context.WithCancel(ctx)

	s.slots[slotIdx] = &slotState{
		task:       task,
		startedAt:  time.Now(),
		cancelFunc: slotCancel,
	}

	go s.runAgent(slotCtx, slotIdx, task, false)
}

// resumeAgent launches an agent in an existing worktree with a continuation prompt.
// Unlike launchAgent, it skips worktree creation and branch setup, reusing whatever
// state exists from the prior attempt.
func (s *Supervisor) resumeAgent(ctx context.Context, slotIdx int, task *db.AutopilotTask) {
	// Create per-slot context so StopAgent can cancel just this agent.
	slotCtx, slotCancel := context.WithCancel(ctx)

	s.mu.Lock()
	s.slots[slotIdx] = &slotState{
		task:       task,
		startedAt:  time.Now(),
		cancelFunc: slotCancel,
	}
	s.mu.Unlock()

	go s.runAgent(slotCtx, slotIdx, task, true)
}

func (s *Supervisor) runAgent(ctx context.Context, slotIdx int, task *db.AutopilotTask, isResume bool) {
	defer func() {
		s.mu.Lock()
		s.slots[slotIdx] = nil
		s.mu.Unlock()
		// Re-evaluate blocked tasks and fill slots with newly unblocked work.
		// Use parentCtx (not the slot ctx which may be cancelled by StopAgent).
		s.unblockSatisfiedTasks()
		s.fillSlots(s.parentCtx)
	}()

	// failStatus is the status to set on infrastructure errors before the agent runs.
	// Fresh runs use "bailed" (no useful work to resume); resumes use "failed" so the
	// user can retry the resume after a transient error.
	failStatus := "bailed"
	if isResume {
		failStatus = "failed"
	}

	home, _ := os.UserHomeDir()

	if !isResume {
		// Ensure directories exist (fresh run creates the worktree).
		if err := os.MkdirAll(filepath.Dir(task.WorktreePath), 0755); err != nil {
			s.emitEvent("error", fmt.Sprintf("Failed to create worktree dir for #%d: %v", task.IssueNumber, err), task)
			_ = s.store.UpdateAutopilotTaskStatus(task.ID, failStatus)
			return
		}
	}
	if err := os.MkdirAll(filepath.Join(home, ".agent-minder", "agents"), 0755); err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to create agents dir for #%d: %v", task.IssueNumber, err), task)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, failStatus)
		return
	}

	// Get base branch: use configured value if set, otherwise auto-detect.
	baseBranch := s.project.AutopilotBaseBranch
	if baseBranch == "" {
		baseBranch, _ = gitpkg.DefaultBranch(s.repoDir)
	}

	if !isResume {
		// Serialize git branch/worktree operations across concurrent agents to
		// avoid ref lock contention that causes ~15% of fresh launches to fail.
		s.gitSetupMu.Lock()

		// Clean up stale branch from previous run if it exists.
		_ = gitpkg.DeleteBranch(s.repoDir, task.Branch)

		// Note: fetch is done once in fillSlots() before launching agents,
		// not here, to avoid concurrent fetch races on the same repo.

		// Create worktree from the latest remote base branch.
		gitErr := gitpkg.WorktreeAdd(s.repoDir, task.WorktreePath, task.Branch, "origin/"+baseBranch)
		s.gitSetupMu.Unlock()

		if gitErr != nil {
			s.emitEvent("error", fmt.Sprintf("Failed to create worktree for #%d: %v", task.IssueNumber, gitErr), task)
			_ = s.store.UpdateAutopilotTaskStatus(task.ID, failStatus)
			return
		}
	}

	if isResume {
		s.emitEvent("started", fmt.Sprintf("Agent resumed on #%d: %s", task.IssueNumber, task.IssueTitle), task)
	} else {
		s.emitEvent("started", fmt.Sprintf("Agent started on #%d: %s", task.IssueNumber, task.IssueTitle), task)
	}

	// Open log file: append for resumes (preserve prior output), create for fresh runs.
	var logFile *os.File
	var err error
	if isResume {
		logFile, err = os.OpenFile(task.AgentLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	} else {
		logFile, err = os.Create(task.AgentLog)
	}
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to open log for #%d: %v", task.IssueNumber, err), task)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, failStatus)
		if !isResume {
			s.cleanup(task, true)
		}
		return
	}
	defer func() { _ = logFile.Close() }()

	// Ensure an agent definition exists (repo → user → built-in fallback).
	agentDefSource, err := ensureAgentDef(task.WorktreePath)
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to resolve agent definition for #%d: %v", task.IssueNumber, err), task)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, failStatus)
		if !isResume {
			s.cleanup(task, true)
		}
		return
	}
	s.emitEvent("started", fmt.Sprintf("Agent def: %s", agentDefSource.Description()), task)
	debugLog("agent def resolved",
		"stage", "autopilot", "step", "agent-def",
		"issue", task.IssueNumber,
		"source", string(agentDefSource),
		"worktree", task.WorktreePath,
	)

	// Build claude command — use per-task overrides if set, else project defaults.
	maxTurns := task.EffectiveMaxTurns(s.project.AutopilotMaxTurns)
	maxBudget := task.EffectiveMaxBudget(s.project.AutopilotMaxBudgetUSD)

	allowedTools := resolveAllowedTools(s.repoDir)
	if !isResume {
		if onboarding.Exists(s.repoDir) {
			s.emitEvent("info", fmt.Sprintf("Permissions: onboarded (%d tools)", len(allowedTools)), task)
		} else {
			s.emitEvent("info", "Permissions: defaults (run 'agent-minder repo enroll' for project-specific tools)", task)
		}
	}

	testCommand := resolveTestCommand(s.repoDir)

	// Load dependency graph and sibling task statuses so the agent
	// understands where its issue fits in the broader work.
	var rw *relatedWork
	dg, dgErr := s.store.GetDepGraph(s.project.ID)
	siblings, sibErr := s.store.GetAutopilotTasks(s.project.ID)
	if dgErr == nil || sibErr == nil {
		rw = &relatedWork{}
		if dgErr == nil && dg != nil {
			rw.depGraph = dg.GraphJSON
		}
		if sibErr == nil {
			rw.siblingTasks = siblings
		}
	}

	var args []string
	if isResume {
		args = buildResumeClaudeArgs(task, baseBranch, s.owner, s.repo, testCommand, maxTurns, maxBudget, allowedTools, rw)
	} else {
		args = buildClaudeArgs(task, baseBranch, s.owner, s.repo, testCommand, maxTurns, maxBudget, allowedTools, rw)
	}

	debugStep := "launch"
	if isResume {
		debugStep = "resume"
	}
	debugLog("claude command",
		"stage", "autopilot", "step", debugStep,
		"issue", task.IssueNumber,
		"args", strings.Join(args[:len(args)-1], " "), // omit prompt (last arg) for brevity
		"workdir", task.WorktreePath,
	)
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = task.WorktreePath
	cmd.Stderr = logFile

	// Set GITHUB_TOKEN for gh CLI calls within the agent.
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+s.ghToken)

	// Get stdout pipe for stream-json parsing.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to get stdout pipe for #%d: %v", task.IssueNumber, err), task)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, failStatus)
		if !isResume {
			s.cleanup(task, true)
		}
		return
	}

	s.mu.Lock()
	if s.slots[slotIdx] != nil {
		s.slots[slotIdx].cmd = cmd
	}
	s.mu.Unlock()

	// Start the agent process.
	if err := cmd.Start(); err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to start agent for #%d: %v", task.IssueNumber, err), task)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, failStatus)
		if !isResume {
			s.cleanup(task, true)
		}
		return
	}

	// Scanner goroutine: reads stdout, writes to log, updates live status.
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanStream(stdout, logFile, slotIdx, s)
	}()

	// Wait for process to finish.
	err = cmd.Wait()
	<-scanDone // ensure all output is processed before inspecting outcome

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	// Check if this agent was stopped by the user.
	s.mu.Lock()
	stoppedByUser := s.slots[slotIdx] != nil && s.slots[slotIdx].stoppedByUser
	s.mu.Unlock()

	if stoppedByUser {
		// User-initiated stop: set stopped status, preserve worktree for investigation.
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "stopped")
		s.emitEvent("stopped", fmt.Sprintf("Agent stopped by user on #%d", task.IssueNumber), task)
		return
	}

	// Inspect outcome.
	status := s.inspectOutcome(ctx, task, exitCode)
	_ = s.store.UpdateAutopilotTaskStatus(task.ID, status)

	switch status {
	case "review":
		// Swap in-progress → needs-review label on the issue.
		ghClient := s.newGHClient()
		ghClient.RemoveLabel(ctx, s.owner, s.repo, task.IssueNumber, "in-progress")
		_ = ghClient.AddLabel(ctx, s.owner, s.repo, task.IssueNumber, "needs-review")
		s.emitEvent("completed", fmt.Sprintf("Agent completed #%d — PR opened, awaiting review & merge", task.IssueNumber), task)
	case "failed":
		// Remove in-progress label since the agent is done.
		ghClient := s.newGHClient()
		ghClient.RemoveLabel(ctx, s.owner, s.repo, task.IssueNumber, "in-progress")
		// Reload task to get failure_reason populated by inspectOutcome.
		if updated, err := s.store.GetAutopilotTasks(s.project.ID); err == nil {
			for _, t := range updated {
				if t.ID == task.ID {
					s.emitEvent("failed", fmt.Sprintf("Agent failed on #%d (%s)", task.IssueNumber, t.FailureReason), task)
					break
				}
			}
		}
	default:
		s.emitEvent("bailed", fmt.Sprintf("Agent bailed on #%d (exit code %d)", task.IssueNumber, exitCode), task)
	}

	// Cleanup: remove worktree for review tasks (restored on demand later);
	// retain worktree and branch for failed/bailed tasks so work can be recovered.
	if status == "review" {
		s.cleanup(task, false)
	}
}

// defaultReviewMaxRetries is the default number of transient-failure retries
// before giving up on a review agent. Resource exhaustion and bails don't count.
// Configurable per-project via autopilot_review_max_retries.
const defaultReviewMaxRetries = 1

// checkReviewTasks checks tasks in "review" and "reviewed" status.
// For "review" tasks: spawns a review agent if a slot is available.
// For "reviewed" tasks (and "review" tasks that weren't picked up): checks if PRs were merged.
// Returns the number of tasks promoted to "done".
func (s *Supervisor) checkReviewTasks(ctx context.Context) int {
	tasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return 0
	}

	ghClient := s.newGHClient()
	promoted := 0

	for _, task := range tasks {
		if task.PRNumber == 0 {
			continue
		}

		switch task.Status {
		case "review":
			// Try to spawn a review agent if a slot is available and retries not exhausted.
			if s.hasIdleSlot() && s.project.AutopilotReviewMaxTurns != nil {
				retries := s.getReviewRetries(task.ID)
				if retries >= s.reviewMaxRetries() {
					// Retries exhausted — give up on automated review.
					_ = s.store.UpdateAutopilotTaskFailureInfo(task.ID, "review_retries_exhausted",
						fmt.Sprintf("failed %d times due to transient errors", retries))
					_ = s.store.UpdateAutopilotTaskStatus(task.ID, "reviewed")
					s.emitEvent("error", fmt.Sprintf("Review retries exhausted for #%d (PR #%d) — %d transient failures",
						task.IssueNumber, task.PRNumber, retries), &task)
					continue
				}
				s.spawnReviewAgent(ctx, task)
				continue // Don't check merge status — the review agent is handling it.
			}

			// No slot available or review not configured — check for merge.
			if ok := s.promoteIfMerged(ctx, ghClient, task); ok {
				promoted++
			}

		case "reviewed":
			// Auto-merge if enabled and review risk is low.
			if s.project.AutopilotAutoMerge && task.ReviewRisk != nil && *task.ReviewRisk == "low-risk" {
				if s.tryAutoMerge(ctx, ghClient, task) {
					promoted++
					continue
				}
			}
			// Review agent finished — check if PR was merged.
			if ok := s.promoteIfMerged(ctx, ghClient, task); ok {
				promoted++
			}
		}
	}

	return promoted
}

// promoteTaskToDone marks a task as done, cleans up labels, and emits a completion event.
func (s *Supervisor) promoteTaskToDone(ctx context.Context, ghClient *ghpkg.Client, task db.AutopilotTask, eventMsg string) {
	_ = s.store.UpdateAutopilotTaskStatus(task.ID, "done")
	ghClient.RemoveLabel(ctx, s.owner, s.repo, task.IssueNumber, "needs-review")
	if task.ReviewRisk != nil && *task.ReviewRisk != "" {
		ghClient.RemoveLabel(ctx, s.owner, s.repo, task.PRNumber, *task.ReviewRisk)
	}
	s.emitEvent("completed", eventMsg, &task)
}

// promoteIfMerged checks if a task's PR has been merged or closed, and if so
// promotes the task to "done" and cleans up labels. Returns true if promoted.
func (s *Supervisor) promoteIfMerged(ctx context.Context, ghClient *ghpkg.Client, task db.AutopilotTask) bool {
	item, err := ghClient.FetchItem(ctx, s.owner, s.repo, task.PRNumber)
	if err != nil {
		return false
	}
	if item.State == "merged" || item.State == "closed" {
		s.promoteTaskToDone(ctx, ghClient, task,
			fmt.Sprintf("PR #%d for issue #%d merged — dependents unblocked", task.PRNumber, task.IssueNumber))
		return true
	}
	return false
}

// tryAutoMerge attempts to auto-merge a reviewed PR that was assessed as low-risk.
// Squash-merges first, then posts a success comment. On failure, it posts
// an error comment and leaves the task in "reviewed" for manual intervention.
// Returns true if the merge succeeded and the task was promoted to "done".
func (s *Supervisor) tryAutoMerge(ctx context.Context, ghClient *ghpkg.Client, task db.AutopilotTask) bool {
	commitMsg := fmt.Sprintf("Auto-merge: %s (#%d)", task.IssueTitle, task.IssueNumber)

	if err := ghClient.MergePR(ctx, s.owner, s.repo, task.PRNumber, "squash", commitMsg); err != nil {
		// Merge failed — post error comment and leave in reviewed.
		_, _ = ghClient.CreateComment(ctx, s.owner, s.repo, task.PRNumber,
			fmt.Sprintf("Auto-merge failed: %v\n\nPlease merge manually or investigate the failure.", err))
		s.emitEvent("error", fmt.Sprintf("Auto-merge failed for PR #%d (issue #%d): %v",
			task.PRNumber, task.IssueNumber, err), &task)
		return false
	}

	// Merge succeeded — post comment and promote to done.
	_, _ = ghClient.CreateComment(ctx, s.owner, s.repo, task.PRNumber,
		"Auto-merged: reviewed as low-risk by review agent")
	s.promoteTaskToDone(ctx, ghClient, task,
		fmt.Sprintf("Auto-merged PR #%d for issue #%d (low-risk) — dependents unblocked",
			task.PRNumber, task.IssueNumber))
	return true
}

// incrReviewRetry increments the in-memory retry counter for a review task.
func (s *Supervisor) incrReviewRetry(taskID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reviewRetries[taskID]++
}

// getReviewRetries returns the current retry count for a review task.
func (s *Supervisor) getReviewRetries(taskID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reviewRetries[taskID]
}

// reviewMaxRetries returns the effective max review retries for the project.
func (s *Supervisor) reviewMaxRetries() int {
	if s.project.AutopilotReviewMaxRetries != nil {
		return *s.project.AutopilotReviewMaxRetries
	}
	return defaultReviewMaxRetries
}

// spawnReviewAgent launches a review agent for a task in "review" status.
// It restores the worktree if needed, transitions the task to "reviewing",
// and runs the agent in an available slot.
func (s *Supervisor) spawnReviewAgent(ctx context.Context, task db.AutopilotTask) {
	// Fetch first to ensure the branch is available from remote.
	_ = gitpkg.Fetch(s.repoDir)

	// Restore worktree if it doesn't exist.
	if _, err := os.Stat(task.WorktreePath); os.IsNotExist(err) {
		if !gitpkg.BranchExists(s.repoDir, task.Branch) {
			s.incrReviewRetry(task.ID)
			s.emitEvent("error", fmt.Sprintf("Cannot review #%d: branch %s not found locally or on remote (retry %d/%d)",
				task.IssueNumber, task.Branch, s.getReviewRetries(task.ID), s.reviewMaxRetries()), &task)
			return
		}
		if err := os.MkdirAll(filepath.Dir(task.WorktreePath), 0755); err != nil {
			s.incrReviewRetry(task.ID)
			s.emitEvent("error", fmt.Sprintf("Failed to create worktree dir for review of #%d: %v (retry %d/%d)",
				task.IssueNumber, err, s.getReviewRetries(task.ID), s.reviewMaxRetries()), &task)
			return
		}
		if err := gitpkg.WorktreeAddExisting(s.repoDir, task.WorktreePath, task.Branch); err != nil {
			s.incrReviewRetry(task.ID)
			s.emitEvent("error", fmt.Sprintf("Failed to restore worktree for review of #%d: %v (retry %d/%d)",
				task.IssueNumber, err, s.getReviewRetries(task.ID), s.reviewMaxRetries()), &task)
			return
		}
	}

	// Find an idle slot.
	s.mu.Lock()
	slotIdx := -1
	for i, slot := range s.slots {
		if slot == nil {
			slotIdx = i
			break
		}
	}
	if slotIdx == -1 {
		s.mu.Unlock()
		return // No slot available (race with hasIdleSlot check).
	}

	// Transition to reviewing and occupy the slot.
	if err := s.store.UpdateAutopilotTaskStatus(task.ID, "reviewing"); err != nil {
		s.mu.Unlock()
		s.emitEvent("error", fmt.Sprintf("Failed to update task #%d to reviewing: %v", task.IssueNumber, err), &task)
		return
	}

	slotCtx, slotCancel := context.WithCancel(ctx)
	s.slots[slotIdx] = &slotState{
		task:       &task,
		startedAt:  time.Now(),
		cancelFunc: slotCancel,
	}
	s.mu.Unlock()

	s.emitEvent("started", fmt.Sprintf("Review agent started on #%d (PR #%d)", task.IssueNumber, task.PRNumber), &task)

	go s.runReviewAgent(slotCtx, slotIdx, &task)
}

// runReviewAgent runs a reviewer agent process, similar to runAgent but for PR review.
// On completion it parses the risk assessment and transitions to "reviewed".
func (s *Supervisor) runReviewAgent(ctx context.Context, slotIdx int, task *db.AutopilotTask) {
	defer func() {
		s.mu.Lock()
		s.slots[slotIdx] = nil
		s.mu.Unlock()
		// Re-evaluate blocked tasks and fill slots with newly unblocked work.
		s.unblockSatisfiedTasks()
		s.fillSlots(s.parentCtx)
	}()

	home, _ := os.UserHomeDir()

	// Ensure agent log directory exists.
	logDir := filepath.Join(home, ".agent-minder", "agents")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to create agents dir for review of #%d: %v", task.IssueNumber, err), task)
		s.incrReviewRetry(task.ID)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "review") // revert to review for retry
		return
	}

	logPath := filepath.Join(logDir, fmt.Sprintf("%s-issue-%d-review.log", s.project.Name, task.IssueNumber))
	task.AgentLog = logPath

	// Ensure a reviewer agent definition exists.
	agentDefSource, err := ensureAgentDefByName(task.WorktreePath, AgentReviewer)
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to resolve reviewer agent def for #%d: %v", task.IssueNumber, err), task)
		s.incrReviewRetry(task.ID)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "review") // revert for retry
		return
	}
	s.emitEvent("info", fmt.Sprintf("Reviewer def: %s", agentDefSource.DescriptionFor(AgentReviewer)), task)

	// Get base branch.
	baseBranch := s.project.AutopilotBaseBranch
	if baseBranch == "" {
		baseBranch, _ = gitpkg.DefaultBranch(s.repoDir)
	}

	// Use review-specific resource limits from project config.
	maxTurns := 10 // sensible default
	if s.project.AutopilotReviewMaxTurns != nil {
		maxTurns = *s.project.AutopilotReviewMaxTurns
	}
	maxBudget := 2.0 // sensible default
	if s.project.AutopilotReviewMaxBudgetUSD != nil {
		maxBudget = *s.project.AutopilotReviewMaxBudgetUSD
	}

	allowedTools := resolveAllowedTools(s.repoDir)
	projectGoal := s.project.GoalDescription
	testCommand := resolveTestCommand(s.repoDir)

	// Load dependency graph and sibling task statuses for review context.
	var rw *relatedWork
	dg, err := s.store.GetDepGraph(s.project.ID)
	tasks, taskErr := s.store.GetAutopilotTasks(s.project.ID)
	if err == nil || taskErr == nil {
		rw = &relatedWork{}
		if err == nil && dg != nil {
			rw.depGraph = dg.GraphJSON
		}
		if taskErr == nil {
			rw.siblingTasks = tasks
		}
	}

	args := buildReviewClaudeArgs(task, baseBranch, s.owner, s.repo, projectGoal, testCommand, maxTurns, maxBudget, allowedTools, rw)

	debugLog("review agent command",
		"stage", "autopilot", "step", "review-launch",
		"issue", task.IssueNumber,
		"pr", task.PRNumber,
		"args", strings.Join(args[:len(args)-1], " "),
		"workdir", task.WorktreePath,
	)

	logFile, err := os.Create(logPath)
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to open review log for #%d: %v", task.IssueNumber, err), task)
		s.incrReviewRetry(task.ID)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "review") // revert for retry
		return
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = task.WorktreePath
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+s.ghToken)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to get stdout pipe for review of #%d: %v", task.IssueNumber, err), task)
		s.incrReviewRetry(task.ID)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "review") // revert for retry
		return
	}

	s.mu.Lock()
	if s.slots[slotIdx] != nil {
		s.slots[slotIdx].cmd = cmd
	}
	s.mu.Unlock()

	if err := cmd.Start(); err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to start review agent for #%d: %v", task.IssueNumber, err), task)
		s.incrReviewRetry(task.ID)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "review") // revert for retry
		return
	}

	// Scanner goroutine: reads stdout, writes to log, updates live status.
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanStream(stdout, logFile, slotIdx, s)
	}()

	// Wait for process to finish.
	_ = cmd.Wait()
	<-scanDone

	// Check if stopped by user.
	s.mu.Lock()
	stoppedByUser := s.slots[slotIdx] != nil && s.slots[slotIdx].stoppedByUser
	s.mu.Unlock()

	if stoppedByUser {
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "review") // revert to review so it can be retried
		s.emitEvent("stopped", fmt.Sprintf("Review agent stopped by user on #%d", task.IssueNumber), task)
		return
	}

	// Parse agent log and classify outcome.
	agentResult, _ := parseAgentLog(logPath)
	if agentResult != nil && agentResult.TotalCost > 0 {
		_ = s.store.UpdateAutopilotTaskCost(task.ID, agentResult.TotalCost)
	}

	_, reason, detail := classifyOutcome(agentResult, maxTurns, maxBudget)

	switch {
	case reason == "max_turns" || reason == "max_budget":
		// Resource exhaustion — no retry, store failure, transition to reviewed.
		_ = s.store.UpdateAutopilotTaskFailureInfo(task.ID, reason, detail)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "reviewed")
		s.emitEvent("completed", fmt.Sprintf("Review agent hit %s on #%d (PR #%d): %s — no retry",
			reason, task.IssueNumber, task.PRNumber, detail), task)

	case agentResult == nil || agentResult.Result == "":
		// Bail — agent produced no output. No retry.
		_ = s.store.UpdateAutopilotTaskFailureInfo(task.ID, "review_bail", "reviewer produced no output")
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "reviewed")
		s.emitEvent("completed", fmt.Sprintf("Review agent bailed on #%d (PR #%d) — no output, no retry",
			task.IssueNumber, task.PRNumber), task)

	case reason == "error":
		// Explicit error from agent — treat as transient, revert for retry.
		s.incrReviewRetry(task.ID)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "review")
		s.emitEvent("error", fmt.Sprintf("Review agent errored on #%d (PR #%d): %s (retry %d/%d)",
			task.IssueNumber, task.PRNumber, detail, s.getReviewRetries(task.ID), s.reviewMaxRetries()), task)

	default:
		// Success — parse risk, post structured comment, apply label, transition to reviewed.
		riskLevel := parseReviewRisk(agentResult)
		label := riskToLabel(riskLevel)

		// Post structured review comment to the PR and apply risk label.
		var commentID int64
		ghClient := s.newGHClient()
		body := formatReviewComment(agentResult.Result, label)
		if cid, err := ghClient.CreateComment(ctx, s.owner, s.repo, task.PRNumber, body); err != nil {
			s.emitEvent("error", fmt.Sprintf("Failed to post review comment on PR #%d: %v", task.PRNumber, err), task)
		} else {
			commentID = cid
		}
		// Remove any previously applied risk labels, then apply the current one.
		for _, old := range riskLabels {
			ghClient.RemoveLabel(ctx, s.owner, s.repo, task.PRNumber, old)
		}
		if err := ghClient.AddLabel(ctx, s.owner, s.repo, task.PRNumber, label); err != nil {
			s.emitEvent("error", fmt.Sprintf("Failed to apply label %q to PR #%d: %v", label, task.PRNumber, err), task)
		}

		_ = s.store.UpdateAutopilotTaskReview(task.ID, label, commentID)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "reviewed")
		s.emitEvent("completed", fmt.Sprintf("Review agent finished #%d (PR #%d, risk: %s)",
			task.IssueNumber, task.PRNumber, label), task)
	}

	// Clean up worktree after review — it can be restored again if needed.
	s.cleanup(task, false)
}

// parseReviewRisk extracts the risk level from a review agent's output.
// Looks for "**Risk level:** low|medium|high" in the result text.
func parseReviewRisk(result *AgentResult) string {
	if result == nil || result.Result == "" {
		return "unknown"
	}

	text := strings.ToLower(result.Result)

	// Look for the structured risk assessment pattern.
	for _, marker := range []string{"**risk level:**", "risk level:"} {
		idx := strings.Index(text, marker)
		if idx < 0 {
			continue
		}
		after := strings.TrimSpace(text[idx+len(marker):])
		// Take the first word after the marker.
		fields := strings.Fields(after)
		if len(fields) > 0 {
			risk := fields[0]
			switch {
			case strings.HasPrefix(risk, "low"):
				return "low"
			case strings.HasPrefix(risk, "medium"):
				return "medium"
			case strings.HasPrefix(risk, "high"):
				return "high"
			}
		}
	}

	return "unknown"
}

// riskLabels is the set of all risk-tier labels that may be applied to a PR.
// Used to remove stale labels before applying the current assessment.
var riskLabels = []string{"low-risk", "needs-testing", "suspect"}

// riskToLabel maps the reviewer agent's risk level to a PR label.
// low → low-risk, medium → needs-testing, high → suspect.
// Unknown or unrecognized levels map to "needs-testing" as the safe default.
func riskToLabel(risk string) string {
	switch risk {
	case "low":
		return "low-risk"
	case "medium":
		return "needs-testing"
	case "high":
		return "suspect"
	default:
		return "needs-testing"
	}
}

// formatReviewComment builds a structured markdown comment for a PR review.
// It wraps the reviewer agent's output with a prominent risk tier header.
func formatReviewComment(agentOutput, label string) string {
	var b strings.Builder

	// Prominent risk tier header.
	emoji := "⚠️"
	recommendation := "Test first"
	switch label {
	case "low-risk":
		emoji = "✅"
		recommendation = "Merge"
	case "needs-testing":
		emoji = "⚠️"
		recommendation = "Test first"
	case "suspect":
		emoji = "🔴"
		recommendation = "Needs rework"
	}

	fmt.Fprintf(&b, "## %s Automated Review — `%s`\n\n", emoji, label)
	fmt.Fprintf(&b, "**Recommendation:** %s\n\n", recommendation)
	fmt.Fprintf(&b, "---\n\n")

	// Include the agent's structured assessment.
	b.WriteString(strings.TrimSpace(agentOutput))
	b.WriteString("\n\n---\n*Reviewed by agent-minder autopilot reviewer*\n")

	return b.String()
}

// unblockSatisfiedTasks re-evaluates blocked tasks and promotes them to queued
// when all their dependencies are satisfied. Returns the number of tasks unblocked.
func (s *Supervisor) unblockSatisfiedTasks() int {
	tasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return 0
	}

	statusMap := make(map[int]string, len(tasks))
	for _, t := range tasks {
		statusMap[t.IssueNumber] = t.Status
	}

	trackedItems, _ := s.store.GetTrackedItems(s.project.ID)
	trackedState := make(map[int]string, len(trackedItems))
	for _, item := range trackedItems {
		trackedState[item.Number] = item.State
	}

	unblocked := 0
	for _, t := range tasks {
		if t.Status != "blocked" {
			continue
		}
		deps := parseDeps(t.Dependencies)
		allSatisfied := true
		for _, dep := range deps {
			if taskStatus, ok := statusMap[dep]; ok {
				if taskStatus != "done" {
					allSatisfied = false
					break
				}
			} else if state, ok := trackedState[dep]; ok {
				if state == "open" {
					allSatisfied = false
					break
				}
			}
		}
		if allSatisfied {
			_ = s.store.UpdateAutopilotTaskStatus(t.ID, "queued")
			s.emitEvent("warning", fmt.Sprintf("Task #%d unblocked — dependencies satisfied", t.IssueNumber), &t)
			unblocked++
		}
	}
	return unblocked
}

// checkManualTasks checks if any manual (non-AI) tasks have had their GitHub issues closed.
// When a manual task's issue is closed, it's promoted to "done" so dependents unblock.
// Returns the number of tasks promoted.
func (s *Supervisor) checkManualTasks(ctx context.Context) int {
	tasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return 0
	}

	ghClient := s.newGHClient()
	promoted := 0

	for _, task := range tasks {
		if task.Status != "manual" {
			continue
		}

		item, err := ghClient.FetchItem(ctx, task.Owner, task.Repo, task.IssueNumber)
		if err != nil {
			continue
		}

		if item.State != "open" {
			_ = s.store.UpdateAutopilotTaskStatus(task.ID, "done")
			promoted++
			s.emitEvent("completed", fmt.Sprintf("Manual task #%d closed — dependents unblocked", task.IssueNumber), &task)
		}
	}

	return promoted
}

func (s *Supervisor) inspectOutcome(ctx context.Context, task *db.AutopilotTask, exitCode int) string {
	// Parse agent log for result event to detect structured failures.
	agentResult, err := parseAgentLog(task.AgentLog)
	if err != nil {
		debugLog("inspectOutcome: parse agent log failed",
			"issue", task.IssueNumber, "error", err.Error())
	}
	if agentResult != nil {
		// Persist agent cost regardless of outcome.
		if agentResult.TotalCost > 0 {
			_ = s.store.UpdateAutopilotTaskCost(task.ID, agentResult.TotalCost)
		}

		effectiveTurns := task.EffectiveMaxTurns(s.project.AutopilotMaxTurns)
		effectiveBudget := task.EffectiveMaxBudget(s.project.AutopilotMaxBudgetUSD)
		status, reason, detail := classifyOutcome(agentResult, effectiveTurns, effectiveBudget)
		if status == "failed" {
			_ = s.store.UpdateAutopilotTaskFailure(task.ID, reason, detail)
			debugLog("inspectOutcome: classified as failed",
				"issue", task.IssueNumber, "reason", reason)

			// Check for PR even on failure — agent may have opened one before exhausting limits.
			ghClient := s.newGHClient()
			pr, err := ghClient.FetchPRForBranch(ctx, s.owner, s.repo, task.Branch)
			if err == nil && pr != nil && pr.Number > 0 {
				_ = s.store.UpdateAutopilotTaskPR(task.ID, pr.Number)
				task.PRNumber = pr.Number // Update in memory so caller sees it.
				debugLog("inspectOutcome: failed task has PR, promoting to review",
					"issue", task.IssueNumber, "pr", pr.Number)
				return "review"
			}

			return "failed"
		}
		if status == "warning" {
			var denials []json.RawMessage
			_ = json.Unmarshal([]byte(detail), &denials)
			s.emitEvent("info", fmt.Sprintf("#%d had %d permission denial(s) — checking for PR", task.IssueNumber, len(denials)), task)
			debugLog("inspectOutcome: warning (non-fatal)",
				"issue", task.IssueNumber, "reason", reason, "detail", detail)
			// Continue to PR check — agent may have completed despite warnings.
		}
	}

	// Check if a PR was opened for this branch.
	ghClient := s.newGHClient()
	pr, err := ghClient.FetchPRForBranch(ctx, s.owner, s.repo, task.Branch)
	if err == nil && pr != nil && pr.Number > 0 {
		_ = s.store.UpdateAutopilotTaskPR(task.ID, pr.Number)
		return "review" // awaiting human review & merge before dependents unblock
	}
	return "bailed"
}

func (s *Supervisor) cleanup(task *db.AutopilotTask, deleteBranch bool) {
	// Serialize with git setup operations to avoid ref lock contention.
	s.gitSetupMu.Lock()
	defer s.gitSetupMu.Unlock()

	// Remove worktree.
	_ = gitpkg.WorktreeRemove(s.repoDir, task.WorktreePath)

	// Delete branch only if bailed (keep if PR opened).
	if deleteBranch {
		_ = gitpkg.DeleteBranch(s.repoDir, task.Branch)
	}
}

func (s *Supervisor) emitEvent(typ, summary string, task *db.AutopilotTask) {
	select {
	case s.events <- Event{
		Time:    time.Now(),
		Type:    typ,
		Summary: summary,
		Task:    task,
	}:
	default:
		// Drop event if channel is full.
	}
}

// userHomeDir is the function used to resolve the user's home directory.
// Tests can override this to isolate from the real filesystem.
var userHomeDir = os.UserHomeDir

// buildClaudeArgs constructs the argument list for the claude CLI invocation.
// It always uses --agent autopilot with a minimal task-context prompt. The agent
// definition is resolved via a three-tier failover chain (repo → user → built-in)
// by ensureAgentDef(), which must be called before this function.
//
// Instead of --dangerously-skip-permissions, each tool from allowedTools is
// passed as a separate --allowedTools flag, giving the agent least-privilege
// access scoped to the tools it actually needs.
func buildClaudeArgs(task *db.AutopilotTask, baseBranch, owner, repo, testCommand string, maxTurns int, maxBudget float64, allowedTools []string, rw *relatedWork) []string {
	prompt := renderTaskContext(task, baseBranch, owner, repo, testCommand, rw)
	return []string{
		"--agent", "autopilot",
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", strconv.Itoa(maxTurns),
		"--max-budget-usd", fmt.Sprintf("%.2f", maxBudget),
		"--allowedTools", toCliAllowedTools(allowedTools),
		"--", prompt,
	}
}

// buildResumeClaudeArgs constructs the argument list for resuming a claude agent
// in an existing worktree. Uses a continuation prompt instead of a fresh start prompt.
func buildResumeClaudeArgs(task *db.AutopilotTask, baseBranch, owner, repo, testCommand string, maxTurns int, maxBudget float64, allowedTools []string, rw *relatedWork) []string {
	prompt := renderResumeTaskContext(task, baseBranch, owner, repo, testCommand, rw)
	return []string{
		"--agent", "autopilot",
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", strconv.Itoa(maxTurns),
		"--max-budget-usd", fmt.Sprintf("%.2f", maxBudget),
		"--allowedTools", toCliAllowedTools(allowedTools),
		"--resume",
		"--", prompt,
	}
}

// RebuildResult holds the outcome of a dependency graph rebuild.
type RebuildResult struct {
	Unblocked int
	Skipped   int
}

// RebuildDependencies re-analyzes the dependency graph for queued and blocked tasks,
// incorporating user guidance. Running/done/review tasks are untouched.
func (s *Supervisor) RebuildDependencies(ctx context.Context, userComment string) ([]DepOption, error) {
	allTasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return nil, fmt.Errorf("get tasks: %w", err)
	}

	// Partition tasks: rebuildable (queued + blocked + skipped + manual) vs context-only (running, done, bailed, stopped, review).
	// Skipped tasks are included so the user can un-skip them via guidance.
	// Manual tasks are included so the LLM can wire them into the dep graph.
	var rebuildable []*db.AutopilotTask
	var contextTasks []*db.AutopilotTask
	for i := range allTasks {
		switch allTasks[i].Status {
		case "queued", "blocked", "skipped", "manual":
			rebuildable = append(rebuildable, &allTasks[i])
		default:
			contextTasks = append(contextTasks, &allTasks[i])
		}
	}

	if len(rebuildable) == 0 {
		return nil, nil
	}

	// Separate agent tasks from manual tasks for distinct prompt sections.
	var agentRebuildable, manualRebuildable []*db.AutopilotTask
	for _, t := range rebuildable {
		if t.Status == "manual" {
			manualRebuildable = append(manualRebuildable, t)
		} else {
			agentRebuildable = append(agentRebuildable, t)
		}
	}

	// Build issue list for the LLM.
	var issueList strings.Builder
	issueList.WriteString("## Agent issues to re-analyze dependencies for:\n")
	for _, t := range agentRebuildable {
		fmt.Fprintf(&issueList, "Issue #%d: %s\n", t.IssueNumber, t.IssueTitle)
		if t.IssueBody != "" {
			body := t.IssueBody
			if len(body) > 200 {
				body = body[:200] + "..."
			}
			fmt.Fprintf(&issueList, "  %s\n", body)
		}
	}

	// Include manual tasks in their own section.
	if len(manualRebuildable) > 0 {
		issueList.WriteString("\n## Manual tasks (non-AI, human-driven — can block agent tasks, watched for completion):\n")
		for _, t := range manualRebuildable {
			fmt.Fprintf(&issueList, "Issue #%d: %s\n", t.IssueNumber, t.IssueTitle)
			if t.IssueBody != "" {
				body := t.IssueBody
				if len(body) > 200 {
					body = body[:200] + "..."
				}
				fmt.Fprintf(&issueList, "  %s\n", body)
			}
		}
	}

	// Include running/done/bailed/stopped/review tasks as context.
	if len(contextTasks) > 0 {
		issueList.WriteString("\n## Other tasks (not being re-analyzed, but may be dependencies):\n")
		for _, t := range contextTasks {
			fmt.Fprintf(&issueList, "Issue #%d [%s]: %s\n", t.IssueNumber, t.Status, t.IssueTitle)
		}
	}

	// Include tracked items not in autopilot tasks as additional context.
	allIssues := make(map[int]bool)
	for _, t := range allTasks {
		allIssues[t.IssueNumber] = true
	}
	trackedItems, _ := s.store.GetTrackedItems(s.project.ID)
	var extContext strings.Builder
	for _, item := range trackedItems {
		if allIssues[item.Number] || item.ItemType != "issue" {
			continue
		}
		fmt.Fprintf(&extContext, "Issue #%d [%s]: %s\n", item.Number, item.State, item.Title)
	}
	if extContext.Len() > 0 {
		issueList.WriteString("\n## Other tracked issues (not autopilot tasks):\n")
		issueList.WriteString(extContext.String())
	}

	// Build previous dep state context.
	var prevDeps strings.Builder
	prevDeps.WriteString("Previous dependency analysis:\n")
	hasPrevDeps := false
	for _, t := range rebuildable {
		if t.Dependencies != "" && t.Dependencies != "[]" {
			fmt.Fprintf(&prevDeps, "  #%d depends on %s\n", t.IssueNumber, t.Dependencies)
			hasPrevDeps = true
		}
	}

	// Build prompt.
	var promptParts []string
	promptParts = append(promptParts, fmt.Sprintf(`Analyze these GitHub issues and determine dependencies between them.
A dependency means issue B cannot start until issue A is completed (e.g., B builds on A's infrastructure or schema changes).

Dependencies can include issues from ALL sections — an agent task can depend on a manual task, running task, or external issue.

%s`, issueList.String()))

	if hasPrevDeps {
		promptParts = append(promptParts, prevDeps.String())
	}

	if strings.TrimSpace(userComment) != "" {
		promptParts = append(promptParts, fmt.Sprintf("User feedback on dependencies:\n  %q\n\nRe-analyze and update the dependency graph considering this feedback.", userComment))
	}

	promptParts = append(promptParts, `Respond with ONLY a JSON array of exactly 3 dependency graph options, ranked from most likely correct to least likely. Each option has a "name", "rationale", and "graph" field.

The "graph" is an object where:
- Keys for AGENT tasks: values are arrays of integer issue numbers that must complete first. Issues with no dependencies get an empty array.
- Keys for MANUAL tasks: value MUST be the string "manual" — these are human-driven tasks that agents may depend on.
- If the user asks to skip or exclude an issue, set its value to the string "skip" instead of an array.

IMPORTANT: Use integer values in dependency arrays, not strings. Agent tasks CAN depend on manual task issue numbers.

The 3 options should represent meaningfully different dependency strategies:
- Option 1: Your best analysis of actual dependencies
- Option 2: An alternative interpretation (e.g., more conservative or more parallel)
- Option 3: A different trade-off (e.g., maximum parallelism or stricter ordering)

Example response (where #99 is a manual task):
[
  {"name": "Conservative — minimal deps", "rationale": "API work must wait for manual schema migration.", "graph": {"99": "manual", "42": [99], "38": [42], "15": []}},
  {"name": "Moderate — logical grouping", "rationale": "Groups related features; UI issues wait on API changes.", "graph": {"99": "manual", "42": [99], "38": [42], "15": [42]}},
  {"name": "Maximum parallelism", "rationale": "All agent issues can start independently.", "graph": {"99": "manual", "42": [], "38": [], "15": []}}
]`)

	prompt := strings.Join(promptParts, "\n\n")

	depModel := s.project.LLMAnalyzerModel
	if depModel == "" {
		depModel = "opus"
	}

	var options []DepOption

	for attempt := 0; attempt < 2; attempt++ {
		promptText := prompt
		if attempt > 0 {
			promptText = prompt + "\n\nYour previous response was not valid JSON. Please respond with ONLY the JSON array of 3 options, no text before or after."
		}

		resp, err := s.completer.Complete(ctx, &claudecli.Request{
			SystemPrompt: "You are analyzing issue dependencies. Respond with ONLY valid JSON, no explanation or preamble.",
			Prompt:       promptText,
			Model:        depModel,
			DisableTools: true,
		})
		if err != nil {
			return nil, fmt.Errorf("LLM call: %w", err)
		}

		content := strings.TrimSpace(resp.Content())
		// Strip markdown fencing if present.
		if strings.HasPrefix(content, "```") {
			lines := strings.Split(content, "\n")
			if len(lines) > 2 {
				content = strings.Join(lines[1:len(lines)-1], "\n")
			}
		}
		// Extract JSON array.
		if start := strings.Index(content, "["); start >= 0 {
			if end := strings.LastIndex(content, "]"); end > start {
				content = content[start : end+1]
			}
		}

		var rawOptions []struct {
			Name      string                     `json:"name"`
			Rationale string                     `json:"rationale"`
			Graph     map[string]json.RawMessage `json:"graph"`
		}

		if err := json.Unmarshal([]byte(content), &rawOptions); err != nil {
			if attempt == 0 {
				continue
			}
			return nil, fmt.Errorf("parse dep options: %w", err)
		}

		for _, ro := range rawOptions {
			opt := DepOption{
				Name:      ro.Name,
				Rationale: ro.Rationale,
				Graph:     ro.Graph,
				Unblocked: countUnblocked(ro.Graph, rebuildable),
			}
			options = append(options, opt)
		}
		break
	}

	if len(options) == 0 {
		return nil, fmt.Errorf("LLM returned no options")
	}

	return options, nil
}

// ApplyRebuildDepOption applies a dep option during a running autopilot session,
// handling the additional concerns of un-skipping, re-evaluating blocked status,
// and filling slots with newly unblocked tasks.
func (s *Supervisor) ApplyRebuildDepOption(ctx context.Context, opt DepOption) (RebuildResult, error) {
	allTasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return RebuildResult{}, fmt.Errorf("get tasks: %w", err)
	}

	// Only update rebuildable tasks (queued, blocked, skipped, manual).
	var skipped int
	for _, t := range allTasks {
		switch t.Status {
		case "queued", "blocked", "skipped", "manual":
			// proceed
		default:
			continue
		}

		key := strconv.Itoa(t.IssueNumber)
		rawDeps, ok := opt.Graph[key]
		if !ok {
			// LLM didn't mention this task — clear deps, un-skip if needed.
			_ = s.store.UpdateAutopilotTaskDeps(t.ID, "[]")
			if t.Status == "skipped" {
				_ = s.store.UpdateAutopilotTaskStatus(t.ID, "queued")
			}
			// Manual tasks stay manual even if not mentioned.
			continue
		}

		// Check for string directives: "skip" or "manual".
		var strVal string
		if json.Unmarshal(rawDeps, &strVal) == nil {
			switch strVal {
			case "skip":
				_ = s.store.UpdateAutopilotTaskStatus(t.ID, "skipped")
				s.emitEvent("warning", fmt.Sprintf("Task #%d excluded from autopilot", t.IssueNumber), &t)
				skipped++
			case "manual":
				_ = s.store.UpdateAutopilotTaskStatus(t.ID, "manual")
				s.emitEvent("warning", fmt.Sprintf("Task #%d is manual (non-AI) — watching for completion", t.IssueNumber), &t)
			}
			continue
		}

		var deps []int
		if err := json.Unmarshal(rawDeps, &deps); err != nil {
			deps = nil // reset: failed unmarshal may have partially initialized the slice
			var strDeps []string
			if err2 := json.Unmarshal(rawDeps, &strDeps); err2 != nil {
				_ = s.store.UpdateAutopilotTaskDeps(t.ID, "[]")
				continue
			}
			for _, sd := range strDeps {
				if n, err3 := strconv.Atoi(sd); err3 == nil {
					deps = append(deps, n)
				}
			}
		}

		depsJSON, _ := json.Marshal(deps)
		if len(deps) == 0 {
			depsJSON = []byte("[]")
		}
		_ = s.store.UpdateAutopilotTaskDeps(t.ID, string(depsJSON))

		// Un-skip if needed (but don't un-manual).
		if t.Status == "skipped" {
			_ = s.store.UpdateAutopilotTaskStatus(t.ID, "queued")
		}
	}

	// Re-evaluate blocked status.
	freshTasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return RebuildResult{}, fmt.Errorf("refresh tasks: %w", err)
	}
	statusMap := make(map[int]string, len(freshTasks))
	for _, t := range freshTasks {
		statusMap[t.IssueNumber] = t.Status
	}
	trackedItems, _ := s.store.GetTrackedItems(s.project.ID)
	trackedState := make(map[int]string, len(trackedItems))
	for _, item := range trackedItems {
		trackedState[item.Number] = item.State
	}

	unblocked := 0
	for _, t := range freshTasks {
		if t.Status != "queued" && t.Status != "blocked" {
			continue
		}
		deps := parseDeps(t.Dependencies)
		allSatisfied := true
		anyDep := false
		for _, dep := range deps {
			anyDep = true
			if taskStatus, ok := statusMap[dep]; ok {
				if taskStatus != "done" {
					allSatisfied = false
					break
				}
			} else if state, ok := trackedState[dep]; ok {
				if state == "open" {
					allSatisfied = false
					break
				}
			}
		}
		if allSatisfied && t.Status == "blocked" {
			_ = s.store.UpdateAutopilotTaskStatus(t.ID, "queued")
			unblocked++
		} else if !allSatisfied && anyDep && t.Status == "queued" {
			_ = s.store.UpdateAutopilotTaskStatus(t.ID, "blocked")
		}
	}

	// Fill slots with any newly unblocked tasks.
	if unblocked > 0 && s.parentCtx != nil {
		s.fillSlots(s.parentCtx)
	}

	// Store the rebuilt graph for persistence.
	graphJSON, _ := json.Marshal(opt.Graph)
	_ = s.store.SaveDepGraph(s.project.ID, string(graphJSON), opt.Name)

	s.emitEvent("completed", fmt.Sprintf("Dep graph rebuilt — %d unblocked, %d skipped", unblocked, skipped), nil)
	return RebuildResult{Unblocked: unblocked, Skipped: skipped}, nil
}

// parseDeps parses a JSON dependency array into issue numbers.
func parseDeps(deps string) []int {
	if deps == "" || deps == "[]" {
		return nil
	}
	depStr := strings.Trim(deps, "[]")
	if depStr == "" {
		return nil
	}
	var result []int
	for _, s := range strings.Split(depStr, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			var n int
			if _, err := fmt.Sscanf(s, "%d", &n); err == nil {
				result = append(result, n)
			}
		}
	}
	return result
}

// emitEventLocked is like emitEvent but must be called while s.mu is held.
// It uses a non-blocking send so it won't deadlock.
func (s *Supervisor) emitEventLocked(typ, summary string, task *db.AutopilotTask) {
	select {
	case s.events <- Event{
		Time:    time.Now(),
		Type:    typ,
		Summary: summary,
		Task:    task,
	}:
	default:
	}
}
