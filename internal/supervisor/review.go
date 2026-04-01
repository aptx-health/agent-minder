package supervisor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aptx-health/agent-minder/internal/claudecli"
	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/lesson"
)

// ReviewAssessment is the structured JSON output from the review extraction call.
type ReviewAssessment struct {
	Risk    string   `json:"risk"`    // "low-risk", "needs-testing", or "suspect"
	Summary string   `json:"summary"` // One-line summary of the review
	Lessons []string `json:"lessons"` // Actionable lessons for future agents
	Issues  []string `json:"issues"`  // Specific issues found (empty if clean)
}

var reviewAssessmentSchema = `{
	"type": "object",
	"properties": {
		"risk": {
			"type": "string",
			"enum": ["low-risk", "needs-testing", "suspect"],
			"description": "Overall risk assessment of the PR"
		},
		"summary": {
			"type": "string",
			"description": "One-line summary of the review findings"
		},
		"lessons": {
			"type": "array",
			"items": {"type": "string"},
			"description": "Actionable lessons that future agents should follow to avoid the issues found. Each lesson should be a clear, imperative statement. Empty array if no lessons."
		},
		"issues": {
			"type": "array",
			"items": {"type": "string"},
			"description": "Specific issues found in the PR. Empty array if the PR is clean."
		}
	},
	"required": ["risk", "summary", "lessons", "issues"]
}`

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

	// Extract structured review assessment via a cheap LLM call.
	assessment := s.extractReviewAssessment(ctx, task, logPath)

	risk := assessment.Risk
	if risk == "" {
		risk = "needs-testing"
	}

	_ = s.store.UpdateTaskReview(task.ID, risk, 0)
	_ = s.store.UpdateTaskStatus(task.ID, db.StatusReviewed)

	s.emitEvent("completed", fmt.Sprintf("Review of #%d complete (risk: %s)", task.IssueNumber, risk), task.ID)
	if assessment.Summary != "" {
		s.emitEvent("info", fmt.Sprintf("Review: %s", assessment.Summary), task.ID)
	}

	// Auto-capture lessons from structured findings.
	if len(assessment.Lessons) > 0 {
		captured := s.captureLessonsFromAssessment(assessment)
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

// extractReviewAssessment runs a cheap structured LLM call to produce a JSON assessment
// from the review agent's log output.
func (s *Supervisor) extractReviewAssessment(ctx context.Context, task *db.Task, logPath string) ReviewAssessment {
	// Read the review portion of the log.
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ReviewAssessment{Risk: "needs-testing"}
	}

	content := string(data)
	// Trim to just the review section if the marker exists.
	if idx := strings.LastIndex(content, "--- REVIEW AGENT ---"); idx >= 0 {
		content = content[idx:]
	}
	// Truncate to last 8000 chars to stay within token limits.
	if len(content) > 8000 {
		content = content[len(content)-8000:]
	}

	prompt := fmt.Sprintf(`Analyze this review agent log for PR #%d (issue #%d) and produce a structured assessment.

The review agent examined the PR, possibly ran tests, and may have made fixes.
Extract the risk level, a one-line summary, any actionable lessons for future agents,
and specific issues found.

Review agent log:
---
%s
---

Produce your assessment as JSON.`, task.PRNumber.Int64, task.IssueNumber, content)

	completer := claudecli.NewCLICompleter()
	resp, err := completer.Complete(ctx, &claudecli.Request{
		SystemPrompt: "You extract structured review assessments from agent logs. Be concise and actionable.",
		Prompt:       prompt,
		Model:        "haiku",
		JSONSchema:   reviewAssessmentSchema,
		DisableTools: true,
	})
	if err != nil {
		debugLog("review assessment extraction failed", "issue", task.IssueNumber, "error", err.Error())
		return ReviewAssessment{Risk: "needs-testing"}
	}

	var assessment ReviewAssessment
	if err := json.Unmarshal([]byte(resp.Content()), &assessment); err != nil {
		debugLog("review assessment parse failed", "issue", task.IssueNumber, "error", err.Error())
		return ReviewAssessment{Risk: "needs-testing"}
	}

	debugLog("review assessment extracted",
		"issue", task.IssueNumber,
		"risk", assessment.Risk,
		"lessons", len(assessment.Lessons),
		"issues", len(assessment.Issues),
	)
	return assessment
}

// captureLessonsFromAssessment creates lesson records from the structured review assessment.
func (s *Supervisor) captureLessonsFromAssessment(assessment ReviewAssessment) []*db.Lesson {
	var created []*db.Lesson

	scope := s.owner + "/" + s.repo
	existing, _ := s.store.GetActiveLessons(scope)

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
		if err := s.store.CreateLesson(l); err == nil {
			created = append(created, l)
			// Add to existing so subsequent checks in this batch see it.
			existing = append(existing, l)
		}
	}

	return created
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
