package supervisor

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/aptx-health/agent-minder/internal/onboarding"
)

// AssembleContext builds the user prompt by running each declared context provider
// and concatenating their output.
func AssembleContext(ctx context.Context, sc *SlotContext, providers []string) string {
	var b strings.Builder

	for _, provider := range providers {
		section := renderProvider(ctx, sc, provider)
		if section != "" {
			b.WriteString(section)
			b.WriteString("\n")
		}
	}

	// Always add commands section for reactive agents with an issue.
	if sc.Job.IssueNumber > 0 {
		b.WriteString(renderCommands(sc))
	}

	return b.String()
}

func renderProvider(ctx context.Context, sc *SlotContext, provider string) string {
	switch {
	case provider == "issue":
		return renderIssueContext(ctx, sc)
	case provider == "repo_info":
		return renderRepoInfo(sc)
	case provider == "file_list":
		return renderFileList(sc)
	case provider == "lessons":
		return "" // lessons handled separately via --append-system-prompt
	case provider == "sibling_jobs":
		return renderSiblingJobs(sc)
	case provider == "dep_graph":
		return renderDepGraph(sc)
	case strings.HasPrefix(provider, "recent_commits:"):
		daysStr := strings.TrimPrefix(provider, "recent_commits:")
		days, err := strconv.Atoi(daysStr)
		if err != nil || days < 1 {
			days = 7
		}
		return renderRecentCommits(sc, days)
	default:
		return ""
	}
}

// --- Individual providers ---

func renderIssueContext(ctx context.Context, sc *SlotContext) string {
	job := sc.Job
	if job.IssueNumber <= 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Task Context\n\n")
	fmt.Fprintf(&b, "**Issue:** #%d — %s\n", job.IssueNumber, job.IssueTitle.String)
	fmt.Fprintf(&b, "**Repository:** %s/%s\n\n", sc.Owner, sc.Repo)

	if job.IssueBody.Valid && job.IssueBody.String != "" {
		b.WriteString(job.IssueBody.String)
		b.WriteString("\n\n")
	}

	// Fetch and include comments.
	comments := sc.FetchIssueComments(ctx)
	if comments != "" {
		b.WriteString("## Issue Discussion\n\n")
		b.WriteString(comments)
		b.WriteString("\n\n")
	}

	return b.String()
}

func renderRepoInfo(sc *SlotContext) string {
	var b strings.Builder
	b.WriteString("## Repository Info\n\n")
	fmt.Fprintf(&b, "**Repository:** %s/%s\n", sc.Owner, sc.Repo)

	if sc.WorktreePath != "" {
		fmt.Fprintf(&b, "**Worktree:** %s\n", sc.WorktreePath)
	} else {
		fmt.Fprintf(&b, "**Directory:** %s\n", sc.RepoDir)
	}

	if sc.Branch != "" {
		fmt.Fprintf(&b, "**Branch:** %s\n", sc.Branch)
	}
	fmt.Fprintf(&b, "**Base branch:** %s\n\n", sc.BaseBranch)

	if sc.TestCommand != "" {
		testCmd := wrapWithTimeout(sc.TestCommand, sc.TestTimeout)
		fmt.Fprintf(&b, "**Test command:** `%s`\n", testCmd)
		fmt.Fprintf(&b, "**Test timeout:** %s\n", sc.TestTimeout)
		fmt.Fprintf(&b, "\n**IMPORTANT:** Always use the test command exactly as shown — it includes a timeout.\n")
		fmt.Fprintf(&b, "If a command hangs past its timeout, it will be killed automatically.\n")
		fmt.Fprintf(&b, "A timeout kill should be treated as a test failure, not retried indefinitely.\n\n")
	}

	// Try to include language/framework info from onboarding.
	f, err := onboarding.Parse(onboarding.FilePath(sc.RepoDir))
	if err == nil {
		if len(f.Inventory.Languages) > 0 {
			fmt.Fprintf(&b, "**Languages:** %s\n", strings.Join(f.Inventory.Languages, ", "))
		}
		if len(f.Inventory.PackageManagers) > 0 {
			fmt.Fprintf(&b, "**Package managers:** %s\n", strings.Join(f.Inventory.PackageManagers, ", "))
		}
	}
	b.WriteString("\n")

	return b.String()
}

