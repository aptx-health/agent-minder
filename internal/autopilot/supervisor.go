// Package autopilot manages automated Claude Code agents that work on GitHub issues.
package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
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

// Event is emitted by the supervisor for the TUI to consume.
type Event struct {
	Time    time.Time
	Type    string // "started", "completed", "bailed", "error", "stopped"
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
}

type slotState struct {
	task      *db.AutopilotTask
	startedAt time.Time
	cmd       *exec.Cmd
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

	mu     sync.Mutex
	slots  []*slotState // len == maxAgents; nil = idle
	active bool
	events chan Event
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a new Supervisor.
func New(store *db.Store, project *db.Project, provider llm.Provider, repoDir, owner, repo, ghToken string) *Supervisor {
	maxAgents := project.AutopilotMaxAgents
	if maxAgents < 1 {
		maxAgents = 3
	}
	return &Supervisor{
		store:   store,
		project: project,
		provider: provider,
		repoDir: repoDir,
		owner:   owner,
		repo:    repo,
		ghToken: ghToken,
		slots:   make([]*slotState, maxAgents),
		events:  make(chan Event, 64),
	}
}

// Events returns the channel of events for the TUI.
func (s *Supervisor) Events() <-chan Event {
	return s.events
}

// IsActive returns whether the supervisor is currently running.
func (s *Supervisor) IsActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Prepare fetches tracked issues, creates autopilot tasks, and builds a dependency graph.
// Always starts fresh — clears previous tasks, cleans up orphaned worktrees.
func (s *Supervisor) Prepare(ctx context.Context) (total, unblocked int, err error) {
	// Clean up any leftovers from a previous run.
	if err := s.store.ClearAutopilotTasks(s.project.ID); err != nil {
		return 0, 0, fmt.Errorf("clear autopilot tasks: %w", err)
	}
	s.cleanOrphanedWorktrees()

	// Convert tracked items to autopilot tasks.
	tasks, err := s.convertTrackedItems(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("convert tracked items: %w", err)
	}
	if len(tasks) == 0 {
		return 0, 0, nil
	}

	// Build dependency graph.
	if err := s.buildDependencyGraph(ctx, tasks); err != nil {
		s.emitEvent("error", fmt.Sprintf("Dependency graph failed: %v — falling back to sequential order", err), nil)
		s.setSequentialDeps(tasks)
	}

	unblockedTasks, err := s.store.QueuedUnblockedTasks(s.project.ID)
	if err != nil {
		return len(tasks), 0, fmt.Errorf("count unblocked: %w", err)
	}

	return len(tasks), len(unblockedTasks), nil
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
				s.emitEvent("stopped", "Autopilot cancelled", nil)
				return
			case <-reviewCheckTicker.C:
				promoted := s.checkReviewTasks(ctx)
				if promoted > 0 {
					s.fillSlots(ctx)
				}
			case <-discoveryTicker.C:
				if s.hasIdleSlot() {
					added := s.discoverNewTasks(ctx)
					if added > 0 {
						s.fillSlots(ctx)
					}
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

			// Check if there's any work left (running agents, queued tasks, or tasks in review).
			hasWork := anyRunning
			if !hasWork {
				tasks, _ := s.store.GetAutopilotTasks(s.project.ID)
				for _, t := range tasks {
					if t.Status == "queued" || t.Status == "review" {
						hasWork = true
						break
					}
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
		s.emitEvent("stopped", "All agents finished", nil)
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
			}
		} else {
			infos[i] = SlotInfo{
				SlotNum:     i + 1,
				IssueNumber: slot.task.IssueNumber,
				IssueTitle:  slot.task.IssueTitle,
				Branch:      slot.task.Branch,
				RunningFor:  time.Since(slot.startedAt),
				Status:      "running",
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
			fmt.Fprintf(&b, "- Slot %d: #%d %s (%s, running %s)\n",
				i+1, slot.task.IssueNumber, slot.task.IssueTitle, slot.task.Branch, elapsed)
		}
	}

	// Summary counts.
	tasks, _ := s.store.GetAutopilotTasks(s.project.ID)
	var queued, running, review, done, bailed int
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
		}
	}
	fmt.Fprintf(&b, "\nTask summary: %d queued, %d running, %d in review, %d done, %d bailed\n", queued, running, review, done, bailed)

	return b.String()
}

