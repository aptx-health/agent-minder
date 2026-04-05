package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/atotto/clipboard"

	"github.com/aptx-health/agent-minder/internal/db"
	gitpkg "github.com/aptx-health/agent-minder/internal/git"
	"github.com/aptx-health/agent-minder/internal/picker"
	"github.com/spf13/cobra"
)

var checkoutCmd = &cobra.Command{
	Use:   "checkout [issue-or-job-id]",
	Short: "Check out an agent's worktree for review",
	Long: `Opens an agent's worktree so you can review and test its work.

If no argument is given, presents an interactive picker of completed jobs
for the current repository.

If the worktree no longer exists on disk, it is recreated from the remote branch.

Examples:
  minder checkout              # interactive picker
  minder checkout #42          # most recent job for issue 42
  minder checkout --job 7      # by job ID`,
	Args: cobra.MaximumNArgs(1),
	RunE: runCheckout,
}

var (
	flagCheckoutRepo string
	flagCheckoutJob  int64
)

func init() {
	rootCmd.AddCommand(checkoutCmd)
	checkoutCmd.Flags().StringVar(&flagCheckoutRepo, "repo", ".", "Repository directory")
	checkoutCmd.Flags().Int64Var(&flagCheckoutJob, "job", 0, "Job ID (skip picker)")
}

func runCheckout(cmd *cobra.Command, args []string) error {
	repoDir, err := resolveRepoDir(flagCheckoutRepo)
	if err != nil {
		return fmt.Errorf("resolve repo: %w", err)
	}

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	store := db.NewStore(conn)
	defer func() { _ = store.Close() }()

	owner, repo, err := resolveOwnerRepo(repoDir)
	if err != nil {
		return fmt.Errorf("resolve owner/repo: %w", err)
	}

	var job *db.Job

	// Direct job ID.
	if flagCheckoutJob > 0 {
		job, err = store.GetJob(flagCheckoutJob)
		if err != nil {
			return fmt.Errorf("job %d not found", flagCheckoutJob)
		}
	}

	// Issue number from arg (e.g., "#42" or "42").
	if job == nil && len(args) > 0 {
		arg := strings.TrimPrefix(args[0], "#")
		issueNum, parseErr := strconv.Atoi(arg)
		if parseErr != nil {
			return fmt.Errorf("invalid argument %q — use #<issue> or a job ID", args[0])
		}
		job, err = findMostRecentJobForIssue(store, owner, repo, issueNum)
		if err != nil {
			return err
		}
	}

	// Interactive picker.
	if job == nil {
		jobs, err := store.GetJobsByRepo(owner, repo)
		if err != nil {
			return fmt.Errorf("list jobs: %w", err)
		}

		// Filter to non-queued jobs.
		var candidates []*db.Job
		for _, j := range jobs {
			if j.Status != db.StatusQueued && j.Status != db.StatusBlocked {
				candidates = append(candidates, j)
			}
		}

		if len(candidates) == 0 {
			fmt.Println("No completed jobs found for this repository.")
			return nil
		}

		job, err = picker.PickJob(candidates, fmt.Sprintf("Select a job (%s/%s)", owner, repo))
		if err != nil {
			return err
		}
	}

	// Warn if actively running.
	if job.Status == db.StatusRunning || job.Status == db.StatusReviewing {
		fmt.Printf("\n⚠ Warning: this job is currently %s — the agent is actively working.\n\n", job.Status)
	}

	return checkoutWorktree(repoDir, job)
}

func findMostRecentJobForIssue(store *db.Store, owner, repo string, issueNum int) (*db.Job, error) {
	jobs, err := store.GetJobsByRepo(owner, repo)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}

	// Find most recent job for this issue (jobs are already ordered by ID desc).
	var candidates []*db.Job
	for _, j := range jobs {
		if j.IssueNumber == issueNum {
			candidates = append(candidates, j)
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no jobs found for issue #%d", issueNum)
	}

	// If multiple (spike + autopilot), let user pick.
	if len(candidates) > 1 {
		return picker.PickJob(candidates, fmt.Sprintf("Multiple jobs for #%d — select one", issueNum))
	}

	return candidates[0], nil
}

func checkoutWorktree(repoDir string, job *db.Job) error {
	branch := job.Branch.String
	if branch == "" {
		// Construct branch from job name.
		branch = fmt.Sprintf("agent/%s", job.Name)
	}

	worktreePath := job.WorktreePath.String

	// Check if worktree exists on disk.
	if worktreePath != "" && dirExistsCheckout(worktreePath) {
		return presentWorktree(worktreePath, branch, job)
	}

	// Worktree is gone — recreate from the branch.
	fmt.Printf("Worktree not found, recreating from branch %s...\n", branch)

	// Fetch latest and prune stale worktree bookkeeping.
	_ = gitpkg.Fetch(repoDir)
	_ = gitpkg.WorktreePrune(repoDir)

	// Create worktree path if we don't have one.
	if worktreePath == "" {
		home, _ := os.UserHomeDir()
		worktreePath = filepath.Join(home, ".agent-minder", "worktrees", "checkout", job.Name)
	}

	_ = os.MkdirAll(filepath.Dir(worktreePath), 0755)

	// Try strategies in order:
	// 1. Check out existing local branch into worktree.
	// 2. If branch only exists on remote, create from origin/<branch>.
	err := gitpkg.WorktreeAddExisting(repoDir, worktreePath, branch)
	if err != nil {
		// Local branch might exist but worktree add failed for other reasons.
		// Delete the local branch and recreate from remote.
		_ = gitpkg.DeleteBranch(repoDir, branch)
		err = gitpkg.WorktreeAdd(repoDir, worktreePath, branch, "origin/"+branch)
		if err != nil {
			return fmt.Errorf("could not create worktree from branch %s: %w", branch, err)
		}
	}

	return presentWorktree(worktreePath, branch, job)
}

func presentWorktree(path, branch string, job *db.Job) error {
	title := job.IssueTitle.String
	if title == "" {
		title = job.Name
	}

	fmt.Println()
	if job.IssueNumber > 0 {
		fmt.Printf("  Issue:    #%d — %s\n", job.IssueNumber, title)
	} else {
		fmt.Printf("  Job:      %s\n", title)
	}
	fmt.Printf("  Agent:    %s\n", job.Agent)
	fmt.Printf("  Status:   %s\n", job.Status)
	if job.PRNumber.Valid && job.PRNumber.Int64 > 0 {
		fmt.Printf("  PR:       #%d\n", job.PRNumber.Int64)
	}
	fmt.Printf("  Branch:   %s\n", branch)
	fmt.Printf("  Worktree: %s\n", path)
	fmt.Println()

	// Copy to clipboard.
	if err := clipboard.WriteAll(path); err == nil {
		fmt.Println("Path copied to clipboard.")
	} else {
		fmt.Printf("  cd %s\n", path)
	}

	return nil
}

// dirExists returns true if the path exists and is a directory.
func dirExistsCheckout(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
