package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aptx-health/agent-minder/internal/agentutil"
	"github.com/aptx-health/agent-minder/internal/db"
	gitpkg "github.com/aptx-health/agent-minder/internal/git"
)

// ensureWorktree resolves or recreates the worktree for a job and returns
// the worktree path and branch name.
func ensureWorktree(repoDir string, job *db.Job) (string, string, error) {
	worktreePath := job.WorktreePath.String
	branch := job.Branch.String

	// Worktree exists on disk — use it.
	if worktreePath != "" && dirExistsCheckout(worktreePath) {
		if branch == "" {
			branch = candidateBranchNames(job)[0]
		}
		return worktreePath, branch, nil
	}

	// Recreate from branch.
	candidates := candidateBranchNames(job)
	fmt.Printf("Worktree not found, recreating from branch %s...\n", candidates[0])

	_ = gitpkg.Fetch(repoDir)
	_ = gitpkg.WorktreePrune(repoDir)

	var err error
	branch, err = pickExistingBranch(repoDir, candidates)
	if err != nil {
		return "", "", err
	}

	if worktreePath == "" {
		home, _ := os.UserHomeDir()
		worktreePath = filepath.Join(home, ".agent-minder", "worktrees", "checkout", job.Name)
	}
	_ = os.MkdirAll(filepath.Dir(worktreePath), 0755)

	if err := createWorktreeFromBranch(repoDir, worktreePath, branch); err != nil {
		return "", "", err
	}
	return worktreePath, branch, nil
}

// buildResumePrompt gathers all available context for a job and dumps it
// into a prompt. No clever filtering — just include everything we have.
func buildResumePrompt(job *db.Job) string {
	var b strings.Builder

	b.WriteString("You're picking up work from a previous agent run. Here's everything we know:\n\n")

	// Always include metadata.
	fmt.Fprintf(&b, "Agent: %s | Status: %s", job.Agent, job.Status)
	if job.CostUSD > 0 {
		fmt.Fprintf(&b, " | Cost: $%.2f", job.CostUSD)
	}
	b.WriteString("\n")

	if job.IssueNumber > 0 {
		fmt.Fprintf(&b, "Issue: #%d — %s\n", job.IssueNumber, job.IssueTitle.String)
	}
	if job.PRNumber.Valid && job.PRNumber.Int64 > 0 {
		fmt.Fprintf(&b, "PR: #%d\n", job.PRNumber.Int64)
	}
	if job.Branch.Valid && job.Branch.String != "" {
		fmt.Fprintf(&b, "Branch: %s\n", job.Branch.String)
	}
	if job.ReviewRisk.Valid && job.ReviewRisk.String != "" {
		fmt.Fprintf(&b, "Review risk: %s\n", job.ReviewRisk.String)
	}
	if job.FailureReason.Valid && job.FailureReason.String != "" {
		fmt.Fprintf(&b, "Failure reason: %s\n", job.FailureReason.String)
	}
	if job.FailureDetail.Valid && job.FailureDetail.String != "" {
		detail := job.FailureDetail.String
		if len(detail) > 1500 {
			detail = detail[:1500] + "...(truncated)"
		}
		fmt.Fprintf(&b, "Failure detail: %s\n", detail)
	}
	b.WriteString("\n")

	// Issue body.
	if job.IssueBody.Valid && job.IssueBody.String != "" {
		b.WriteString("--- Issue body ---\n")
		body := job.IssueBody.String
		if len(body) > 3000 {
			body = body[:3000] + "\n...(truncated)"
		}
		b.WriteString(body)
		b.WriteString("\n\n")
	}

	// Result JSON (spike findings, etc).
	if job.ResultJSON.Valid && job.ResultJSON.String != "" {
		b.WriteString("--- Agent result ---\n")
		result := job.ResultJSON.String
		if len(result) > 3000 {
			result = result[:3000] + "\n...(truncated)"
		}
		b.WriteString(result)
		b.WriteString("\n\n")
	}

	// Agent log final output.
	if logResult := parseAgentLogResult(job); logResult != "" {
		b.WriteString("--- Agent final output ---\n")
		b.WriteString(logResult)
		b.WriteString("\n\n")
	}

	b.WriteString("Review the git log, current diff, and any PR comments to get fully oriented. What would you like to do?")

	return b.String()
}

// parseAgentLogResult extracts the result text from the agent's stream-json log.
func parseAgentLogResult(job *db.Job) string {
	logPath := job.AgentLog.String
	if logPath == "" {
		// Try convention-based path.
		home, _ := os.UserHomeDir()
		base := filepath.Join(home, ".agent-minder", "agents")
		candidate := filepath.Join(base, fmt.Sprintf("%s-%s.log", job.DeploymentID, job.Name))
		if fileExists(candidate) {
			logPath = candidate
		}
	}
	if logPath == "" {
		return ""
	}

	result, err := agentutil.ParseAgentLog(logPath)
	if err != nil || result == nil {
		return ""
	}

	text := result.Result
	if len(text) > 1500 {
		text = text[:1500] + "\n...(truncated)"
	}
	return text
}

// launchClaude execs claude in the given directory with an initial prompt.
// This replaces the current process.
func launchClaude(dir, prompt string) error {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not found in PATH: %w", err)
	}

	// Change to worktree directory before exec.
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("chdir to worktree: %w", err)
	}

	fmt.Printf("\nLaunching Claude in %s...\n\n", dir)

	// exec replaces this process with claude.
	return syscall.Exec(claudePath, []string{"claude", prompt}, os.Environ())
}

// resolveLogPath finds the log file for a job.
func resolveLogPath(job *db.Job) string {
	logPath := job.AgentLog.String
	if logPath != "" && fileExists(logPath) {
		return logPath
	}

	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".agent-minder", "agents")

	candidates := []string{
		filepath.Join(base, fmt.Sprintf("%s-%s.log", job.DeploymentID, job.Name)),
	}
	if job.IssueNumber > 0 {
		candidates = append(candidates,
			filepath.Join(base, fmt.Sprintf("%s-issue-%d.log", job.DeploymentID, job.IssueNumber)),
		)
	}

	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return ""
}
