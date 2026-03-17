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

	"github.com/dustinlange/agent-minder/internal/db"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
	"github.com/dustinlange/agent-minder/internal/llm"
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
	store    *db.Store
	project  *db.Project
	provider llm.Provider
	repoDir  string // primary repo for worktree operations
	owner    string
	repo     string
	ghToken  string

	mu         sync.Mutex
	slots      []*slotState // len == maxAgents; nil = idle
	active     bool
	paused     bool // when true, fillSlots returns early
	preparedAt time.Time
	events     chan Event
	parentCtx  context.Context // parent context for the session (used by slot goroutines for fillSlots)
	cancel     context.CancelFunc
	done       chan struct{}
	discovery  chan struct{} // signal to trigger immediate discovery
}

// New creates a new Supervisor.
func New(store *db.Store, project *db.Project, provider llm.Provider, repoDir, owner, repo, ghToken string) *Supervisor {
	maxAgents := project.AutopilotMaxAgents
	if maxAgents < 1 {
		maxAgents = 3
	}
	return &Supervisor{
		store:     store,
		project:   project,
		provider:  provider,
		repoDir:   repoDir,
		owner:     owner,
		repo:      repo,
		ghToken:   ghToken,
		slots:     make([]*slotState, maxAgents),
		events:    make(chan Event, 64),
		discovery: make(chan struct{}, 1),
	}
}

// Events returns the channel of events for the TUI.
func (s *Supervisor) Events() <-chan Event {
	return s.events
}

