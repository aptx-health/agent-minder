package supervisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/lesson"
)

// spawnReviewAgent launches a review agent for a task that has a PR.
// It runs in the task's existing worktree and produces a risk assessment.
func (s *Supervisor) spawnReviewAgent(ctx context.Context, slotIdx int, task *db.Task) {
	if !s.deploy.ReviewEnabled {
		return
	}
	if !task.PRNumber.Valid {
		return
	}

	// Mark as reviewing.
	_ = s.store.UpdateTaskStatus(task.ID, db.StatusReviewing)

	slotCtx, slotCancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.slots[slotIdx] = &slotState{
		task:       task,
		startedAt:  time.Now(),
		cancelFunc: slotCancel,
	}
	s.mu.Unlock()

	go s.runReviewAgent(slotCtx, slotIdx, task)
}

func (s *Supervisor) runReviewAgent(ctx context.Context, slotIdx int, task *db.Task) {
	defer func() {
		s.mu.Lock()
		s.slots[slotIdx] = nil
		s.mu.Unlock()
		s.fillSlots(s.parentCtx)
	}()

	worktreePath := task.WorktreePath.String
	if worktreePath == "" {
		s.emitEvent("error", fmt.Sprintf("No worktree for review of #%d", task.IssueNumber), task.ID)
		_ = s.store.UpdateTaskStatus(task.ID, db.StatusReview) // revert
		return
	}

	// Ensure reviewer agent def exists.
	_, err := ensureAgentDefByName(worktreePath, AgentReviewer)
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Reviewer agent def error: %v", err), task.ID)
		_ = s.store.UpdateTaskStatus(task.ID, db.StatusReview)
		return
	}

	s.emitEvent("started", fmt.Sprintf("Review agent started on #%d (PR #%d)", task.IssueNumber, task.PRNumber.Int64), task.ID)

	// Build review args.
	baseBranch := s.deploy.BaseBranch
	testCommand := resolveTestCommand(s.repoDir)
	allowedTools := resolveAllowedTools(s.repoDir)

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

	issueComments := s.fetchIssueComments(ctx, task)

	args := buildReviewClaudeArgs(task, s.deploy, baseBranch, testCommand, allowedTools, rw, issueComments)

	// Open log file (append to existing agent log).
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".agent-minder", "agents")
	_ = os.MkdirAll(logDir, 0755)
	logPath := task.AgentLog.String
	if logPath == "" {
		logPath = filepath.Join(logDir, fmt.Sprintf("%s-issue-%d.log", s.deploy.ID, task.IssueNumber))
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		s.emitEvent("error", fmt.Sprintf("Review log error: %v", err), task.ID)
		_ = s.store.UpdateTaskStatus(task.ID, db.StatusReview)
		return
	}
	defer func() { _ = logFile.Close() }()

	// Write separator in log.
	_, _ = fmt.Fprintf(logFile, "\n\n--- REVIEW AGENT ---\n\n")

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = worktreePath
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+s.ghToken)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = s.store.UpdateTaskStatus(task.ID, db.StatusReview)
		return
	}

	s.mu.Lock()
	if s.slots[slotIdx] != nil {
		s.slots[slotIdx].cmd = cmd
	}
	s.mu.Unlock()

	if err := cmd.Start(); err != nil {
		s.emitEvent("error", fmt.Sprintf("Review agent start failed: %v", err), task.ID)
		_ = s.store.UpdateTaskStatus(task.ID, db.StatusReview)
		return
	}

	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanStream(stdout, logFile, slotIdx, s)
	}()

	_ = cmd.Wait()
	<-scanDone

	// Parse review outcome.
	reviewResult := parseReviewFromLog(logPath)

	// Determine risk level.
	risk := "needs-testing" // default
	if strings.Contains(strings.ToLower(reviewResult), "low-risk") {
		risk = "low-risk"
	} else if strings.Contains(strings.ToLower(reviewResult), "suspect") {
		risk = "suspect"
	}

	_ = s.store.UpdateTaskReview(task.ID, risk, 0)
	_ = s.store.UpdateTaskStatus(task.ID, db.StatusReviewed)

	s.emitEvent("completed", fmt.Sprintf("Review of #%d complete (risk: %s)", task.IssueNumber, risk), task.ID)

	// Auto-capture lessons from review findings.
	if reviewResult != "" {
		captured, _ := lesson.CaptureFromReview(s.store, s.owner, s.repo, reviewResult)
		if len(captured) > 0 {
			s.emitEvent("info", fmt.Sprintf("Captured %d lessons from review of #%d", len(captured), task.IssueNumber), task.ID)
		}
	}

	// Auto-merge if configured and low-risk.
	if s.deploy.AutoMerge && risk == "low-risk" && task.PRNumber.Valid {
		ghClient := s.newGHClient()
		if err := ghClient.MergePR(ctx, s.owner, s.repo, int(task.PRNumber.Int64), "merge", ""); err == nil {
			_ = s.store.CompleteTask(task.ID, db.StatusDone)
			ghClient.RemoveLabel(ctx, s.owner, s.repo, task.IssueNumber, "needs-review")
			s.emitEvent("completed", fmt.Sprintf("Auto-merged PR #%d for #%d", task.PRNumber.Int64, task.IssueNumber), task.ID)
		}
	}
}

