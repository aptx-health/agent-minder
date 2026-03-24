package cmd

import (
	"fmt"
	"time"

	"github.com/dustinlange/agent-minder/internal/api"
	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/deploy"
	"github.com/spf13/cobra"
)

var deployListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all deployments",
	Args:  cobra.NoArgs,
	RunE:  runDeployList,
}

func init() {
	deployCmd.AddCommand(deployListCmd)
}

func runDeployList(_ *cobra.Command, _ []string) error {
	// Remote mode: query the daemon's HTTP API.
	if client := remoteClient(); client != nil {
		return runDeployListRemote(client)
	}

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	projects, err := store.ListDeployProjects()
	if err != nil {
		return fmt.Errorf("list deploys: %w", err)
	}

	if len(projects) == 0 {
		fmt.Println("No deployments found.")
		return nil
	}

	fmt.Printf("%-20s %-25s %7s  %-12s %s\n", "ID", "REPO", "ISSUES", "STATUS", "STARTED")
	for _, p := range projects {
		tasks, _ := store.GetAutopilotTasks(p.ID)

		// Determine repo.
		repoRef := ""
		if len(tasks) > 0 && tasks[0].Owner != "" {
			repoRef = tasks[0].Owner + "/" + tasks[0].Repo
		}

		// Summarize task status.
		alive, _ := deploy.IsRunning(p.Name)
		counts := map[string]int{}
		for _, t := range tasks {
			counts[t.Status]++
		}

		statusStr := "completed"
		if alive {
			running := counts["running"]
			if running > 0 {
				statusStr = fmt.Sprintf("%d running", running)
			} else {
				statusStr = "idle"
			}
		}

		// Time since creation.
		age := ""
		if p.CreatedAt != "" {
			created, err := time.Parse("2006-01-02 15:04:05", p.CreatedAt)
			if err == nil {
				d := time.Since(created)
				switch {
				case d < time.Hour:
					age = fmt.Sprintf("%dm ago", int(d.Minutes()))
				case d < 24*time.Hour:
					age = fmt.Sprintf("%dh ago", int(d.Hours()))
				default:
					age = fmt.Sprintf("%dd ago", int(d.Hours()/24))
				}
			}
		}

		fmt.Printf("%-20s %-25s %7d  %-12s %s\n",
			p.Name, truncate(repoRef, 25), len(tasks), statusStr, age)
	}
	return nil
}

func runDeployListRemote(client *api.Client) error {
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

	// Summarize task status.
	counts := map[string]int{}
	for _, t := range tasks {
		counts[t.Status]++
	}
	var statusStr string
	if !status.Alive {
		statusStr = "completed"
	} else if running := counts["running"]; running > 0 {
		statusStr = fmt.Sprintf("%d running", running)
	} else {
		statusStr = "idle"
	}

	// Uptime as age.
	age := ""
	if status.StartedAt != "" {
		started, err := time.Parse(time.RFC3339, status.StartedAt)
		if err == nil {
			d := time.Since(started)
			switch {
			case d < time.Hour:
				age = fmt.Sprintf("%dm ago", int(d.Minutes()))
			case d < 24*time.Hour:
				age = fmt.Sprintf("%dh ago", int(d.Hours()))
			default:
				age = fmt.Sprintf("%dd ago", int(d.Hours()/24))
			}
		}
	}

	fmt.Printf("%-20s %-25s %7s  %-12s %s\n", "ID", "REPO", "ISSUES", "STATUS", "STARTED")
	fmt.Printf("%-20s %-25s %7d  %-12s %s\n",
		status.DeployID, truncate(repoRef, 25), len(tasks), statusStr, age)
	return nil
}
