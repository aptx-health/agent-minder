package cmd

import (
	"fmt"
	"time"

	"github.com/dustinlange/agent-minder/internal/api"
	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/deploy"
	"github.com/spf13/cobra"
)

var deployStatusCmd = &cobra.Command{
	Use:   "status <deploy-id>",
	Short: "Show status of a deployment",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDeployStatus,
}

func init() {
	deployCmd.AddCommand(deployStatusCmd)
}

func runDeployStatus(_ *cobra.Command, args []string) error {
	// Remote mode: query the daemon's HTTP API.
	if client := remoteClient(); client != nil {
		return runDeployStatusRemote(client)
	}

	if len(args) == 0 {
		return fmt.Errorf("deploy ID required (or use --remote to query a remote daemon)")
	}
	id := args[0]

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	project, err := store.GetProject(id)
	if err != nil {
		return fmt.Errorf("deploy %q not found", id)
	}
	if !project.IsDeploy {
		return fmt.Errorf("%q is not a deploy project", id)
	}

	tasks, err := store.GetAutopilotTasks(project.ID)
	if err != nil {
		return fmt.Errorf("get tasks: %w", err)
	}

	// Determine repo from tasks.
	repoRef := ""
	if len(tasks) > 0 && tasks[0].Owner != "" {
		repoRef = tasks[0].Owner + "/" + tasks[0].Repo
	}

	// Check daemon status.
	alive, _ := deploy.IsRunning(id)
	status := "completed"
	if alive {
		status = "running"
	} else if deploy.WasCrashShutdown(id) {
		status = "crashed"
	}

	fmt.Printf("Deploy %s [%s]\n", id, status)
	if alive {
		if hb := deploy.ReadHeartbeat(id); !hb.IsZero() {
			ago := time.Since(hb).Truncate(time.Second)
			fmt.Printf("Last heartbeat: %s ago\n", ago)
		}
	} else if status == "crashed" {
		fmt.Printf("Stale PID file detected — daemon exited uncleanly.\n")
		fmt.Printf("Relaunch with: agent-minder deploy respawn %s\n", id)
	}
	if repoRef != "" {
		fmt.Printf("Repo:  %s\n", repoRef)
	}
	fmt.Println()

	printTaskTable(tasks)

	// Show budget ceiling info if configured.
	if project.TotalBudgetUSD > 0 {
		var totalCost float64
		for _, t := range tasks {
			totalCost += t.CostUSD
		}
		pct := 0.0
		if project.TotalBudgetUSD > 0 {
			pct = (totalCost / project.TotalBudgetUSD) * 100
		}
		fmt.Printf("Budget ceiling: $%.2f / $%.2f (%.0f%%)\n", totalCost, project.TotalBudgetUSD, pct)
	}
	return nil
}

func runDeployStatusRemote(client *api.Client) error {
	status, err := client.GetStatus()
	if err != nil {
		return fmt.Errorf("remote: %w", err)
	}

	tasks, err := client.GetTasks()
	if err != nil {
		return fmt.Errorf("remote: %w", err)
	}

	// Determine repo from tasks.
	repoRef := ""
	if len(tasks) > 0 && tasks[0].Owner != "" {
		repoRef = tasks[0].Owner + "/" + tasks[0].Repo
	}

	statusLabel := "running"
	if !status.Alive {
		statusLabel = "completed"
	}

	fmt.Printf("Deploy %s [%s]\n", status.DeployID, statusLabel)
	if status.Alive && status.UptimeSec > 0 {
		fmt.Printf("Uptime: %s\n", formatDuration(time.Duration(status.UptimeSec)*time.Second))
	}
	if repoRef != "" {
		fmt.Printf("Repo:  %s\n", repoRef)
	}
	fmt.Println()

	printRemoteTaskTable(tasks)
	return nil
}

// printTaskTable renders the task table and summary from local db.AutopilotTask records.
func printTaskTable(tasks []db.AutopilotTask) {
	var totalCost float64
	counts := map[string]int{}
	for _, t := range tasks {
		counts[t.Status]++
		totalCost += t.CostUSD

		costStr := "-"
		if t.CostUSD > 0 {
			costStr = fmt.Sprintf("$%.2f", t.CostUSD)
		}
		elapsed := "-"
		if t.StartedAt != "" {
			start, err := time.Parse("2006-01-02 15:04:05", t.StartedAt)
			if err == nil {
				var end time.Time
				if t.CompletedAt != "" {
					if e, err := time.Parse("2006-01-02 15:04:05", t.CompletedAt); err == nil {
						end = e
					}
				}
				if end.IsZero() && t.Status == "running" {
					end = time.Now().UTC()
				}
				if !end.IsZero() {
					d := end.Sub(start)
					elapsed = fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
				}
			}
		}
		prStr := ""
		if t.PRNumber > 0 {
			prStr = fmt.Sprintf("PR #%d", t.PRNumber)
		}
		failStr := ""
		if t.FailureReason != "" {
			failStr = fmt.Sprintf(" (%s)", t.FailureReason)
		}
		fmt.Printf("  #%-6d %-30s %-10s %-8s %6s  %s%s\n",
			t.IssueNumber, truncateStr(t.IssueTitle, 30), t.Status, prStr, costStr, elapsed, failStr)
	}

	// Summary line.
	fmt.Println()
	printStatusSummary(counts, totalCost)
}

// printRemoteTaskTable renders the task table from remote API TaskResponse records.
func printRemoteTaskTable(tasks []api.TaskResponse) {
	var totalCost float64
	counts := map[string]int{}
	for _, t := range tasks {
		counts[t.Status]++
		totalCost += t.CostUSD

		costStr := "-"
		if t.CostUSD > 0 {
			costStr = fmt.Sprintf("$%.2f", t.CostUSD)
		}
		elapsed := "-"
		if t.StartedAt != "" {
			start, err := time.Parse("2006-01-02 15:04:05", t.StartedAt)
			if err == nil {
				var end time.Time
				if t.CompletedAt != "" {
					if e, err := time.Parse("2006-01-02 15:04:05", t.CompletedAt); err == nil {
						end = e
					}
				}
				if end.IsZero() && t.Status == "running" {
					end = time.Now().UTC()
				}
				if !end.IsZero() {
					d := end.Sub(start)
					elapsed = fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
				}
			}
		}
		prStr := ""
		if t.PRNumber > 0 {
			prStr = fmt.Sprintf("PR #%d", t.PRNumber)
		}
		failStr := ""
		if t.FailureReason != "" {
			failStr = fmt.Sprintf(" (%s)", t.FailureReason)
		}
		fmt.Printf("  #%-6d %-30s %-10s %-8s %6s  %s%s\n",
			t.IssueNumber, truncateStr(t.IssueTitle, 30), t.Status, prStr, costStr, elapsed, failStr)
	}

	// Summary line.
	fmt.Println()
	printStatusSummary(counts, totalCost)
}

// printStatusSummary prints the cost and task status summary line.
func printStatusSummary(counts map[string]int, totalCost float64) {
	parts := ""
	for _, s := range []string{"running", "queued", "review", "done", "bailed", "failed", "stopped"} {
		if c := counts[s]; c > 0 {
			if parts != "" {
				parts += ", "
			}
			parts += fmt.Sprintf("%d %s", c, s)
		}
	}
	fmt.Printf("Cost: $%.2f | %s\n", totalCost, parts)
}

// formatDuration renders a duration as a human-readable string.
func formatDuration(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours()/24), int(d.Hours())%24)
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
