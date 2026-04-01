package cmd

import (
	"fmt"

	"github.com/aptx-health/agent-minder/internal/daemon"
	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status [deploy-id]",
	Short: "Show deployment status",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runStatus,
}

var (
	flagRemote    string
	flagStatusKey string
)

func init() {
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().StringVar(&flagRemote, "remote", "", "Remote daemon address (host:port)")
	statusCmd.Flags().StringVar(&flagStatusKey, "api-key", "", "API key for remote access")
}

func runStatus(cmd *cobra.Command, args []string) error {
	// Remote mode.
	if flagRemote != "" {
		client := daemon.NewClient("http://"+flagRemote, flagStatusKey)
		status, err := client.GetStatus()
		if err != nil {
			return fmt.Errorf("fetch status: %w", err)
		}
		fmt.Printf("Deploy: %s (PID %d, up %ds)\n", status.DeployID, status.PID, status.UptimeSec)
		fmt.Printf("Budget: $%.2f / $%.2f", status.TotalSpent, status.TotalBudget)
		if status.BudgetPaused {
			fmt.Print(" [PAUSED]")
		}
		fmt.Println()

		tasks, err := client.GetTasks()
		if err != nil {
			return fmt.Errorf("fetch tasks: %w", err)
		}
		printTasks(tasks)
		return nil
	}

	// Local mode — read from DB.
	if len(args) == 0 {
		return listAllDeployments()
	}

	deployID := args[0]
	alive, pid := daemon.IsRunning(deployID)

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return err
	}
	store := db.NewStore(conn)
	defer func() { _ = store.Close() }()

	deploy, err := store.GetDeployment(deployID)
	if err != nil {
		return fmt.Errorf("deployment %s not found", deployID)
	}

	fmt.Printf("Deploy: %s (%s/%s, %s mode)\n", deployID, deploy.Owner, deploy.Repo, deploy.Mode)
	if alive {
		fmt.Printf("Status: running (PID %d)\n", pid)
	} else {
		fmt.Println("Status: stopped")
	}

	spent, _ := store.TotalSpend(deployID)
	fmt.Printf("Budget: $%.2f / $%.2f\n", spent, deploy.TotalBudgetUSD)

	tasks, _ := store.GetTasks(deployID)
	fmt.Printf("Tasks: %d\n\n", len(tasks))
	for _, t := range tasks {
		pr := ""
		if t.PRNumber.Valid {
			pr = fmt.Sprintf(" PR#%d", t.PRNumber.Int64)
		}
		cost := ""
		if t.CostUSD > 0 {
			cost = fmt.Sprintf(" $%.2f", t.CostUSD)
		}
		fmt.Printf("  #%-5d %-30s %-10s%s%s\n", t.IssueNumber, t.IssueTitle.String, t.Status, pr, cost)
	}

	return nil
}

func listAllDeployments() error {
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

	if len(deploys) == 0 {
		fmt.Println("No deployments found.")
		return nil
	}

	for _, d := range deploys {
		alive, _ := daemon.IsRunning(d.ID)
		status := "stopped"
		if alive {
			status = "running"
		}
		tasks, _ := store.GetTasks(d.ID)
		fmt.Printf("%-10s %s/%s  %-8s  %d tasks  %s\n",
			d.ID, d.Owner, d.Repo, status, len(tasks), d.StartedAt.Format("2006-01-02 15:04"))
	}
	return nil
}

func printTasks(tasks []daemon.TaskResponse) {
	if len(tasks) == 0 {
		fmt.Println("No tasks.")
		return
	}
	fmt.Println()
	for _, t := range tasks {
		pr := ""
		if t.PRNumber > 0 {
			pr = fmt.Sprintf(" PR#%d", t.PRNumber)
		}
		cost := ""
		if t.CostUSD > 0 {
			cost = fmt.Sprintf(" $%.2f", t.CostUSD)
		}
		fmt.Printf("  #%-5d %-30s %-10s%s%s\n", t.IssueNumber, t.IssueTitle, t.Status, pr, cost)
	}
}
