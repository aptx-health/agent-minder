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
	IssueNumber int     `json:"issue_number,omitempty"`
	Title       string  `json:"title"`
	Agent       string  `json:"agent,omitempty"`
	Status      string  `json:"status"`
	PRNumber    int     `json:"pr_number,omitempty"`
	CostUSD     float64 `json:"cost_usd,omitempty"`
}

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

		jobs, err := client.GetJobs()
		if err != nil {
			return fmt.Errorf("fetch jobs: %w", err)
		}

		if flagJSON {
			jj := make([]jobJSON, len(jobs))
			for i, j := range jobs {
				jj[i] = jobJSON{
					IssueNumber: j.IssueNumber,
					Title:       j.Title,
					Agent:       j.Agent,
					Status:      j.Status,
					PRNumber:    j.PRNumber,
					CostUSD:     j.CostUSD,
				}
			}
			return printJSON(statusJSON{
				DeployID: status.DeployID,
				Alive:    true,
				PID:      status.PID,
				Budget:   budgetJSON{Spent: status.TotalSpent, Total: status.TotalBudget},
				Jobs:     jj,
			})
		}

		fmt.Printf("Deploy: %s (PID %d, up %ds)\n", status.DeployID, status.PID, status.UptimeSec)
		fmt.Printf("Budget: $%.2f / $%.2f", status.TotalSpent, status.TotalBudget)
		if status.BudgetPaused {
			fmt.Print(" [PAUSED]")
		}
		fmt.Println()
		printJobs(jobs)
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
	jobs, _ := store.GetJobs(deployID)

	if flagJSON {
		pidVal := 0
		if alive {
			pidVal = pid
		}
		jj := make([]jobJSON, len(jobs))
		for i, j := range jobs {
			pr := 0
			if j.PRNumber.Valid {
				pr = int(j.PRNumber.Int64)
			}
			title := j.IssueTitle.String
			if title == "" {
				title = j.Name
			}
			jj[i] = jobJSON{
				IssueNumber: j.IssueNumber,
				Title:       title,
				Agent:       j.Agent,
				Status:      j.Status,
				PRNumber:    pr,
				CostUSD:     j.CostUSD,
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
			Jobs:     jj,
		})
	}

	fmt.Printf("Deploy: %s (%s/%s, %s mode)\n", deployID, deploy.Owner, deploy.Repo, deploy.Mode)
	if alive {
		fmt.Printf("Status: running (PID %d)\n", pid)
	} else {
		fmt.Println("Status: stopped")
	}

	fmt.Printf("Budget: $%.2f / $%.2f\n", spent, deploy.TotalBudgetUSD)

	fmt.Printf("Jobs: %d\n\n", len(jobs))
	for _, j := range jobs {
		pr := ""
		if j.PRNumber.Valid {
			pr = fmt.Sprintf(" PR#%d", j.PRNumber.Int64)
		}
		cost := ""
		if j.CostUSD > 0 {
			cost = fmt.Sprintf(" $%.2f", j.CostUSD)
		}
		title := j.IssueTitle.String
		if title == "" {
			title = j.Name
		}
		if j.IssueNumber > 0 {
			fmt.Printf("  #%-5d %-30s %-10s%s%s\n", j.IssueNumber, title, j.Status, pr, cost)
		} else {
			agent := ""
			if j.Agent != "autopilot" {
				agent = fmt.Sprintf("[%s] ", j.Agent)
			}
			fmt.Printf("  %-6s %-30s %-10s%s%s\n", agent, title, j.Status, pr, cost)
		}
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
		jobs, _ := store.GetJobs(d.ID)
		fmt.Printf("%-10s %s/%s  %-8s  %d jobs  %s\n",
			d.ID, d.Owner, d.Repo, status, len(jobs), d.StartedAt.Format("2006-01-02 15:04"))
	}
	return nil
}

func printJobs(jobs []daemon.JobResponse) {
	if len(jobs) == 0 {
		fmt.Println("No jobs.")
		return
	}
	fmt.Println()
	for _, j := range jobs {
		pr := ""
		if j.PRNumber > 0 {
			pr = fmt.Sprintf(" PR#%d", j.PRNumber)
		}
		cost := ""
		if j.CostUSD > 0 {
			cost = fmt.Sprintf(" $%.2f", j.CostUSD)
		}
		title := j.Title
		if title == "" {
			title = j.IssueTitle // fallback for older daemons
		}
		if j.IssueNumber > 0 {
			fmt.Printf("  #%-5d %-30s %-10s%s%s\n", j.IssueNumber, title, j.Status, pr, cost)
		} else {
			agent := ""
			if j.Agent != "autopilot" {
				agent = fmt.Sprintf("[%s] ", j.Agent)
			}
			fmt.Printf("  %-6s %-30s %-10s%s%s\n", agent, title, j.Status, pr, cost)
		}
	}
}