// IsActive returns true if the supervisor has been launched and is running.
func (s *Supervisor) IsActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// TriggerDiscovery signals the supervisor to check for new tracked items immediately.
func (s *Supervisor) TriggerDiscovery() {
	select {
	case s.discovery <- struct{}{}:
	default: // already pending
	}
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
	// Use --dangerously-skip-permissions so gh commands run without approval.
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", "3",
		"--dangerously-skip-permissions",
		prompt,
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
	if task.Status != "bailed" && task.Status != "stopped" {
		return fmt.Errorf("task #%d has status %q — only bailed or stopped tasks can be restarted", task.IssueNumber, task.Status)
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

// IsPaused returns whether slot filling is currently paused.
func (s *Supervisor) IsPaused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused
}

// Prepare fetches tracked issues, creates autopilot tasks, and builds a dependency graph.
// Always starts fresh — clears previous tasks, cleans up orphaned worktrees.
// Optional guidance is passed to the LLM during dependency analysis.
func (s *Supervisor) Prepare(ctx context.Context, guidance string) (total int, options []DepOption, agentDef AgentDefSource, err error) {
	// Validate configured base branch exists in at least one enrolled repo.
	if s.project.AutopilotBaseBranch != "" {
		repos, err := s.store.GetRepos(s.project.ID)
		if err != nil {
			return 0, nil, "", fmt.Errorf("get enrolled repos: %w", err)
		}
		found := false
		for _, r := range repos {
			if gitpkg.BranchExists(r.Path, s.project.AutopilotBaseBranch) {
				found = true
				break
			}
		}
		if !found {
			return 0, nil, "", fmt.Errorf("configured base branch %q not found in any enrolled repo", s.project.AutopilotBaseBranch)
		}
	}

	// Detect which agent definition tier will be used.
	agentDef = detectAgentDef(s.repoDir)

	// Clean up any leftovers from a previous run.
	if err := s.store.ClearAutopilotTasks(s.project.ID); err != nil {
		return 0, nil, "", fmt.Errorf("clear autopilot tasks: %w", err)
	}
	s.cleanOrphanedWorktrees()

	// Convert tracked items to autopilot tasks.
	tasks, err := s.convertTrackedItems(ctx)
	if err != nil {
		return 0, nil, "", fmt.Errorf("convert tracked items: %w", err)
	}
	if len(tasks) == 0 {
		return 0, nil, agentDef, nil
	}

	// Build dependency graph options.
	options, err = s.buildDepOptions(ctx, tasks, guidance)
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Dependency graph failed: %v — falling back to sequential order", err), nil)
		// Build a single sequential fallback option.
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

	return len(tasks), options, agentDef, nil
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

	s.mu.Lock()
	s.preparedAt = time.Now()
	s.mu.Unlock()

	return nil
}

// cleanOrphanedWorktrees removes worktrees left behind by interrupted agents.
func (s *Supervisor) cleanOrphanedWorktrees() {
	worktreeBase := filepath.Join(os.Getenv("HOME"), ".agent-minder", "worktrees", s.project.Name)
	entries, err := os.ReadDir(worktreeBase)
	if err != nil {
		return // directory may not exist
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(worktreeBase, e.Name())
		if err := gitpkg.WorktreeRemove(s.repoDir, path); err != nil {
			// Best-effort: try direct removal if git worktree remove fails.
			_ = os.RemoveAll(path)
		}
	}
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
		discoveryTicker := time.NewTicker(60 * time.Second)
		defer discoveryTicker.Stop()

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
			case <-discoveryTicker.C:
				if s.hasIdleSlot() {
					added := s.discoverNewTasks(ctx)
					if added > 0 {
						s.fillSlots(ctx)
					}
				}
			case <-s.discovery:
				added := s.discoverNewTasks(ctx)
				if added > 0 {
					s.fillSlots(ctx)
				}
			default:
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
					if t.Status == "queued" || t.Status == "review" || t.Status == "manual" || t.Status == "blocked" {
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
	var queued, running, review, done, bailed, stopped, manual int
	for _, t := range tasks {
		switch t.Status {
		case "queued":
			queued++
		case "running":
			running++
		case "review":
			review++
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
	summary := fmt.Sprintf("\nTask summary: %d queued, %d running, %d in review, %d done, %d bailed, %d stopped", queued, running, review, done, bailed, stopped)
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
	ghClient := ghpkg.NewClient(s.ghToken)

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

	messages := []llm.Message{{Role: "user", Content: prompt}}

	var options []DepOption
	var content string

	for attempt := 0; attempt < 2; attempt++ {
		resp, err := s.provider.Complete(ctx, &llm.Request{
			Model:       s.project.LLMAnalyzerModel,
			System:      "You are analyzing issue dependencies. Respond with ONLY valid JSON, no explanation or preamble.",
			Messages:    messages,
			MaxTokens:   1536,
			Temperature: 0,
		})
		if err != nil {
			return nil, fmt.Errorf("LLM call: %w", err)
		}

		content = strings.TrimSpace(resp.Content)
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
				messages = append(messages,
					llm.Message{Role: "assistant", Content: resp.Content},
					llm.Message{Role: "user", Content: fmt.Sprintf("That was not valid JSON array: %v\n\nPlease respond with ONLY the JSON array of 3 options, no text before or after.", err)},
				)
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

// discoverNewTasks checks tracked items for new open issues not already in the
// autopilot task list and adds them. Returns the number of new tasks added.
func (s *Supervisor) discoverNewTasks(ctx context.Context) int {
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
	ghClient := ghpkg.NewClient(s.ghToken)

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
			Dependencies: "[]", // No deps — new tasks start unblocked.
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
			s.emitEvent("discovered", fmt.Sprintf("New manual task #%d: %s (watching for completion)", t.IssueNumber, t.IssueTitle), nil)
		} else {
			s.emitEvent("discovered", fmt.Sprintf("New task #%d: %s", t.IssueNumber, t.IssueTitle), nil)
		}
	}

	// Warn that the dependency graph (built once during Prepare) may be stale.
	// Dynamically discovered tasks are queued without deps, but they could
	// affect ordering of already-queued tasks. The user can stop and re-launch
	// autopilot to rebuild the full dep graph with an LLM call.
	var nums []string
	for _, t := range newTasks {
		nums = append(nums, fmt.Sprintf("#%d", t.IssueNumber))
	}
	s.emitEvent("warning", fmt.Sprintf("New tasks discovered (%s) — dependency graph may be stale. New tasks will run without dependency ordering.", strings.Join(nums, ", ")), nil)

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

	go s.runAgent(slotCtx, slotIdx, task)
}

func (s *Supervisor) runAgent(ctx context.Context, slotIdx int, task *db.AutopilotTask) {
	defer func() {
		s.mu.Lock()
		s.slots[slotIdx] = nil
		s.mu.Unlock()
		// Re-evaluate blocked tasks and fill slots with newly unblocked work.
		// Use parentCtx (not the slot ctx which may be cancelled by StopAgent).
		s.unblockSatisfiedTasks()
		s.fillSlots(s.parentCtx)
	}()

	home, _ := os.UserHomeDir()

	// Ensure directories exist.
	if err := os.MkdirAll(filepath.Dir(task.WorktreePath), 0755); err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to create worktree dir for #%d: %v", task.IssueNumber, err), task)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "bailed")
		return
	}
	if err := os.MkdirAll(filepath.Join(home, ".agent-minder", "agents"), 0755); err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to create agents dir for #%d: %v", task.IssueNumber, err), task)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "bailed")
		return
	}

	// Clean up stale branch from previous run if it exists.
	_ = gitpkg.DeleteBranch(s.repoDir, task.Branch)

	// Get base branch: use configured value if set, otherwise auto-detect.
	baseBranch := s.project.AutopilotBaseBranch
	if baseBranch == "" {
		baseBranch, _ = gitpkg.DefaultBranch(s.repoDir)
	}

	// Fetch latest from origin so the worktree starts from the latest base branch.
	if err := gitpkg.Fetch(s.repoDir); err != nil {
		s.emitEvent("warning", fmt.Sprintf("Fetch failed for #%d: %v (using cached ref)", task.IssueNumber, err), task)
	}

	// Create worktree from the latest remote base branch.
	if err := gitpkg.WorktreeAdd(s.repoDir, task.WorktreePath, task.Branch, "origin/"+baseBranch); err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to create worktree for #%d: %v", task.IssueNumber, err), task)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "bailed")
		return
	}

	s.emitEvent("started", fmt.Sprintf("Agent started on #%d: %s", task.IssueNumber, task.IssueTitle), task)

	// Open log file.
	logFile, err := os.Create(task.AgentLog)
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to open log for #%d: %v", task.IssueNumber, err), task)
		s.cleanup(task, true)
		return
	}
	defer func() { _ = logFile.Close() }()

	// Ensure an agent definition exists (repo → user → built-in fallback).
	agentDefSource, err := ensureAgentDef(task.WorktreePath)
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to resolve agent definition for #%d: %v", task.IssueNumber, err), task)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "bailed")
		s.cleanup(task, true)
		return
	}
	s.emitEvent("started", fmt.Sprintf("Agent def: %s", agentDefSource.Description()), task)
	debugLog("agent def resolved",
		"stage", "autopilot", "step", "agent-def",
		"issue", task.IssueNumber,
		"source", string(agentDefSource),
		"worktree", task.WorktreePath,
	)

	// Build claude command.
	maxTurns := s.project.AutopilotMaxTurns
	if maxTurns < 1 {
		maxTurns = 50
	}
	maxBudget := s.project.AutopilotMaxBudgetUSD
	if maxBudget <= 0 {
		maxBudget = 3.00
	}

	args := buildClaudeArgs(task, baseBranch, s.owner, s.repo, maxTurns, maxBudget)
	debugLog("claude command",
		"stage", "autopilot", "step", "launch",
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
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "bailed")
		s.cleanup(task, true)
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
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "bailed")
		s.cleanup(task, true)
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
		ghClient := ghpkg.NewClient(s.ghToken)
		ghClient.RemoveLabel(ctx, s.owner, s.repo, task.IssueNumber, "in-progress")
		_ = ghClient.AddLabel(ctx, s.owner, s.repo, task.IssueNumber, "needs-review")
		s.emitEvent("completed", fmt.Sprintf("Agent completed #%d — PR opened, awaiting review & merge", task.IssueNumber), task)
	case "failed":
		// Remove in-progress label since the agent is done.
		ghClient := ghpkg.NewClient(s.ghToken)
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

	// Cleanup: remove worktree, delete branch if bailed or failed.
	s.cleanup(task, status == "bailed" || status == "failed")
}

