package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aptx-health/agent-minder/internal/db"
	gitpkg "github.com/aptx-health/agent-minder/internal/git"
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
mode: reactive
output: pr
stages:
  - name: implement
  - name: review
    agent: reviewer
    on_failure: skip
    retries: 1
context:
  - issue
  - repo_info
  - lessons
  - sibling_jobs
  - dep_graph
---

You are an autonomous agent working on a GitHub issue in an isolated git worktree.
Your task context is provided in the user prompt.

## First steps
1. Label the issue in-progress using the gh command from your task context
2. Post a starting comment
3. Read the full issue with comments and any linked issues
4. Explore the codebase to understand the relevant code

## Pre-check: assess whether you can complete this
After exploring but BEFORE making changes, decide if you can complete the work confidently.

**Proceed if:**
- You have a clear mental model of what needs to change and why
- The changes are mechanical or follow clear patterns (even across many files)
- You can write automated tests to verify correctness

**Bail if:**
- You don't understand the architecture well enough to be confident in your changes
- The issue needs design decisions that aren't specified (ambiguous requirements)
- Implementation requires extensive manual or interactive testing (UI, hardware, running services)
- The change has unclear blast radius across the codebase

File count alone is NOT a reason to bail — a 20-file rename is simpler than a 3-file architecture change.

## If you can complete the work
- Implement the changes
- Ensure tests pass
- Commit with "Fixes #<issue>" in the message
- Rebase onto the base branch before pushing
- Open a draft PR

## If you cannot proceed — structured bail
When you decide to bail, do the following in order:

1. Write your bail report to a file, then post it as an issue comment:
   - Write the report to /tmp/bail-report.md using the Write tool
   - Post with: gh issue comment <number> --body-file /tmp/bail-report.md
   - This avoids shell escaping issues with inline --body
2. Update labels: gh issue edit <number> --add-label blocked --remove-label in-progress
3. Commit any partial work you've done (even without a PR) so future attempts have context
4. As your FINAL message, output a JSON block wrapped in <bail-report> tags (this MUST be
   in your last text response — the orchestrator parses it from your output):

<bail-report>
{
  "reason": "Why you're bailing — be specific about what's blocking you",
  "files_examined": ["list", "of", "files", "you", "explored"],
  "plan": "Step-by-step implementation plan for the next agent or human",
  "sub_issues": ["Optional: if the issue should be decomposed, suggest 2-4 sub-issues with clear scope"],
  "complexity": "small | medium | large | epic"
}
</bail-report>

IMPORTANT: The <bail-report> tags must NOT be inside a code fence or any other wrapper.
Output them as raw text so the orchestrator can parse them.

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
mode: reactive
output: pr
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

	// Install from template registry, falling back to built-in constants.
	for _, tmpl := range AgentTemplates() {
		if tmpl.Name == string(name) {
			_, err := InstallAgentDef(worktreePath, tmpl)
			if err != nil {
				return "", err
			}
			return AgentDefBuiltIn, nil
		}
	}

	// Legacy fallback for agents not in the template registry.
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

// resolveBaseBranch determines the base branch from onboarding.yaml, deploy config, or git default.
func resolveBaseBranch(repoDir string, deploy *db.Deployment) string {
	// 1. Onboarding config takes priority (repo-specific knowledge).
	f, err := onboarding.Parse(onboarding.FilePath(repoDir))
	if err == nil && f.Context.BaseBranch != "" {
		return f.Context.BaseBranch
	}
	// 2. Deploy flag / config.
	if deploy.BaseBranch != "" {
		return deploy.BaseBranch
	}
	// 3. Git default branch detection.
	if branch, err := gitpkg.DefaultBranch(repoDir); err == nil && branch != "" {
		return branch
	}
	return "main"
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

// buildAgentArgs constructs CLI arguments for any agent with pre-assembled prompt.
func buildAgentArgs(job *db.Job, deploy *db.Deployment, agentName string, allowedTools []string, prompt, lessonsPrompt string) []string {
	maxTurns := job.EffectiveMaxTurns(deploy)
	maxBudget := job.EffectiveMaxBudget(deploy)

	// For reviewer, use deploy-level review overrides if set.
	if agentName == "reviewer" {
		if deploy.ReviewMaxTurns.Valid {
			maxTurns = int(deploy.ReviewMaxTurns.Int64)
		}
		if deploy.ReviewMaxBudget.Valid {
			maxBudget = deploy.ReviewMaxBudget.Float64
		}
	}

	args := []string{
		"--agent", agentName,
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", strconv.Itoa(maxTurns),
		"--max-budget-usd", fmt.Sprintf("%.2f", maxBudget),
		"--allowedTools", toCliAllowedTools(allowedTools),
	}
	if lessonsPrompt != "" {
		args = append(args, "--append-system-prompt", lessonsPrompt)
	}
	args = append(args, "--", prompt)
	return args
}
