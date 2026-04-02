package cmd

import (
	"encoding/json"
	"fmt"
	"os"

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
	flagJSON      bool
)

func init() {
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().StringVar(&flagRemote, "remote", "", "Remote daemon address (host:port)")
	statusCmd.Flags().StringVar(&flagStatusKey, "api-key", "", "API key for remote access")
	statusCmd.Flags().BoolVar(&flagJSON, "json", false, "Output structured JSON instead of human-readable text")
}

// statusJSON is the structured output for --json mode.
type statusJSON struct {
	DeployID string     `json:"deploy_id"`
	Alive    bool       `json:"alive"`
	PID      int        `json:"pid,omitempty"`
	Mode     string     `json:"mode,omitempty"`
	Owner    string     `json:"owner,omitempty"`
	Repo     string     `json:"repo,omitempty"`
	Budget   budgetJSON `json:"budget"`
	Jobs     []jobJSON  `json:"jobs"`
}

type budgetJSON struct {
	Spent float64 `json:"spent"`
	Total float64 `json:"total"`
}

type jobJSON struct {
	IssueNumber int     `json:"issue_number"`
	IssueTitle  string  `json:"issue_title"`
	Status      string  `json:"status"`
	PRNumber    int     `json:"pr_number,omitempty"`
	CostUSD     float64 `json:"cost_usd,omitempty"`
}

// printJSON marshals v as indented JSON and writes to stdout.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func runStatus(cmd *cobra.Command, args []string) error {
	// Remote mode.
	if flagRemote != "" {
		client := daemon.NewClient("http://"+flagRemote, flagStatusKey)
		status, err := client.GetStatus()
		if err != nil {
			return fmt.Errorf("fetch status: %w", err)
		}

		tasks, err := client.GetTasks()
		if err != nil {
			return fmt.Errorf("fetch tasks: %w", err)
		}

		if flagJSON {
			jobs := make([]jobJSON, len(tasks))
			for i, t := range tasks {
				jobs[i] = jobJSON{
					IssueNumber: t.IssueNumber,
					IssueTitle:  t.IssueTitle,
					Status:      t.Status,
					PRNumber:    t.PRNumber,
					CostUSD:     t.CostUSD,
				}
			}
			return printJSON(statusJSON{
				DeployID: status.DeployID,
				Alive:    true,
				PID:      status.PID,
				Budget:   budgetJSON{Spent: status.TotalSpent, Total: status.TotalBudget},
				Jobs:     jobs,
			})
		}

		fmt.Printf("Deploy: %s (PID %d, up %ds)\n", status.DeployID, status.PID, status.UptimeSec)
		fmt.Printf("Budget: $%.2f / $%.2f", status.TotalSpent, status.TotalBudget)
		if status.BudgetPaused {
			fmt.Print(" [PAUSED]")
		}
		fmt.Println()
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

	spent, _ := store.TotalSpend(deployID)
	tasks, _ := store.GetTasks(deployID)

	if flagJSON {
		pidVal := 0
		if alive {
			pidVal = pid
		}
		jobs := make([]jobJSON, len(tasks))
		for i, t := range tasks {
			pr := 0
			if t.PRNumber.Valid {
				pr = int(t.PRNumber.Int64)
			}
			jobs[i] = jobJSON{
				IssueNumber: t.IssueNumber,
				IssueTitle:  t.IssueTitle.String,
				Status:      t.Status,
				PRNumber:    pr,
				CostUSD:     t.CostUSD,
			}
		}
		return printJSON(statusJSON{
			DeployID: deployID,
			Alive:    alive,
			PID:      pidVal,
			Mode:     deploy.Mode,
			Owner:    deploy.Owner,
			Repo:     deploy.Repo,
			Budget:   budgetJSON{Spent: spent, Total: deploy.TotalBudgetUSD},
			Jobs:     jobs,
		})
	}

	fmt.Printf("Deploy: %s (%s/%s, %s mode)\n", deployID, deploy.Owner, deploy.Repo, deploy.Mode)
	if alive {
		fmt.Printf("Status: running (PID %d)\n", pid)
	} else {
		fmt.Println("Status: stopped")
	}

	fmt.Printf("Budget: $%.2f / $%.2f\n", spent, deploy.TotalBudgetUSD)

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
