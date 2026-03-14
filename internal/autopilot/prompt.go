package autopilot

import (
	"fmt"
	"strings"

	"github.com/dustinlange/agent-minder/internal/db"
)

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

	b.WriteString("## Your decision\n\n")
	b.WriteString("After exploring, decide:\n\n")

	b.WriteString("### If you can confidently complete this work:\n")
	b.WriteString("- Implement the changes\n")
	b.WriteString("- **Before opening a PR, you MUST validate your changes:**\n")
	b.WriteString("  1. Run `go build ./...` — if it fails, fix the build errors\n")
	b.WriteString("  2. Run `go test ./...` — if tests fail, fix them\n")
	b.WriteString("  3. If you cannot fix build/test failures after 3 attempts, do NOT open a PR — follow the bail procedure below instead\n")
	b.WriteString("- Ensure pre-commit hooks are satisfied\n")
	fmt.Fprintf(&b, "- Commit with \"Fixes #%d\" in the commit message\n", task.IssueNumber)
	b.WriteString("- Push the branch\n")
	b.WriteString("- Open a draft PR that includes a **Test results** section in the description with:\n")
	b.WriteString("  - The `go build ./...` result (pass/fail)\n")
	b.WriteString("  - The `go test ./...` output summary (number of packages passed, any failures)\n\n")

	b.WriteString("### If you cannot proceed (too risky, blocked, unclear, or failing after retries):\n")
	b.WriteString("- Do NOT open a PR\n")
	fmt.Fprintf(&b, "- Post a comment on #%d with:\n", task.IssueNumber)
	b.WriteString("  - What you explored and learned about the codebase\n")
	b.WriteString("  - Your specific questions or what's blocking you\n")
	b.WriteString("  - If build/tests failed: include the full error output and what you tried to fix\n")
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
