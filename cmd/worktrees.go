package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
	gitpkg "github.com/aptx-health/agent-minder/internal/git"
	"github.com/aptx-health/agent-minder/internal/reaper"
	"github.com/spf13/cobra"
)

var worktreesCmd = &cobra.Command{
	Use:   "worktrees",
	Short: "List agent worktrees with size, age, and reaper protection status",
	Long: `Lists every worktree under ~/.agent-minder/worktrees/ with its job status,
completion age, hold-file presence, and on-disk size.

Worktrees with .minder-hold are protected from the periodic reaper.
Recently-completed worktrees (< 1h) are also spared.`,
	RunE: runWorktrees,
}

var (
	pruneOlderThan string
	pruneDryRun    bool
	pruneStatuses  string
	pruneYes       bool
)

var worktreesPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove worktrees for completed jobs older than a threshold",
	Long: `Removes git worktrees + their on-disk directories for jobs in a terminal
state older than --older-than. Held worktrees (.minder-hold) are always spared.

Defaults: --older-than 7d --status done,reviewed,bailed`,
	RunE: runWorktreesPrune,
}

func init() {
	rootCmd.AddCommand(worktreesCmd)
	worktreesCmd.AddCommand(worktreesPruneCmd)
	worktreesPruneCmd.Flags().StringVar(&pruneOlderThan, "older-than", "7d", "Minimum completion age to prune (e.g. 7d, 48h)")
	worktreesPruneCmd.Flags().BoolVar(&pruneDryRun, "dry-run", false, "Show what would be removed without removing")
	worktreesPruneCmd.Flags().StringVar(&pruneStatuses, "status", "done,reviewed,bailed", "Comma-separated job statuses eligible for prune")
	worktreesPruneCmd.Flags().BoolVarP(&pruneYes, "yes", "y", false, "Skip confirmation prompt")
}

func runWorktreesPrune(_ *cobra.Command, _ []string) error {
	threshold, err := parseAgeFlag(pruneOlderThan)
	if err != nil {
		return fmt.Errorf("--older-than: %w", err)
	}
	statusSet := map[string]bool{}
	for _, s := range strings.Split(pruneStatuses, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			statusSet[s] = true
		}
	}

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return err
	}
	store := db.NewStore(conn)
	defer func() { _ = store.Close() }()

	deploys, err := store.ListDeployments()
	if err != nil {
		return err
	}
	repoByDeploy := map[string]string{}
	jobByPath := map[string]*db.Job{}
	deployByJob := map[int64]string{}
	for _, d := range deploys {
		repoByDeploy[d.ID] = d.RepoDir
		jobs, _ := store.GetJobs(d.ID)
		for _, j := range jobs {
			if j.WorktreePath.Valid && j.WorktreePath.String != "" {
				jobByPath[j.WorktreePath.String] = j
				deployByJob[j.ID] = d.ID
			}
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".agent-minder", "worktrees")
	rows := collectWorktrees(root, jobByPath)

	type candidate struct {
		row     worktreeRow
		job     *db.Job
		repoDir string
		ageStr  string
	}
	var candidates []candidate
	var skippedHeld, skippedYoung, skippedStatus, skippedOrphan int
	for _, r := range rows {
		if r.Held {
			skippedHeld++
			continue
		}
		j, ok := jobByPath[r.Path]
		if !ok {
			skippedOrphan++
			continue
		}
		if !statusSet[j.Status] {
			skippedStatus++
			continue
		}
		if !j.CompletedAt.Valid || time.Since(j.CompletedAt.Time) < threshold {
			skippedYoung++
			continue
		}
		repoDir := repoByDeploy[deployByJob[j.ID]]
		if repoDir == "" {
			skippedOrphan++
			continue
		}
		candidates = append(candidates, candidate{
			row:     r,
			job:     j,
			repoDir: repoDir,
			ageStr:  humanAge(time.Since(j.CompletedAt.Time)),
		})
	}

	if len(candidates) == 0 {
		fmt.Printf("nothing to prune (held=%d young=%d wrong-status=%d orphan/no-repo=%d)\n",
			skippedHeld, skippedYoung, skippedStatus, skippedOrphan)
		return nil
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].row.SizeBytes > candidates[j].row.SizeBytes })

	var totalBytes int64
	fmt.Printf("%-7s  %-10s  %-10s  %-8s  %s\n", "ISSUE", "STATUS", "AGE", "SIZE", "PATH")
	for _, c := range candidates {
		totalBytes += c.row.SizeBytes
		issue := "-"
		if c.row.IssueNum > 0 {
			issue = fmt.Sprintf("#%d", c.row.IssueNum)
		}
		fmt.Printf("%-7s  %-10s  %-10s  %-8s  %s\n",
			issue, c.job.Status, c.ageStr, c.row.SizeHuman, c.row.Path)
	}
	fmt.Printf("\n%d worktree(s), %s reclaimable (held=%d young=%d wrong-status=%d orphan/no-repo=%d)\n",
		len(candidates), humanBytes(totalBytes), skippedHeld, skippedYoung, skippedStatus, skippedOrphan)

	if pruneDryRun {
		fmt.Println("\n(dry-run; no changes made)")
		return nil
	}

	if !pruneYes {
		fmt.Print("\nProceed with removal? [y/N]: ")
		var resp string
		_, _ = fmt.Scanln(&resp)
		if !strings.EqualFold(strings.TrimSpace(resp), "y") {
			fmt.Println("aborted")
			return nil
		}
	}

	var removed, failed int
	for _, c := range candidates {
		if err := gitpkg.WorktreeRemove(c.repoDir, c.row.Path); err != nil {
			fmt.Printf("FAIL  %s: %v\n", c.row.Path, err)
			failed++
			continue
		}
		// Clear stored worktree path so it doesn't show up next time.
		_ = store.ClearJobWorktree(c.job.ID)
		removed++
	}
	fmt.Printf("\nremoved %d, failed %d\n", removed, failed)
	return nil
}

