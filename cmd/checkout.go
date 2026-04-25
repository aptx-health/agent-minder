package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/atotto/clipboard"

	"github.com/aptx-health/agent-minder/internal/daemon"
	"github.com/aptx-health/agent-minder/internal/db"
	gitpkg "github.com/aptx-health/agent-minder/internal/git"
	"github.com/aptx-health/agent-minder/internal/picker"
	"github.com/spf13/cobra"
)

var checkoutCmd = &cobra.Command{
	Use:   "checkout [issue-or-job-id]",
	Short: "Interact with an agent's work",
	Long: `Select a job and choose what to do: checkout the worktree, resume with
Claude, or view logs.

If no argument is given, presents an interactive picker of completed jobs
for the current repository, then an action menu.

Examples:
  minder checkout              # interactive picker → action menu
  minder checkout #42          # most recent job for issue 42
  minder checkout --job 7      # by job ID
  minder checkout -g spike     # filter to spike jobs
  minder checkout -g 529       # find issue #529 or PR #529`,
	Args: cobra.MaximumNArgs(1),
	RunE: runCheckout,
}

var (
	flagCheckoutRepo   string
	flagCheckoutJob    int64
	flagCheckoutRemote string
	flagCheckoutKey    string
	flagCheckoutGrep   string
)

func init() {
	rootCmd.AddCommand(checkoutCmd)
	checkoutCmd.Flags().StringVar(&flagCheckoutRepo, "repo", ".", "Repository directory")
	checkoutCmd.Flags().Int64Var(&flagCheckoutJob, "job", 0, "Job ID (skip picker)")
	checkoutCmd.Flags().StringVar(&flagCheckoutRemote, "remote", "", "Remote daemon address (host:port)")
	checkoutCmd.Flags().StringVar(&flagCheckoutKey, "api-key", "", "API key for remote access")
	checkoutCmd.Flags().StringVarP(&flagCheckoutGrep, "grep", "g", "", "Filter jobs by substring (matches issue, agent, title, PR, status)")
}