// checkReviewTasks checks if any tasks in "review" status have had their PRs merged.
// Returns the number of tasks promoted to "done".
func (s *Supervisor) checkReviewTasks(ctx context.Context) int {
	tasks, err := s.store.GetAutopilotTasks(s.project.ID)
	if err != nil {
		return 0
	}

	ghClient := ghpkg.NewClient(s.ghToken)
	promoted := 0

	for _, task := range tasks {
		if task.Status != "review" || task.PRNumber == 0 {
			continue
		}

		item, err := ghClient.FetchItem(ctx, s.owner, s.repo, task.PRNumber)
		if err != nil {
			continue
		}

		if item.State == "merged" || item.State == "closed" {
			_ = s.store.UpdateAutopilotTaskStatus(task.ID, "done")
			// Clean up the needs-review label.
			ghClient.RemoveLabel(ctx, s.owner, s.repo, task.IssueNumber, "needs-review")
			promoted++
			s.emitEvent("completed", fmt.Sprintf("PR #%d for issue #%d merged — dependents unblocked", task.PRNumber, task.IssueNumber), &task)
		}
	}

	return promoted
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

	ghClient := ghpkg.NewClient(s.ghToken)
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
		status, reason, detail := classifyOutcome(agentResult, s.project.AutopilotMaxTurns, s.project.AutopilotMaxBudgetUSD)
		if status == "failed" {
			_ = s.store.UpdateAutopilotTaskFailure(task.ID, reason, detail)
			debugLog("inspectOutcome: classified as failed",
				"issue", task.IssueNumber, "reason", reason)
			return "failed"
		}
	}

	// Check if a PR was opened for this branch.
	ghClient := ghpkg.NewClient(s.ghToken)
	pr, err := ghClient.FetchPRForBranch(ctx, s.owner, s.repo, task.Branch)
	if err == nil && pr != nil && pr.Number > 0 {
		_ = s.store.UpdateAutopilotTaskPR(task.ID, pr.Number)
		return "review" // awaiting human review & merge before dependents unblock
	}
	return "bailed"
}