func renderFileList(sc *SlotContext) string {
	dir := sc.WorktreePath
	if dir == "" {
		dir = sc.RepoDir
	}

	// Use fd or find for a quick tree, limited to reasonable depth.
	out, err := exec.Command("fd", "--type", "f", "--max-depth", "3", "--exclude", ".git", ".", dir).Output()
	if err != nil {
		// Fallback to find.
		out, err = exec.Command("find", dir, "-maxdepth", "3", "-type", "f",
			"-not", "-path", "*/.git/*", "-not", "-path", "*/node_modules/*").Output()
		if err != nil {
			return ""
		}
	}

	files := strings.TrimSpace(string(out))
	if files == "" {
		return ""
	}

	// Trim to repo-relative paths.
	lines := strings.Split(files, "\n")
	if len(lines) > 100 {
		lines = lines[:100]
		lines = append(lines, fmt.Sprintf("... and %d more files", len(strings.Split(files, "\n"))-100))
	}

	var b strings.Builder
	b.WriteString("## File Structure\n\n```\n")
	for _, line := range lines {
		// Make paths relative.
		rel := strings.TrimPrefix(line, dir+"/")
		b.WriteString(rel)
		b.WriteString("\n")
	}
	b.WriteString("```\n\n")

	return b.String()
}