// parseReviewFromLog extracts the review agent's output from the log.
func parseReviewFromLog(logPath string) string {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}

	content := string(data)
	// Find the review agent section.
	idx := strings.LastIndex(content, "--- REVIEW AGENT ---")
	if idx >= 0 {
		content = content[idx:]
	}

	// Extract the last substantial text block (the review summary).
	lines := strings.Split(content, "\n")
	var result []string
	for _, line := range lines {
		// Skip JSON stream events.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "{") {
			continue
		}
		result = append(result, line)
	}

	if len(result) > 50 {
		result = result[len(result)-50:]
	}
	return strings.Join(result, "\n")
}

// enhancedCheckReviewTasks extends checkReviewTasks to also spawn review agents.
// Called from the main supervisor loop.
func (s *Supervisor) enhancedCheckReviewTasks(ctx context.Context) {
	tasks, err := s.store.GetTasks(s.deploy.ID)
	if err != nil {
		return
	}

	ghClient := s.newGHClient()

	for _, t := range tasks {
		switch t.Status {
		case db.StatusReview:
			// Check if we have a free slot to spawn a review agent.
			if s.deploy.ReviewEnabled && s.hasIdleSlot() {
				s.mu.Lock()
				for i, slot := range s.slots {
					if slot == nil {
						s.mu.Unlock()
						s.spawnReviewAgent(ctx, i, t)
						s.mu.Lock()
						break
					}
				}
				s.mu.Unlock()
			}

			// Also check if PR was merged (review might not be needed).
			if t.PRNumber.Valid {
				merged, err := ghClient.IsPRMerged(ctx, s.owner, s.repo, int(t.PRNumber.Int64))
				if err == nil && merged {
					_ = s.store.CompleteTask(t.ID, db.StatusDone)
					ghClient.RemoveLabel(ctx, s.owner, s.repo, t.IssueNumber, "needs-review")
					s.emitEvent("completed", fmt.Sprintf("PR #%d merged — #%d done", t.PRNumber.Int64, t.IssueNumber), t.ID)
				}
			}

		case db.StatusReviewed:
			// Check if PR was merged.
			if t.PRNumber.Valid {
				merged, err := ghClient.IsPRMerged(ctx, s.owner, s.repo, int(t.PRNumber.Int64))
				if err == nil && merged {
					_ = s.store.CompleteTask(t.ID, db.StatusDone)
					ghClient.RemoveLabel(ctx, s.owner, s.repo, t.IssueNumber, "needs-review")
					s.emitEvent("completed", fmt.Sprintf("PR #%d merged — #%d done", t.PRNumber.Int64, t.IssueNumber), t.ID)
				}
			}
		}
	}
}
