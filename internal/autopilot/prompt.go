package autopilot

import (
	"fmt"
	"strings"

	"github.com/dustinlange/agent-minder/internal/db"
)

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

// renderPrompt builds the agent prompt from the design doc template.
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
	b.WriteString("3. Read the full issue and any linked issues for context\n")
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