// convertTrackedItems reads the user's already-tracked issues and creates autopilot tasks.
func (s *Supervisor) convertTrackedItems(ctx context.Context) ([]*db.AutopilotTask, error) {
	items, err := s.store.GetTrackedItems(s.project.ID)
	if err != nil {
		return nil, fmt.Errorf("get tracked items: %w", err)
	}

	// Filter out issues with the skip label, non-open items, and PRs.
	skipLabel := s.project.AutopilotSkipLabel
	if skipLabel == "" {
		skipLabel = "no-agent"
	}

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
		if hasLabel(liveStatus.Labels, skipLabel) {
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

func (s *Supervisor) buildDependencyGraph(ctx context.Context, tasks []*db.AutopilotTask) error {
	if len(tasks) <= 1 {
		return nil // No dependencies possible with 0-1 tasks.
	}

	// Build task issue numbers set for quick lookup.
	taskIssues := make(map[int]bool, len(tasks))
	for _, t := range tasks {
		taskIssues[t.IssueNumber] = true
	}

	// Build issue list for the LLM — include tasks being worked on.
	var issueList strings.Builder
	issueList.WriteString("## Issues to be worked on by agents:\n")
	for _, t := range tasks {
		fmt.Fprintf(&issueList, "Issue #%d: %s\n", t.IssueNumber, t.IssueTitle)
		if t.IssueBody != "" {
			body := t.IssueBody
			if len(body) > 200 {
				body = body[:200] + "..."
			}
			fmt.Fprintf(&issueList, "  %s\n", body)
		}
	}

	// Include other tracked issues as context (skipped, closed, etc.)
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

	prompt := fmt.Sprintf(`Analyze these GitHub issues and determine dependencies between them.
A dependency means issue B cannot start until issue A is completed (e.g., B builds on A's infrastructure or schema changes).

Dependencies can include issues from BOTH sections — an agent task can depend on a non-agent issue if that issue's work must be done first.

%s

Respond with ONLY a JSON object. Keys are issue numbers (only for issues being worked on by agents), values are arrays of integer issue numbers that must complete first. Issues with no dependencies get an empty array.

IMPORTANT: Use integer values in the arrays, not strings.
Example: {"42": [], "38": [42], "15": [42, 38]}

Be conservative — only add a dependency if the work truly cannot proceed without the other issue being done first.`, issueList.String())

	messages := []llm.Message{{Role: "user", Content: prompt}}

	var rawGraph map[string]json.RawMessage
	var content string

	for attempt := 0; attempt < 2; attempt++ {
		resp, err := s.provider.Complete(ctx, &llm.Request{
			Model:       s.project.LLMAnalyzerModel,
			System:      "You are analyzing issue dependencies. Respond with ONLY valid JSON, no explanation or preamble.",
			Messages:    messages,
			MaxTokens:   512,
			Temperature: 0,
		})
		if err != nil {
			return fmt.Errorf("LLM call: %w", err)
		}

		content = strings.TrimSpace(resp.Content)
		// Strip markdown fencing if present.
		if strings.HasPrefix(content, "```") {
			lines := strings.Split(content, "\n")
			if len(lines) > 2 {
				content = strings.Join(lines[1:len(lines)-1], "\n")
			}
		}
		// Extract JSON object — trim any leading/trailing non-JSON text.
		if start := strings.Index(content, "{"); start >= 0 {
			if end := strings.LastIndex(content, "}"); end > start {
				content = content[start : end+1]
			}
		}

		if err := json.Unmarshal([]byte(content), &rawGraph); err != nil {
			if attempt == 0 {
				// Retry with error feedback.
				messages = append(messages,
					llm.Message{Role: "assistant", Content: resp.Content},
					llm.Message{Role: "user", Content: fmt.Sprintf("That was not valid JSON: %v\n\nPlease respond with ONLY the JSON object, no text before or after.", err)},
				)
				continue
			}
			return fmt.Errorf("parse dep graph: %w", err)
		}
		break
	}

	// Update each task's dependencies.
	for _, t := range tasks {
		key := strconv.Itoa(t.IssueNumber)
		rawDeps, ok := rawGraph[key]
		if !ok {
			continue
		}

		// Try []int first, then []string, then []any.
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
		}
	}

	return nil
}

// setSequentialDeps creates a simple chain: each task depends on the previous one (by issue number).
// This is the safe fallback when the LLM dep graph fails — tasks run one at a time in order.
func (s *Supervisor) setSequentialDeps(tasks []*db.AutopilotTask) {
	// Tasks are already sorted by issue number from the DB query.
	for i := 1; i < len(tasks); i++ {
		deps := []int{tasks[i-1].IssueNumber}
		depsJSON, _ := json.Marshal(deps)
		_ = s.store.UpdateAutopilotTaskDeps(tasks[i].ID, string(depsJSON))
	}
}

func (s *Supervisor) fillSlots(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	skipLabel := s.project.AutopilotSkipLabel
	if skipLabel == "" {
		skipLabel = "no-agent"
	}
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
		if hasLabel(liveStatus.Labels, skipLabel) {
			continue
		}

		body := ""
		content, err := ghClient.FetchItemContent(ctx, item.Owner, item.Repo, item.Number, "issue")
		if err == nil {
			body = content.Body
		}

		newTasks = append(newTasks, &db.AutopilotTask{
			ProjectID:    s.project.ID,
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
		s.emitEvent("discovered", fmt.Sprintf("New task #%d: %s", t.IssueNumber, t.IssueTitle), nil)
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

	s.slots[slotIdx] = &slotState{
		task:      task,
		startedAt: time.Now(),
	}

	go s.runAgent(ctx, slotIdx, task)
}

func (s *Supervisor) runAgent(ctx context.Context, slotIdx int, task *db.AutopilotTask) {
	defer func() {
		s.mu.Lock()
		s.slots[slotIdx] = nil
		s.mu.Unlock()
		// Try to fill slots with newly unblocked work.
		s.fillSlots(ctx)
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

	// Create worktree.
	if err := gitpkg.WorktreeAdd(s.repoDir, task.WorktreePath, task.Branch); err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to create worktree for #%d: %v", task.IssueNumber, err), task)
		_ = s.store.UpdateAutopilotTaskStatus(task.ID, "bailed")
		return
	}

	s.emitEvent("started", fmt.Sprintf("Agent started on #%d: %s", task.IssueNumber, task.IssueTitle), task)

	// Get base branch.
	baseBranch, _ := gitpkg.DefaultBranch(s.repoDir)

	// Build prompt.
	prompt := renderPrompt(task, baseBranch, s.owner, s.repo)

	// Open log file.
	logFile, err := os.Create(task.AgentLog)
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Failed to open log for #%d: %v", task.IssueNumber, err), task)
		s.cleanup(task, true)
		return
	}
	defer func() { _ = logFile.Close() }()

	// Build claude command.
	maxTurns := s.project.AutopilotMaxTurns
	if maxTurns < 1 {
		maxTurns = 50
	}
	maxBudget := s.project.AutopilotMaxBudgetUSD
	if maxBudget <= 0 {
		maxBudget = 3.00
	}

	cmd := exec.CommandContext(ctx, "claude",
		"-p",
		"--max-turns", strconv.Itoa(maxTurns),
		"--max-budget-usd", fmt.Sprintf("%.2f", maxBudget),
		"--dangerously-skip-permissions",
		prompt,
	)
	cmd.Dir = task.WorktreePath
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	// Set GITHUB_TOKEN for gh CLI calls within the agent.
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+s.ghToken)

	s.mu.Lock()
	if s.slots[slotIdx] != nil {
		s.slots[slotIdx].cmd = cmd
	}
	s.mu.Unlock()

	// Run the agent.
	err = cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	// Inspect outcome.
	status := s.inspectOutcome(ctx, task, exitCode)
	_ = s.store.UpdateAutopilotTaskStatus(task.ID, status)

	if status == "review" {
		// Swap in-progress → needs-review label on the issue.
		ghClient := ghpkg.NewClient(s.ghToken)
		ghClient.RemoveLabel(ctx, s.owner, s.repo, task.IssueNumber, "in-progress")
		_ = ghClient.AddLabel(ctx, s.owner, s.repo, task.IssueNumber, "needs-review")
		s.emitEvent("completed", fmt.Sprintf("Agent completed #%d — PR opened, awaiting review & merge", task.IssueNumber), task)
	} else {
		s.emitEvent("bailed", fmt.Sprintf("Agent bailed on #%d (exit code %d)", task.IssueNumber, exitCode), task)
	}

	// Cleanup: remove worktree, delete branch only if bailed.
	s.cleanup(task, status == "bailed")
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

func (s *Supervisor) inspectOutcome(ctx context.Context, task *db.AutopilotTask, exitCode int) string {
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