func runCheckout(cmd *cobra.Command, args []string) error {
	if flagCheckoutRemote != "" {
		return runCheckoutRemote(args)
	}

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

	detectRepoRename(store, repoDir, owner, repo)

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

		candidates = picker.FilterJobs(candidates, flagCheckoutGrep)

		if len(candidates) == 0 {
			fmt.Println("No matching jobs found for this repository.")
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

	// Action menu.
	action, err := picker.PickAction(job)
	if err != nil {
		return err
	}

	return executeAction(action, repoDir, job)
}

func executeAction(action picker.Action, repoDir string, job *db.Job) error {
	switch action {
	case picker.ActionCheckout:
		return checkoutWorktree(repoDir, job)

	case picker.ActionResume:
		worktreePath, _, err := ensureWorktree(repoDir, job)
		if err != nil {
			return err
		}
		prompt := buildResumePrompt(job)
		return launchClaude(worktreePath, prompt)

	case picker.ActionLogs:
		logPath := resolveLogPath(job)
		if logPath == "" {
			return fmt.Errorf("log not found for job %s (deploy %s)", job.Name, job.DeploymentID)
		}
		f, err := os.Open(logPath)
		if err != nil {
			return fmt.Errorf("open log: %w", err)
		}
		defer func() { _ = f.Close() }()
		return streamLog(f, false)

	case picker.ActionOpenIssue:
		url := fmt.Sprintf("https://github.com/%s/%s/issues/%d", job.Owner, job.Repo, job.IssueNumber)
		return openBrowser(url)

	case picker.ActionOpenPR:
		url := fmt.Sprintf("https://github.com/%s/%s/pull/%d", job.Owner, job.Repo, job.PRNumber.Int64)
		return openBrowser(url)
	}
	return nil
}

func openBrowser(url string) error {
	fmt.Println(url)
	return exec.Command("open", url).Start()
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
	worktreePath := job.WorktreePath.String

	// If worktree is on disk, use the recorded branch (or our best guess) and present it.
	if worktreePath != "" && dirExistsCheckout(worktreePath) {
		branch := job.Branch.String
		if branch == "" {
			branch = candidateBranchNames(job)[0]
		}
		return presentWorktree(worktreePath, branch, job)
	}

	// Worktree is gone — fetch and pick the first branch that actually exists.
	candidates := candidateBranchNames(job)
	fmt.Printf("Worktree not found, recreating from branch %s...\n", candidates[0])

	_ = gitpkg.Fetch(repoDir)
	_ = gitpkg.WorktreePrune(repoDir)

	branch, err := pickExistingBranch(repoDir, candidates)
	if err != nil {
		return err
	}

	if worktreePath == "" {
		home, _ := os.UserHomeDir()
		worktreePath = filepath.Join(home, ".agent-minder", "worktrees", "checkout", job.Name)
	}
	_ = os.MkdirAll(filepath.Dir(worktreePath), 0755)

	if err := createWorktreeFromBranch(repoDir, worktreePath, branch); err != nil {
		return err
	}
	return presentWorktree(worktreePath, branch, job)
}

// candidateBranchNames returns branch names to try in priority order, mirroring
// the conventions in supervisor.newSlotContext. Issue-bound jobs use
// `agent/issue-<N>` regardless of the agent name; proactive jobs use
// `agent/<job.Name>`.
func candidateBranchNames(job *db.Job) []string {
	var names []string
	seen := map[string]bool{}
	add := func(b string) {
		if b == "" || seen[b] {
			return
		}
		seen[b] = true
		names = append(names, b)
	}
	add(job.Branch.String)
	if job.IssueNumber > 0 {
		add(fmt.Sprintf("agent/issue-%d", job.IssueNumber))
	}
	if job.Name != "" {
		add(fmt.Sprintf("agent/%s", job.Name))
	}
	return names
}

// pickExistingBranch returns the first candidate that exists locally or on origin.
func pickExistingBranch(repoDir string, candidates []string) (string, error) {
	for _, b := range candidates {
		if gitpkg.BranchExists(repoDir, b) {
			return b, nil
		}
	}
	return "", fmt.Errorf("no matching branch found locally or on origin (tried: %s)", strings.Join(candidates, ", "))
}

// candidateBranchNamesRemote is the JobResponse equivalent of candidateBranchNames.
func candidateBranchNamesRemote(j *daemon.JobResponse) []string {
	var names []string
	seen := map[string]bool{}
	add := func(b string) {
		if b == "" || seen[b] {
			return
		}
		seen[b] = true
		names = append(names, b)
	}
	add(j.Branch)
	if j.IssueNumber > 0 {
		add(fmt.Sprintf("agent/issue-%d", j.IssueNumber))
	}
	if j.Name != "" {
		add(fmt.Sprintf("agent/%s", j.Name))
	}
	return names
}

// createWorktreeFromBranch creates a worktree at worktreePath for the given branch,
// preferring an existing local branch and falling back to origin/<branch>.
func createWorktreeFromBranch(repoDir, worktreePath, branch string) error {
	if err := gitpkg.WorktreeAddExisting(repoDir, worktreePath, branch); err == nil {
		return nil
	}
	// Local branch may be missing or in a bad state; recreate from remote.
	_ = gitpkg.DeleteBranch(repoDir, branch)
	if err := gitpkg.WorktreeAdd(repoDir, worktreePath, branch, "origin/"+branch); err != nil {
		return fmt.Errorf("could not create worktree from branch %s: %w", branch, err)
	}
	return nil
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

// dirExistsCheckout returns true if the path exists and is a directory.
func dirExistsCheckout(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// runCheckoutRemote handles checkout from a remote daemon — fetches branch info
// from the API, then creates a local worktree from origin/<branch>.
func runCheckoutRemote(args []string) error {
	repoDir, err := resolveRepoDir(flagCheckoutRepo)
	if err != nil {
		return fmt.Errorf("resolve repo: %w", err)
	}

	addr := flagCheckoutRemote
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	client := daemon.NewClient(addr, flagCheckoutKey)

	var selected *daemon.JobResponse

	if flagCheckoutJob > 0 {
		j, err := client.GetJob(flagCheckoutJob)
		if err != nil {
			return fmt.Errorf("job %d not found: %w", flagCheckoutJob, err)
		}
		selected = j
	}

	// Issue from arg.
	if selected == nil && len(args) > 0 {
		arg := strings.TrimPrefix(args[0], "#")
		issueNum, parseErr := strconv.Atoi(arg)
		if parseErr != nil {
			return fmt.Errorf("invalid argument %q", args[0])
		}
		jobs, err := client.GetJobs()
		if err != nil {
			return fmt.Errorf("fetch jobs: %w", err)
		}
		var candidates []daemon.JobResponse
		for _, j := range jobs {
			if j.IssueNumber == issueNum {
				candidates = append(candidates, j)
			}
		}
		if len(candidates) == 0 {
			return fmt.Errorf("no jobs found for issue #%d", issueNum)
		}
		if len(candidates) > 1 {
			selected, err = picker.PickRemoteJob(candidates, fmt.Sprintf("Multiple jobs for #%d", issueNum))
			if err != nil {
				return err
			}
		} else {
			selected = &candidates[0]
		}
	}

	// Interactive picker.
	if selected == nil {
		jobs, err := client.GetJobs()
		if err != nil {
			return fmt.Errorf("fetch jobs: %w", err)
		}
		var candidates []daemon.JobResponse
		for _, j := range jobs {
			if j.Status != "queued" && j.Status != "blocked" {
				candidates = append(candidates, j)
			}
		}
		candidates = picker.FilterRemoteJobs(candidates, flagCheckoutGrep)
		if len(candidates) == 0 {
			fmt.Println("No matching jobs found on remote daemon.")
			return nil
		}
		selected, err = picker.PickRemoteJob(candidates, "Select a job")
		if err != nil {
			return err
		}
	}

	if selected.Status == "running" || selected.Status == "reviewing" {
		fmt.Printf("\nWarning: this job is currently %s — the agent is actively working.\n\n", selected.Status)
	}

	candidates := candidateBranchNamesRemote(selected)
	if len(candidates) == 0 {
		return fmt.Errorf("job has no branch and no issue/name to derive one from — cannot create worktree")
	}

	fmt.Printf("Fetching branch %s from remote...\n", candidates[0])
	_ = gitpkg.Fetch(repoDir)
	_ = gitpkg.WorktreePrune(repoDir)

	branch, err := pickExistingBranch(repoDir, candidates)
	if err != nil {
		return err
	}

	home, _ := os.UserHomeDir()
	worktreePath := filepath.Join(home, ".agent-minder", "worktrees", "checkout", selected.Name)
	_ = os.MkdirAll(filepath.Dir(worktreePath), 0755)

	// Remove stale worktree at this path.
	if dirExistsCheckout(worktreePath) {
		_ = gitpkg.WorktreeRemove(repoDir, worktreePath)
	}

	// Delete local branch if it exists, recreate from origin.
	_ = gitpkg.DeleteBranch(repoDir, branch)
	if err := gitpkg.WorktreeAdd(repoDir, worktreePath, branch, "origin/"+branch); err != nil {
		return fmt.Errorf("could not create worktree from branch %s: %w", branch, err)
	}

	title := selected.Title
	fmt.Println()
	if selected.IssueNumber > 0 {
		fmt.Printf("  Issue:    #%d — %s\n", selected.IssueNumber, title)
	} else {
		fmt.Printf("  Job:      %s\n", title)
	}
	fmt.Printf("  Agent:    %s\n", selected.Agent)
	fmt.Printf("  Status:   %s\n", selected.Status)
	if selected.PRNumber > 0 {
		fmt.Printf("  PR:       #%d\n", selected.PRNumber)
	}
	fmt.Printf("  Branch:   %s\n", branch)
	fmt.Printf("  Worktree: %s\n", worktreePath)
	fmt.Println()

	if err := clipboard.WriteAll(worktreePath); err == nil {
		fmt.Println("Path copied to clipboard.")
	} else {
		fmt.Printf("  cd %s\n", worktreePath)
	}

	return nil
}
