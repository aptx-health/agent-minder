package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/onboarding"
)

// AgentDefSource identifies which tier provided the agent definition.
type AgentDefSource string

const (
	AgentDefRepo    AgentDefSource = "repo"
	AgentDefUser    AgentDefSource = "user"
	AgentDefBuiltIn AgentDefSource = "built-in"
)

// Description returns a human-readable description.
func (s AgentDefSource) Description() string {
	switch s {
	case AgentDefRepo:
		return "repo-level (.claude/agents/autopilot.md)"
	case AgentDefUser:
		return "user-level (~/.claude/agents/autopilot.md)"
	case AgentDefBuiltIn:
		return "built-in default"
	default:
		return string(s)
	}
}

// AgentName identifies which agent type to resolve.
type AgentName string

const (
	AgentAutopilot AgentName = "autopilot"
	AgentReviewer  AgentName = "reviewer"
)

// defaultAgentDef is the built-in fallback agent definition.
const defaultAgentDef = `---
name: autopilot
description: >
  Autonomous agent that implements GitHub issues in isolated git worktrees.
  Install in a repo's .claude/agents/ directory for project-specific guidance.
tools: Bash, Read, Edit, Write, Glob, Grep
---

You are an autonomous agent working on a GitHub issue in an isolated git worktree.
Your task context is provided in the user prompt.

## First steps
1. Label the issue in-progress using the gh command from your task context
2. Post a starting comment
3. Read the full issue with comments and any linked issues
4. Explore the codebase to understand the relevant code

## Pre-check: assess complexity
After exploring but BEFORE making changes, assess the task.
If the change requires modifying more than 8 files or significant architectural decisions,
bail with a detailed implementation plan as a comment.

## If you can complete the work
- Implement the changes
- Ensure tests pass
- Commit with "Fixes #<issue>" in the message
- Rebase onto the base branch before pushing
- Open a draft PR

## If you cannot proceed
- Post a comment explaining what you learned and what blocks you
- Add the "blocked" label
- Remove the "in-progress" label

## Constraints
- Only modify files within your worktree
- Do not keep retrying if stuck — bail early with good context
- Do not over-engineer
`

// defaultReviewerDef is the built-in reviewer agent definition.
const defaultReviewerDef = `---
name: reviewer
description: >
  Reviews PRs opened by autopilot agents. Checks for correctness,
  test coverage, and code quality.
tools: Bash, Read, Edit, Write, Glob, Grep
---

You are a code reviewer examining a PR opened by an automated agent.
Review context is provided in the user prompt.

## Review process
1. Read the PR diff and understand the changes
2. Check that the implementation matches the issue requirements
3. Run the test command if provided
4. Look for bugs, edge cases, and missing error handling
5. Assess risk level: low-risk, needs-testing, or suspect

## If changes are needed
- Make the fixes directly in the worktree
- Run tests to verify
- Commit and push

## Risk assessment
Rate the PR as one of:
- low-risk: Simple, correct, well-tested
- needs-testing: Looks correct but needs manual verification
- suspect: Has issues that need human review
`

// defaultAllowedTools is the default tool permission set.
var defaultAllowedTools = []string{
	"Bash(git:*)",
	"Bash(gh:*)",
	"Bash(npm:*)",
	"Bash(go:*)",
	"Bash(make:*)",
	"Read",
	"Edit",
	"Write",
	"Glob",
	"Grep",
}

// ensureAgentDef checks for an agent definition and installs the built-in fallback if needed.
func ensureAgentDef(worktreePath string) (AgentDefSource, error) {
	return ensureAgentDefByName(worktreePath, AgentAutopilot)
}