// parseAgeFlag accepts Go-duration syntax plus a "d" suffix for days (e.g. "7d").
func parseAgeFlag(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		var days float64
		if _, err := fmt.Sscanf(s, "%fd", &days); err != nil {
			return 0, err
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	return time.ParseDuration(s)
}

type worktreeRow struct {
	Path      string
	DeployID  string
	IssueNum  int
	JobStatus string
	Completed time.Time
	HasJob    bool
	Held      bool
	SizeBytes int64
	SizeHuman string
}

func runWorktrees(_ *cobra.Command, _ []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".agent-minder", "worktrees")

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return err
	}
	store := db.NewStore(conn)
	defer func() { _ = store.Close() }()

	// Index jobs by worktree path for lookup.
	jobByPath := map[string]*db.Job{}
	deploys, _ := store.ListDeployments()
	for _, d := range deploys {
		jobs, _ := store.GetJobs(d.ID)
		for _, j := range jobs {
			if j.WorktreePath.Valid && j.WorktreePath.String != "" {
				jobByPath[j.WorktreePath.String] = j
			}
		}
	}

	rows := collectWorktrees(root, jobByPath)
	if len(rows) == 0 {
		fmt.Println("no worktrees found under", root)
		return nil
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].SizeBytes > rows[j].SizeBytes })

	var totalBytes, heldBytes int64
	fmt.Printf("%-7s  %-10s  %-12s  %-8s  %-6s  %s\n", "ISSUE", "STATUS", "COMPLETED", "SIZE", "HOLD", "PATH")
	for _, r := range rows {
		totalBytes += r.SizeBytes
		if r.Held {
			heldBytes += r.SizeBytes
		}
		issue := "-"
		if r.IssueNum > 0 {
			issue = fmt.Sprintf("#%d", r.IssueNum)
		}
		status := r.JobStatus
		if !r.HasJob {
			status = "orphan"
		}
		completed := "-"
		if !r.Completed.IsZero() {
			completed = humanAge(time.Since(r.Completed)) + " ago"
		}
		hold := ""
		if r.Held {
			hold = "HOLD"
		}
		fmt.Printf("%-7s  %-10s  %-12s  %-8s  %-6s  %s\n",
			issue, status, completed, r.SizeHuman, hold, r.Path)
	}
	fmt.Printf("\n%d worktrees, %s total (%s held)\n",
		len(rows), humanBytes(totalBytes), humanBytes(heldBytes))
	return nil
}

func collectWorktrees(root string, jobByPath map[string]*db.Job) []worktreeRow {
	var rows []worktreeRow
	deployDirs, err := os.ReadDir(root)
	if err != nil {
		return rows
	}
	for _, dd := range deployDirs {
		if !dd.IsDir() {
			continue
		}
		deployID := dd.Name()
		deployPath := filepath.Join(root, deployID)
		issueDirs, err := os.ReadDir(deployPath)
		if err != nil {
			continue
		}
		for _, id := range issueDirs {
			if !id.IsDir() {
				continue
			}
			path := filepath.Join(deployPath, id.Name())
			row := worktreeRow{
				Path:     path,
				DeployID: deployID,
				Held:     reaper.IsHeld(path),
			}
			if n, ok := parseIssueDir(id.Name()); ok {
				row.IssueNum = n
			}
			if j, ok := jobByPath[path]; ok {
				row.HasJob = true
				row.JobStatus = j.Status
				if j.CompletedAt.Valid {
					row.Completed = j.CompletedAt.Time
				}
			}
			row.SizeBytes = dirSize(path)
			row.SizeHuman = humanBytes(row.SizeBytes)
			rows = append(rows, row)
		}
	}
	return rows
}

func parseIssueDir(name string) (int, bool) {
	if !strings.HasPrefix(name, "issue-") {
		return 0, false
	}
	var n int
	_, err := fmt.Sscanf(name, "issue-%d", &n)
	return n, err == nil
}

// dirSize shells out to `du -sk` for speed and accuracy (apparent-vs-real
// disk usage, hard links, etc. handled correctly).
func dirSize(path string) int64 {
	out, err := exec.Command("du", "-sk", path).Output()
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0
	}
	var kb int64
	_, _ = fmt.Sscanf(fields[0], "%d", &kb)
	return kb * 1024
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
