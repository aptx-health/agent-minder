package cmd

import (
	"fmt"
	"time"

	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/deploy"
	"github.com/spf13/cobra"
)

var deployStatusCmd = &cobra.Command{
	Use:   "status <deploy-id>",
	Short: "Show status of a deployment",
	Args:  cobra.ExactArgs(1),
	RunE:  runDeployStatus,
}

func init() {
	deployCmd.AddCommand(deployStatusCmd)
}

func runDeployStatus(cmd *cobra.Command, args []string) error {
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
	}

	fmt.Printf("Deploy %s [%s]\n", id, status)
	if repoRef != "" {
		fmt.Printf("Repo:  %s\n", repoRef)
	}
	fmt.Println()

	// Track totals.
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
	return nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