func renderRecentCommits(sc *SlotContext, days int) string {
	dir := sc.RepoDir
	since := fmt.Sprintf("--since=%d.days.ago", days)
	out, err := exec.Command("git", "-C", dir, "log", "--oneline", since, "-20").Output()
	if err != nil {
		return ""
	}

	commits := strings.TrimSpace(string(out))
	if commits == "" {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Recent Commits (last %d days)\n\n```\n%s\n```\n\n", days, commits)
	return b.String()
}

func renderSiblingJobs(sc *SlotContext) string {
	jobs, err := sc.Store.GetJobs(sc.Deploy.ID)
	if err != nil || len(jobs) <= 1 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Related Jobs\n\n")
	b.WriteString("| Issue | Title | Status |\n")
	b.WriteString("|-------|-------|--------|\n")
	for _, j := range jobs {
		if j.ID == sc.Job.ID {
			continue
		}
		label := j.Name
		if j.IssueNumber > 0 {
			label = fmt.Sprintf("#%d", j.IssueNumber)
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", label, j.IssueTitle.String, j.Status)
	}
	b.WriteString("\n")

	return b.String()
}

func renderDepGraph(sc *SlotContext) string {
	dg, err := sc.Store.GetDepGraph(sc.Deploy.ID)
	if err != nil || dg == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Dependency Graph\n\n```json\n")
	b.WriteString(dg.GraphJSON)
	b.WriteString("\n```\n\n")
	return b.String()
}

func renderCommands(sc *SlotContext) string {
	job := sc.Job
	if job.IssueNumber <= 0 {
		return ""
	}

	var b strings.Builder
	owner, repo := sc.Owner, sc.Repo

	b.WriteString("## Commands for this task\n\n")
	fmt.Fprintf(&b, "Label in-progress: gh issue edit %d --add-label \"in-progress\" -R %s/%s\n", job.IssueNumber, owner, repo)
	fmt.Fprintf(&b, "Post starting comment: gh issue comment %d --body \"Agent starting work on this issue\" -R %s/%s\n", job.IssueNumber, owner, repo)
	fmt.Fprintf(&b, "Commit message must include: Fixes #%d\n", job.IssueNumber)
	fmt.Fprintf(&b, "Rebase before push:\n")
	fmt.Fprintf(&b, "  git fetch origin %s\n", sc.BaseBranch)
	fmt.Fprintf(&b, "  git rebase origin/%s\n", sc.BaseBranch)
	fmt.Fprintf(&b, "Draft PR: gh pr create --draft --base %s -R %s/%s\n", sc.BaseBranch, owner, repo)

	return b.String()
}

// renderReviewContext builds context for the review stage.
func renderReviewContext(ctx context.Context, sc *SlotContext) string {
	job := sc.Job
	var b strings.Builder

	fmt.Fprintf(&b, "## Review Context\n\n")
	fmt.Fprintf(&b, "**PR:** #%d\n", job.PRNumber.Int64)
	if job.IssueNumber > 0 {
		fmt.Fprintf(&b, "**Issue:** #%d — %s\n", job.IssueNumber, job.IssueTitle.String)
	}
	fmt.Fprintf(&b, "**Repository:** %s/%s\n", sc.Owner, sc.Repo)
	fmt.Fprintf(&b, "**Branch:** %s\n", job.Branch.String)
	fmt.Fprintf(&b, "**Base branch:** %s\n", sc.BaseBranch)
	if sc.WorktreePath != "" {
		fmt.Fprintf(&b, "**Worktree:** %s\n", sc.WorktreePath)
	}
	b.WriteString("\n")

	if job.IssueBody.Valid && job.IssueBody.String != "" {
		b.WriteString("## Issue Description\n\n")
		b.WriteString(job.IssueBody.String)
		b.WriteString("\n\n")
	}

	comments := sc.FetchIssueComments(ctx)
	if comments != "" {
		b.WriteString("## Issue Discussion\n\n")
		b.WriteString(comments)
		b.WriteString("\n\n")
	}

	if sc.TestCommand != "" {
		testCmd := wrapWithTimeout(sc.TestCommand, sc.TestTimeout)
		fmt.Fprintf(&b, "## Test command\n\nRun tests: `%s`\n\n", testCmd)
		b.WriteString("**IMPORTANT:** Always use the test command exactly as shown — it includes a timeout. A timeout kill should be treated as a test failure.\n\n")
	}

	// Add sibling context.
	b.WriteString(renderSiblingJobs(sc))
	b.WriteString(renderDepGraph(sc))

	fmt.Fprintf(&b, "## Commands for this review\n\n")
	fmt.Fprintf(&b, "View PR diff: gh pr diff %d -R %s/%s\n", job.PRNumber.Int64, sc.Owner, sc.Repo)
	fmt.Fprintf(&b, "View PR: gh pr view %d -R %s/%s\n", job.PRNumber.Int64, sc.Owner, sc.Repo)
	fmt.Fprintf(&b, "Rebase before push:\n")
	fmt.Fprintf(&b, "  git fetch origin %s\n", sc.BaseBranch)
	fmt.Fprintf(&b, "  git rebase origin/%s\n", sc.BaseBranch)

	return b.String()
}

// wrapWithTimeout wraps a command with a portable timeout mechanism.
// - Go test commands: uses -timeout flag (built-in, no external deps)
// - Other commands: uses perl alarm (available on macOS + Linux, no coreutils needed)
func wrapWithTimeout(cmd, timeout string) string {
	secs := timeoutToSeconds(timeout)

	// Go test: use -timeout flag directly.
	if strings.HasPrefix(cmd, "go test") {
		return cmd + " -timeout " + timeout
	}

	// Everything else: perl alarm is portable (macOS ships perl, Linux has it).
	return fmt.Sprintf("perl -e 'alarm %d; exec @ARGV' -- %s", secs, cmd)
}

// timeoutToSeconds converts a Go duration string like "5m" to seconds.
func timeoutToSeconds(timeout string) int {
	timeout = strings.TrimSpace(timeout)
	var n int
	if strings.HasSuffix(timeout, "m") {
		if _, err := fmt.Sscanf(timeout, "%dm", &n); err == nil && n > 0 {
			return n * 60
		}
	}
	if strings.HasSuffix(timeout, "s") {
		if _, err := fmt.Sscanf(timeout, "%ds", &n); err == nil && n > 0 {
			return n
		}
	}
	if strings.HasSuffix(timeout, "h") {
		if _, err := fmt.Sscanf(timeout, "%dh", &n); err == nil && n > 0 {
			return n * 3600
		}
	}
	return 300 // default 5 minutes
}