func (s *Supervisor) cleanup(task *db.AutopilotTask, deleteBranch bool) {
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
func buildClaudeArgs(task *db.AutopilotTask, baseBranch, owner, repo string, maxTurns int, maxBudget float64) []string {
	prompt := renderTaskContext(task, baseBranch, owner, repo)
	return []string{
		"--agent", "autopilot",
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", strconv.Itoa(maxTurns),
		"--max-budget-usd", fmt.Sprintf("%.2f", maxBudget),
		"--dangerously-skip-permissions",
		prompt,
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
	messages := []llm.Message{{Role: "user", Content: prompt}}

	var options []DepOption
	var content string

	for attempt := 0; attempt < 2; attempt++ {
		resp, err := s.provider.Complete(ctx, &llm.Request{
			Model:       s.project.LLMAnalyzerModel,
			System:      "You are analyzing issue dependencies. Respond with ONLY valid JSON, no explanation or preamble.",
			Messages:    messages,
			MaxTokens:   1536,
			Temperature: 0,
		})
		if err != nil {
			return nil, fmt.Errorf("LLM call: %w", err)
		}

		content = strings.TrimSpace(resp.Content)
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
				messages = append(messages,
					llm.Message{Role: "assistant", Content: resp.Content},
					llm.Message{Role: "user", Content: fmt.Sprintf("That was not valid JSON array: %v\n\nPlease respond with ONLY the JSON array of 3 options, no text before or after.", err)},
				)
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