func ensureAgentDefByName(worktreePath string, name AgentName) (AgentDefSource, error) {
	filename := string(name) + ".md"

	// Check repo-level.
	repoPath := filepath.Join(worktreePath, ".claude", "agents", filename)
	if _, err := os.Stat(repoPath); err == nil {
		return AgentDefRepo, nil
	}

	// Check user-level.
	home, _ := os.UserHomeDir()
	userPath := filepath.Join(home, ".claude", "agents", filename)
	if _, err := os.Stat(userPath); err == nil {
		return AgentDefUser, nil
	}

	// Install built-in to worktree.
	def := defaultAgentDef
	if name == AgentReviewer {
		def = defaultReviewerDef
	}
	dir := filepath.Join(worktreePath, ".claude", "agents")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create agent dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(def), 0644); err != nil {
		return "", fmt.Errorf("write agent def: %w", err)
	}
	return AgentDefBuiltIn, nil
}

// resolveAllowedTools reads tools from onboarding.yaml or returns defaults.
func resolveAllowedTools(repoDir string) []string {
	f, err := onboarding.Parse(onboarding.FilePath(repoDir))
	if err != nil {
		return defaultAllowedTools
	}
	if len(f.Permissions.AllowedTools) == 0 {
		return defaultAllowedTools
	}
	return f.Permissions.AllowedTools
}

// toCliAllowedTools converts tool patterns to comma-separated CLI format.
func toCliAllowedTools(tools []string) string {
	converted := make([]string, len(tools))
	for i, t := range tools {
		converted[i] = onboarding.ToCliToolPattern(t)
	}
	return strings.Join(converted, ",")
}

// resolveTestCommand reads from onboarding.yaml or auto-detects.
func resolveTestCommand(repoDir string) string {
	f, err := onboarding.Parse(onboarding.FilePath(repoDir))
	if err == nil && f.Context.TestCommand != "" {
		return f.Context.TestCommand
	}
	return detectTestCommand(repoDir)
}

func detectTestCommand(repoDir string) string {
	if _, err := os.Stat(filepath.Join(repoDir, "go.mod")); err == nil {
		return "go test ./..."
	}
	if _, err := os.Stat(filepath.Join(repoDir, "package.json")); err == nil {
		return "npm test"
	}
	for _, marker := range []string{"pytest.ini", "setup.cfg", "pyproject.toml", "setup.py"} {
		if _, err := os.Stat(filepath.Join(repoDir, marker)); err == nil {
			return "pytest"
		}
	}
	if _, err := os.Stat(filepath.Join(repoDir, "Cargo.toml")); err == nil {
		return "cargo test"
	}
	if _, err := os.Stat(filepath.Join(repoDir, "Makefile")); err == nil {
		return "make test"
	}
	return ""
}

// relatedWork holds dep graph and sibling task context.
type relatedWork struct {
	depGraph     string
	siblingTasks []*db.Task
}

