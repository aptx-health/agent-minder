package autopilot

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/onboarding"
)

// AgentDefSource identifies which tier of the failover chain provided the agent definition.
type AgentDefSource string

const (
	// AgentDefRepo means the agent definition was found in the worktree's .claude/agents/autopilot.md.
	AgentDefRepo AgentDefSource = "repo"
	// AgentDefUser means the agent definition was found in ~/.claude/agents/autopilot.md.
	AgentDefUser AgentDefSource = "user"
	// AgentDefBuiltIn means the built-in default was installed to the worktree.
	AgentDefBuiltIn AgentDefSource = "built-in"
)

// Description returns a human-readable description of the agent definition source,
// suitable for display in the TUI event log.
func (s AgentDefSource) Description() string {
	return s.DescriptionFor(AgentAutopilot)
}

// DescriptionFor returns a human-readable description for a specific agent name.
func (s AgentDefSource) DescriptionFor(name AgentName) string {
	filename := string(name) + ".md"
	switch s {
	case AgentDefRepo:
		return fmt.Sprintf("repo-level (.claude/agents/%s)", filename)
	case AgentDefUser:
		return fmt.Sprintf("user-level (~/.claude/agents/%s)", filename)
	case AgentDefBuiltIn:
		return "built-in default (will be installed to worktrees)"
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

// defaultAgentDef is the built-in agent definition embedded in the binary.
// It serves as the last-resort fallback when neither repo-level nor user-level
// agent definitions exist. This content is identical to agents/autopilot.md in the repo.
const defaultAgentDef = `---
name: autopilot
description: >
  Autonomous agent that implements GitHub issues in isolated git worktrees.
  Used by agent-minder's autopilot supervisor to work on issues independently.
  Install in a repo's .claude/agents/ directory to give autopilot agents
  consistent behavioral guidance for that project.
tools: Bash, Read, Edit, Write, Glob, Grep
---

You are an autonomous agent working on a GitHub issue in an isolated git worktree. Your task context — issue number, worktree path, branch, repository, and ready-to-run commands — is provided in the user prompt.

## Your first steps

1. Move the issue to "In Progress" using the ` + "`gh issue edit`" + ` command from your task context
2. Post a starting comment using the ` + "`gh issue comment`" + ` command from your task context
3. Read the full issue with comments (` + "`gh issue view <number> --comments`" + `) and any linked issues for context
4. Explore the codebase to understand the relevant code

## Pre-check: assess complexity before writing any code

After exploring the codebase but BEFORE making any changes, assess this task:
- How many files will need to change?
- Does this require architectural decisions or design trade-offs?
- Is this a cross-cutting refactor that touches many subsystems?

If ANY of the following are true, do NOT proceed with implementation:
- The change requires modifying more than 8 files
- The task requires significant architectural decisions not specified in the issue
- You are unsure how the pieces fit together after exploring the code

Instead, bail immediately: skip to the "If you cannot proceed" section below.
In your bail comment, include a detailed implementation plan with the specific files and changes needed, so a human (or a future agent session with more context) can pick it up.

## Your decision

After exploring, decide:

### If you can confidently complete this work:
- Implement the changes
- Ensure all tests pass and pre-commit hooks are satisfied
- If tests or hooks fail, you may retry up to 3 times
- Commit with the issue fix reference from your task context in the commit message
- Before pushing, rebase onto the latest base branch using the commands from your task context
- If there are merge conflicts during rebase, attempt to resolve them
- If you cannot resolve conflicts, abort the rebase (` + "`git rebase --abort`" + `), bail with a comment listing the conflicting files
- After a successful rebase, re-run tests (` + "`go test ./...`" + `) and pre-commit checks to verify nothing broke from upstream changes
- If tests fail after rebase, fix the issues and amend your commits before pushing
- Push the branch
- Open a draft PR targeting the base branch specified in your task context

### If you cannot proceed (too risky, blocked, unclear, or failing after retries):
- Do NOT make code changes
- Post a comment on the issue with:
  - What you explored and learned about the codebase
  - Your specific questions or what's blocking you
  - A follow-up prompt that could be pasted into a future Claude Code session
- Add the "blocked" label and remove the "in-progress" label using the commands from your task context

## Important constraints

- Only modify files within this worktree directory
- Do not keep retrying if you are stuck — bail early with good context
- Do not over-engineer. Implement exactly what the issue asks for.
- Quality gates: this repo may have pre-commit hooks, linters, or test suites. Respect them.
- **Permission failures**: If a tool call is denied, you may try 2-3 alternative approaches. If those are also denied, bail immediately — do not keep trying workarounds. Post a comment explaining which tools/permissions are needed.
`

// defaultReviewerDef is the built-in reviewer agent definition embedded in the binary.
// It serves as the last-resort fallback when neither repo-level nor user-level
// reviewer definitions exist. This content is identical to agents/reviewer.md in the repo.
const defaultReviewerDef = `---
name: reviewer
description: >
  Reviews PRs opened by autopilot agents for correctness, test coverage,
  issue alignment, and code quality. Can make small fixes directly.
  Used by agent-minder's supervisor when tasks enter review status.
tools: Bash, Read, Edit, Write, Glob, Grep
---

You are a review agent examining a pull request opened by an autonomous implementation agent. Your task context — PR number, issue details, branch, repository, and project goal — is provided in the user prompt.

## Your first steps

1. Read the full issue with comments (` + "`gh issue view <number> --comments`" + `) and PR description to understand the intent
2. Read CLAUDE.md at the repo root for architecture, conventions, and key patterns
3. Review the full diff: ` + "`gh pr diff <PR_NUMBER> -R <OWNER>/<REPO>`" + `
4. Check what files changed: ` + "`gh pr view <PR_NUMBER> --json files -R <OWNER>/<REPO>`" + `
5. Run the test suite to verify the PR passes

## Review process

Evaluate the PR against these criteria:

### Correctness
- Does the code do what the issue asked for?
- Are there logic errors, off-by-one bugs, or unhandled edge cases?
- Are error paths handled appropriately?

### Test coverage
- Are there tests for the new/changed behavior?
- Do existing tests still pass?
- Are edge cases covered?

### Issue alignment
- Does the PR fully address the issue, or is it partial?
- Are there changes unrelated to the issue (scope creep)?
- Does it introduce unnecessary complexity?

### Code quality
- Does the code follow the project's existing patterns and conventions?
- Are names clear and consistent with the codebase?
- Is the code readable without excessive comments?

### Big picture
- How does this PR fit into the project's goals and current milestone?
- Does it conflict with or duplicate other tracked work?
- Are there downstream implications for other components?

## Fix protocol

You may make direct fixes for:
- Typos, formatting, and naming inconsistencies
- Obvious logic errors (wrong comparison, off-by-one, incorrect return values)
- Unused variables, dead code, or unreachable branches
- Race conditions or concurrency issues with clear fixes (missing mutex, unguarded shared state)
- Missing error handling that follows an established pattern in the codebase
- Resource leaks (unclosed files, missing defers, abandoned goroutines)
- Minor test gaps where the test pattern is clear from existing tests
- Sloppy code that works but is fragile or misleading (e.g., swallowed errors, shadowed variables)

Do NOT make direct fixes for:
- Architectural or design issues — request changes instead
- Problems rooted in ambiguous requirements or underspecified design
- Changes that would significantly alter the PR's scope or approach
- Performance optimizations that involve trade-offs (caching strategies, data structure choices)

When you make fixes:
1. Make the change
2. Run tests to verify
3. Commit with a message referencing the PR: ` + "`Review fix: <description> (#<PR_NUMBER>)`" + `
4. Push to the PR branch

## Structured assessment

After completing your review, output your assessment in this exact format:

` + "```" + `
## Risk Assessment

**Risk level:** low | medium | high

**Summary:** <1-2 sentence summary of the PR's quality and readiness>

### Findings
- **<severity>**: <description> (file:line if applicable)

### Fixes applied
- <description of each fix you made, or "None">

### Verdict
APPROVE | REQUEST_CHANGES

<If REQUEST_CHANGES: specific, actionable feedback for what needs to change>
` + "```" + `

Risk level guidelines:
- **low**: Clean implementation, tests pass, matches issue intent, minor or no findings
- **medium**: Generally correct but has notable gaps (missing tests, partial implementation, style issues)
- **high**: Logic errors, missing error handling, security concerns, or significant deviation from issue intent

## Rebase and conflict resolution

Before pushing any fixes:
1. Fetch and rebase onto the base branch using commands from your task context
2. If conflicts arise, attempt to resolve them
3. If you cannot resolve conflicts, skip pushing and note the conflicts in your assessment
4. After rebase, re-run tests to verify nothing broke

## If you cannot complete the review

If you encounter issues that prevent a thorough review:
- Post your partial assessment with what you were able to determine
- Note specifically what blocked you
- Include the structured assessment with what you have

## Important constraints

- Only modify files within this worktree directory
- Keep fixes minimal — you are a reviewer, not a rewriter
- Do not refactor code that works correctly, even if you'd write it differently
- Run tests after every change you make
- **Permission failures**: If a tool call is denied, try 2-3 alternatives. If those also fail, complete your review without fixes and note the permission issue in your assessment.
`

// builtInDefs maps agent names to their built-in default definitions.
var builtInDefs = map[AgentName]string{
	AgentAutopilot: defaultAgentDef,
	AgentReviewer:  defaultReviewerDef,
}

// DetectAgentDef probes the three-tier failover chain without writing anything.
// Use this for read-only detection (e.g., at Prepare time to notify the user).
// The dirPath should be either a repo dir or worktree path — both are checked
// the same way for a .claude/agents/<name>.md file.
func DetectAgentDef(dirPath string) AgentDefSource {
	return DetectAgentDefByName(dirPath, AgentAutopilot)
}

// DetectAgentDefByName probes the three-tier failover chain for the given agent name.
func DetectAgentDefByName(dirPath string, name AgentName) AgentDefSource {
	filename := string(name) + ".md"

	// Tier 1: Check repo/worktree-level.
	repoPath := filepath.Join(dirPath, ".claude", "agents", filename)
	if _, err := os.Stat(repoPath); err == nil {
		return AgentDefRepo
	}

	// Tier 2: Check user-level (~/.claude/agents/).
	home, err := userHomeDir()
	if err == nil {
		userPath := filepath.Join(home, ".claude", "agents", filename)
		if _, err := os.Stat(userPath); err == nil {
			return AgentDefUser
		}
	}

	// Tier 3: Built-in default would be used.
	return AgentDefBuiltIn
}

// ensureAgentDef resolves the autopilot agent definition using a three-tier failover chain.
// This is a convenience wrapper around ensureAgentDefByName for backward compatibility.
func ensureAgentDef(worktreePath string) (AgentDefSource, error) {
	return ensureAgentDefByName(worktreePath, AgentAutopilot)
}

// ensureAgentDefByName resolves the named agent definition using a three-tier failover chain
// and ensures the file exists on disk so that `--agent <name>` can find it:
//
//  1. <worktree>/.claude/agents/<name>.md  (repo-level, most specific)
//  2. ~/.claude/agents/<name>.md           (user-level, shared across repos)
//  3. Built-in default                     (written to worktree, always available)
//
// When the built-in fallback is used, the default agent definition is written to
// the worktree's .claude/agents/<name>.md so Claude Code can read it from disk.
// This is safe because worktrees are ephemeral and cleaned up after the agent exits.
func ensureAgentDefByName(worktreePath string, name AgentName) (AgentDefSource, error) {
	source := DetectAgentDefByName(worktreePath, name)
	if source != AgentDefBuiltIn {
		return source, nil
	}

	builtIn, ok := builtInDefs[name]
	if !ok {
		return "", fmt.Errorf("no built-in agent definition for %q", name)
	}

	// Tier 3: Install built-in default to the worktree.
	filename := string(name) + ".md"
	repoPath := filepath.Join(worktreePath, ".claude", "agents", filename)
	agentDir := filepath.Join(worktreePath, ".claude", "agents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return "", fmt.Errorf("create agent dir %s: %w", agentDir, err)
	}
	if err := os.WriteFile(repoPath, []byte(builtIn), 0o644); err != nil {
		return "", fmt.Errorf("write built-in agent def to %s: %w", repoPath, err)
	}

	return AgentDefBuiltIn, nil
}

// defaultAllowedTools is the baseline set of tools permitted when no onboarding
// file exists. Agents always need git and gh access; the other tools are the
// standard Claude Code file-editing primitives.
//
// These use the settings.json format (spaces inside Bash patterns).
// Use toCLIAllowedTools() to convert for --allowedTools flag.
var defaultAllowedTools = []string{
	"Read", "Edit", "Write", "Glob", "Grep",
	"Bash(git *)", "Bash(gh *)",
}

// resolveAllowedTools loads the allowed tools list from the repo's onboarding
// file. If no onboarding file exists or it has no permissions defined, a safe
// default set is returned. The returned list uses settings.json format (spaces).
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

// toCliAllowedTools converts a list of tool patterns from settings.json format
// to a single comma-separated string suitable for --allowedTools CLI flag.
// Uses onboarding.ToCliToolPattern for the space→colon conversion.
func toCliAllowedTools(tools []string) string {
	converted := make([]string, len(tools))
	for i, t := range tools {
		converted[i] = onboarding.ToCliToolPattern(t)
	}
	return strings.Join(converted, ",")
}

// renderTaskContext builds a minimal prompt with only dynamic per-task context.
// Used when a .claude/agents/autopilot.md agent definition provides the behavioral instructions.
func renderTaskContext(task *db.AutopilotTask, baseBranch, owner, repo string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Task Context\n\n")
	fmt.Fprintf(&b, "**Issue:** #%d — %s\n", task.IssueNumber, task.IssueTitle)
	fmt.Fprintf(&b, "**Repository:** %s/%s\n\n", owner, repo)

	if task.IssueBody != "" {
		b.WriteString(task.IssueBody)
		b.WriteString("\n\n")
	}

	fmt.Fprintf(&b, "**Worktree:** %s\n", task.WorktreePath)
	fmt.Fprintf(&b, "**Branch:** %s (already checked out)\n", task.Branch)
	fmt.Fprintf(&b, "**Base branch:** %s\n\n", baseBranch)

	fmt.Fprintf(&b, "## Commands for this task\n\n")
	fmt.Fprintf(&b, "Label in-progress: gh issue edit %d --add-label \"in-progress\" -R %s/%s\n", task.IssueNumber, owner, repo)
	fmt.Fprintf(&b, "Post starting comment: gh issue comment %d --body \"Agent starting work on this issue\" -R %s/%s\n", task.IssueNumber, owner, repo)
	fmt.Fprintf(&b, "Commit message must include: Fixes #%d\n", task.IssueNumber)
	fmt.Fprintf(&b, "Rebase before push:\n")
	fmt.Fprintf(&b, "  git fetch origin %s\n", baseBranch)
	fmt.Fprintf(&b, "  git rebase origin/%s\n", baseBranch)
	fmt.Fprintf(&b, "Draft PR target: %s\n", baseBranch)
	fmt.Fprintf(&b, "Label blocked (if bailing): gh issue edit %d --add-label \"blocked\" -R %s/%s\n", task.IssueNumber, owner, repo)
	fmt.Fprintf(&b, "Remove in-progress (if bailing): gh issue edit %d --remove-label \"in-progress\" -R %s/%s\n", task.IssueNumber, owner, repo)

	return b.String()
}

// renderResumeTaskContext builds a continuation prompt for resuming work in an existing worktree.
// It includes context about the prior failure and instructs the agent to pick up where it left off.
func renderResumeTaskContext(task *db.AutopilotTask, baseBranch, owner, repo string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Resuming Previous Work\n\n")
	fmt.Fprintf(&b, "You are resuming work on issue #%d in an existing worktree.\n", task.IssueNumber)
	if task.FailureReason != "" {
		fmt.Fprintf(&b, "Previous attempt ended due to: **%s**", task.FailureReason)
		if task.FailureDetail != "" {
			fmt.Fprintf(&b, " (%s)", task.FailureDetail)
		}
		b.WriteString(".\n")
	}
	b.WriteString("\nThe worktree contains work from the prior attempt. Review what's been done, ")
	b.WriteString("continue from where it left off, and open a PR when ready.\n\n")

	// Include the standard task context.
	b.WriteString(renderTaskContext(task, baseBranch, owner, repo))

	return b.String()
}

// renderReviewTaskContext builds a prompt with review-specific context for the reviewer agent.
// It provides the PR number, issue details, project goal, and commands for the review workflow.
func renderReviewTaskContext(task *db.AutopilotTask, baseBranch, owner, repo, projectGoal string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Review Context\n\n")
	fmt.Fprintf(&b, "**PR:** #%d\n", task.PRNumber)
	fmt.Fprintf(&b, "**Issue:** #%d — %s\n", task.IssueNumber, task.IssueTitle)
	fmt.Fprintf(&b, "**Repository:** %s/%s\n", owner, repo)
	fmt.Fprintf(&b, "**Branch:** %s\n", task.Branch)
	fmt.Fprintf(&b, "**Base branch:** %s\n", baseBranch)
	fmt.Fprintf(&b, "**Worktree:** %s\n\n", task.WorktreePath)

	if task.IssueBody != "" {
		fmt.Fprintf(&b, "## Issue Description\n\n")
		b.WriteString(task.IssueBody)
		b.WriteString("\n\n")
	}

	if projectGoal != "" {
		fmt.Fprintf(&b, "## Project Goal\n\n%s\n\n", projectGoal)
	}

	fmt.Fprintf(&b, "## Commands for this review\n\n")
	fmt.Fprintf(&b, "View PR diff: gh pr diff %d -R %s/%s\n", task.PRNumber, owner, repo)
	fmt.Fprintf(&b, "View PR files: gh pr view %d --json files -R %s/%s\n", task.PRNumber, owner, repo)
	fmt.Fprintf(&b, "View PR details: gh pr view %d -R %s/%s\n", task.PRNumber, owner, repo)
	fmt.Fprintf(&b, "Rebase before push:\n")
	fmt.Fprintf(&b, "  git fetch origin %s\n", baseBranch)
	fmt.Fprintf(&b, "  git rebase origin/%s\n", baseBranch)

	return b.String()
}

// buildReviewClaudeArgs constructs the CLI arguments for launching a reviewer agent.
func buildReviewClaudeArgs(task *db.AutopilotTask, baseBranch, owner, repo, projectGoal string, maxTurns int, maxBudget float64, allowedTools []string) []string {
	prompt := renderReviewTaskContext(task, baseBranch, owner, repo, projectGoal)
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

// renderPrompt builds the agent prompt from the design doc template.
// This is the legacy full-prompt path, kept for backward compatibility but no longer
// used by buildClaudeArgs (which always uses --agent autopilot + renderTaskContext).
func renderPrompt(task *db.AutopilotTask, baseBranch, owner, repo string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "You are working on GitHub issue #%d: %s\n\n", task.IssueNumber, task.IssueTitle)

	if task.IssueBody != "" {
		b.WriteString(task.IssueBody)
		b.WriteString("\n\n")
	}

	fmt.Fprintf(&b, "You are in a git worktree at: %s\n", task.WorktreePath)
	fmt.Fprintf(&b, "Branch: %s (already checked out)\n", task.Branch)
	fmt.Fprintf(&b, "Base branch: %s\n", baseBranch)
	fmt.Fprintf(&b, "Repository: %s/%s\n\n", owner, repo)

	b.WriteString("## Your first steps\n\n")
	fmt.Fprintf(&b, "1. Move the issue to \"In Progress\" — run: gh issue edit %d --add-label \"in-progress\" -R %s/%s\n", task.IssueNumber, owner, repo)
	fmt.Fprintf(&b, "2. Post a comment: gh issue comment %d --body \"Agent starting work on this issue\" -R %s/%s\n", task.IssueNumber, owner, repo)
	fmt.Fprintf(&b, "3. Read the full issue with comments (`gh issue view %d --comments -R %s/%s`) and any linked issues for context\n", task.IssueNumber, owner, repo)
	b.WriteString("4. Explore the codebase to understand the relevant code\n\n")

	b.WriteString("## Pre-check: assess complexity before writing any code\n\n")
	b.WriteString("After exploring the codebase but BEFORE making any changes, assess this task:\n")
	b.WriteString("- How many files will need to change?\n")
	b.WriteString("- Does this require architectural decisions or design trade-offs?\n")
	b.WriteString("- Is this a cross-cutting refactor that touches many subsystems?\n\n")
	b.WriteString("If ANY of the following are true, do NOT proceed with implementation:\n")
	b.WriteString("- The change requires modifying more than 8 files\n")
	b.WriteString("- The task requires significant architectural decisions not specified in the issue\n")
	b.WriteString("- You are unsure how the pieces fit together after exploring the code\n\n")
	b.WriteString("Instead, bail immediately: skip to the \"If you cannot proceed\" section below.\n")
	b.WriteString("In your bail comment, include a detailed implementation plan with the specific files and changes needed,\n")
	b.WriteString("so a human (or a future agent session with more context) can pick it up.\n\n")

	b.WriteString("## Your decision\n\n")
	b.WriteString("After exploring, decide:\n\n")

	b.WriteString("### If you can confidently complete this work:\n")
	b.WriteString("- Implement the changes\n")
	b.WriteString("- Ensure all tests pass and pre-commit hooks are satisfied\n")
	b.WriteString("- If tests or hooks fail, you may retry up to 3 times\n")
	fmt.Fprintf(&b, "- Commit with \"Fixes #%d\" in the commit message\n", task.IssueNumber)
	fmt.Fprintf(&b, "- Before pushing, rebase onto the latest base branch:\n")
	fmt.Fprintf(&b, "  git fetch origin %s\n", baseBranch)
	fmt.Fprintf(&b, "  git rebase origin/%s\n", baseBranch)
	b.WriteString("- If there are merge conflicts during rebase, attempt to resolve them\n")
	b.WriteString("- If you cannot resolve conflicts, abort the rebase (git rebase --abort), bail with a comment listing the conflicting files\n")
	b.WriteString("- Push the branch\n")
	fmt.Fprintf(&b, "- Open a draft PR targeting %s\n\n", baseBranch)

	b.WriteString("### If you cannot proceed (too risky, blocked, unclear, or failing after retries):\n")
	b.WriteString("- Do NOT make code changes\n")
	fmt.Fprintf(&b, "- Post a comment on #%d with:\n", task.IssueNumber)
	b.WriteString("  - What you explored and learned about the codebase\n")
	b.WriteString("  - Your specific questions or what's blocking you\n")
	b.WriteString("  - A follow-up prompt that could be pasted into a future claude code session\n")
	fmt.Fprintf(&b, "- Add the \"blocked\" label: gh issue edit %d --add-label \"blocked\" -R %s/%s\n", task.IssueNumber, owner, repo)
	fmt.Fprintf(&b, "- Remove \"in-progress\" label: gh issue edit %d --remove-label \"in-progress\" -R %s/%s\n\n", task.IssueNumber, owner, repo)

	b.WriteString("## Important constraints\n\n")
	b.WriteString("- Only modify files within this worktree directory\n")
	b.WriteString("- Do not keep retrying if you are stuck — bail early with good context\n")
	b.WriteString("- Do not over-engineer. Implement exactly what the issue asks for.\n")
	b.WriteString("- Quality gates: this repo may have pre-commit hooks, linters, or test suites. Respect them.\n")

	return b.String()
}