// renderRelatedWork formats dependency and sibling context as markdown.
func renderRelatedWork(task *db.Task, rw *relatedWork) string {
	if rw == nil {
		return ""
	}
	var b strings.Builder

	if rw.depGraph != "" {
		b.WriteString("## Dependency Graph\n\n")
		b.WriteString("```json\n")
		b.WriteString(rw.depGraph)
		b.WriteString("\n```\n\n")
	}

	if len(rw.siblingTasks) > 0 {
		b.WriteString("## Related Tasks\n\n")
		b.WriteString("| Issue | Title | Status |\n")
		b.WriteString("|-------|-------|--------|\n")
		for _, t := range rw.siblingTasks {
			if t.ID == task.ID {
				continue
			}
			fmt.Fprintf(&b, "| #%d | %s | %s |\n", t.IssueNumber, t.IssueTitle.String, t.Status)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// renderTaskContext builds the user prompt with task-specific context.
func renderTaskContext(task *db.Task, deploy *db.Deployment, baseBranch, testCommand string, rw *relatedWork, issueComments string) string {
	var b strings.Builder
	owner, repo := deploy.Owner, deploy.Repo

	fmt.Fprintf(&b, "## Task Context\n\n")
	fmt.Fprintf(&b, "**Issue:** #%d — %s\n", task.IssueNumber, task.IssueTitle.String)
	fmt.Fprintf(&b, "**Repository:** %s/%s\n\n", owner, repo)

	if task.IssueBody.Valid && task.IssueBody.String != "" {
		b.WriteString(task.IssueBody.String)
		b.WriteString("\n\n")
	}

	if issueComments != "" {
		b.WriteString("## Issue Discussion\n\n")
		b.WriteString(issueComments)
		b.WriteString("\n\n")
	}

	fmt.Fprintf(&b, "**Worktree:** %s\n", task.WorktreePath.String)
	fmt.Fprintf(&b, "**Branch:** %s (already checked out)\n", task.Branch.String)
	fmt.Fprintf(&b, "**Base branch:** %s\n\n", baseBranch)

	if testCommand != "" {
		fmt.Fprintf(&b, "## Test command\n\nRun tests: `%s`\n\n", testCommand)
	}

	if related := renderRelatedWork(task, rw); related != "" {
		b.WriteString(related)
	}

	fmt.Fprintf(&b, "## Commands for this task\n\n")
	fmt.Fprintf(&b, "Label in-progress: gh issue edit %d --add-label \"in-progress\" -R %s/%s\n", task.IssueNumber, owner, repo)
	fmt.Fprintf(&b, "Post starting comment: gh issue comment %d --body \"Agent starting work on this issue\" -R %s/%s\n", task.IssueNumber, owner, repo)
	fmt.Fprintf(&b, "Commit message must include: Fixes #%d\n", task.IssueNumber)
	fmt.Fprintf(&b, "Rebase before push:\n")
	fmt.Fprintf(&b, "  git fetch origin %s\n", baseBranch)
	fmt.Fprintf(&b, "  git rebase origin/%s\n", baseBranch)
	fmt.Fprintf(&b, "Draft PR: gh pr create --draft --base %s -R %s/%s\n", baseBranch, owner, repo)

	return b.String()
}

// renderResumeTaskContext builds a continuation prompt for resuming work.
func renderResumeTaskContext(task *db.Task, deploy *db.Deployment, baseBranch, testCommand string, rw *relatedWork, issueComments string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Resuming Previous Work\n\n")
	fmt.Fprintf(&b, "You are resuming work on issue #%d in an existing worktree.\n", task.IssueNumber)
	if task.FailureReason.Valid && task.FailureReason.String != "" {
		fmt.Fprintf(&b, "Previous attempt ended due to: **%s**", task.FailureReason.String)
		if task.FailureDetail.Valid && task.FailureDetail.String != "" {
			fmt.Fprintf(&b, " (%s)", task.FailureDetail.String)
		}
		b.WriteString(".\n")
	}
	b.WriteString("\nReview prior work, continue from where it left off, and open a PR when ready.\n\n")
	b.WriteString(renderTaskContext(task, deploy, baseBranch, testCommand, rw, issueComments))

	return b.String()
}

// renderReviewTaskContext builds a review-specific prompt.
func renderReviewTaskContext(task *db.Task, deploy *db.Deployment, baseBranch, testCommand string, rw *relatedWork, issueComments string) string {
	var b strings.Builder
	owner, repo := deploy.Owner, deploy.Repo

	fmt.Fprintf(&b, "## Review Context\n\n")
	fmt.Fprintf(&b, "**PR:** #%d\n", task.PRNumber.Int64)
	fmt.Fprintf(&b, "**Issue:** #%d — %s\n", task.IssueNumber, task.IssueTitle.String)
	fmt.Fprintf(&b, "**Repository:** %s/%s\n", owner, repo)
	fmt.Fprintf(&b, "**Branch:** %s\n", task.Branch.String)
	fmt.Fprintf(&b, "**Base branch:** %s\n", baseBranch)
	fmt.Fprintf(&b, "**Worktree:** %s\n\n", task.WorktreePath.String)

	if task.IssueBody.Valid && task.IssueBody.String != "" {
		b.WriteString("## Issue Description\n\n")
		b.WriteString(task.IssueBody.String)
		b.WriteString("\n\n")
	}

	if issueComments != "" {
		b.WriteString("## Issue Discussion\n\n")
		b.WriteString(issueComments)
		b.WriteString("\n\n")
	}

	if testCommand != "" {
		fmt.Fprintf(&b, "## Test command\n\nRun tests: `%s`\n\n", testCommand)
		b.WriteString("**IMPORTANT:** You MUST run this test command after making any fixes.\n\n")
	}

	if related := renderRelatedWork(task, rw); related != "" {
		b.WriteString(related)
	}

	fmt.Fprintf(&b, "## Commands for this review\n\n")
	fmt.Fprintf(&b, "View PR diff: gh pr diff %d -R %s/%s\n", task.PRNumber.Int64, owner, repo)
	fmt.Fprintf(&b, "View PR: gh pr view %d -R %s/%s\n", task.PRNumber.Int64, owner, repo)
	fmt.Fprintf(&b, "Rebase before push:\n")
	fmt.Fprintf(&b, "  git fetch origin %s\n", baseBranch)
	fmt.Fprintf(&b, "  git rebase origin/%s\n", baseBranch)

	return b.String()
}

// buildClaudeArgs constructs CLI arguments for a fresh agent run.
func buildClaudeArgs(task *db.Task, deploy *db.Deployment, baseBranch, testCommand string, allowedTools []string, rw *relatedWork, issueComments, lessonsPrompt string) []string {
	prompt := renderTaskContext(task, deploy, baseBranch, testCommand, rw, issueComments)
	args := []string{
		"--agent", "autopilot",
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", strconv.Itoa(task.EffectiveMaxTurns(deploy)),
		"--max-budget-usd", fmt.Sprintf("%.2f", task.EffectiveMaxBudget(deploy)),
		"--allowedTools", toCliAllowedTools(allowedTools),
	}
	if lessonsPrompt != "" {
		args = append(args, "--append-system-prompt", lessonsPrompt)
	}
	args = append(args, "--", prompt)
	return args
}

// buildResumeClaudeArgs constructs CLI arguments for resuming an agent.
func buildResumeClaudeArgs(task *db.Task, deploy *db.Deployment, baseBranch, testCommand string, allowedTools []string, rw *relatedWork, issueComments, lessonsPrompt string) []string {
	prompt := renderResumeTaskContext(task, deploy, baseBranch, testCommand, rw, issueComments)
	args := []string{
		"--agent", "autopilot",
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", strconv.Itoa(task.EffectiveMaxTurns(deploy)),
		"--max-budget-usd", fmt.Sprintf("%.2f", task.EffectiveMaxBudget(deploy)),
		"--allowedTools", toCliAllowedTools(allowedTools),
		"--resume",
	}
	if lessonsPrompt != "" {
		args = append(args, "--append-system-prompt", lessonsPrompt)
	}
	args = append(args, "--", prompt)
	return args
}

// buildReviewClaudeArgs constructs CLI arguments for the review agent.
func buildReviewClaudeArgs(task *db.Task, deploy *db.Deployment, baseBranch, testCommand string, allowedTools []string, rw *relatedWork, issueComments string) []string {
	prompt := renderReviewTaskContext(task, deploy, baseBranch, testCommand, rw, issueComments)
	maxTurns := deploy.MaxTurns
	maxBudget := deploy.MaxBudgetUSD
	if deploy.ReviewMaxTurns.Valid {
		maxTurns = int(deploy.ReviewMaxTurns.Int64)
	}
	if deploy.ReviewMaxBudget.Valid {
		maxBudget = deploy.ReviewMaxBudget.Float64
	}
	return []string{
		"--agent", "reviewer",
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", strconv.Itoa(maxTurns),
		"--max-budget-usd", fmt.Sprintf("%.2f", maxBudget),
		"--allowedTools", toCliAllowedTools(allowedTools),
		"--", prompt,
	}
}
