package cmd

import (
	"fmt"

	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/scheduler"
	"github.com/spf13/cobra"
)

var jobsHistoryCmd = &cobra.Command{
	Use:   "history <automation-name>",
	Short: "Show past activations of a scheduled/triggered automation",
	Long: `List past runs of a cron or trigger automation defined in jobs.yaml,
newest first, by reading job provenance (source_type/source_name) recorded
at activation time.

Examples:
  minder jobs history nightly-security-scan
  minder jobs history weekly-deps --limit 20
  minder jobs history weekly-deps --deploy abc123 --json`,
	Args: cobra.ExactArgs(1),
	RunE: runJobsHistory,
}

var (
	flagJobsHistoryDeploy string
	flagJobsHistoryLimit  int
	flagJobsHistoryJSON   bool
)

func init() {
	jobsCmd.AddCommand(jobsHistoryCmd)
	jobsHistoryCmd.Flags().StringVar(&flagJobsHistoryDeploy, "deploy", "", "Deployment ID to scope history to (default: most recent deployment for --repo)")
	jobsHistoryCmd.Flags().IntVar(&flagJobsHistoryLimit, "limit", 10, "Maximum number of past runs to show")
	jobsHistoryCmd.Flags().BoolVar(&flagJobsHistoryJSON, "json", false, "Output structured JSON instead of human-readable text")
}

// jobHistoryEntryJSON is the structured output for a single history row in
// `minder jobs history --json`.
type jobHistoryEntryJSON struct {
	JobID         int64   `json:"job_id"`
	Name          string  `json:"name"`
	SourceType    string  `json:"source_type"`
	Status        string  `json:"status"`
	QueuedAt      string  `json:"queued_at,omitempty"`
	StartedAt     string  `json:"started_at,omitempty"`
	CompletedAt   string  `json:"completed_at,omitempty"`
	PRNumber      int     `json:"pr_number,omitempty"`
	CostUSD       float64 `json:"cost_usd,omitempty"`
	FailureReason string  `json:"failure_reason,omitempty"`
}

// jobsHistoryJSON is the top-level structured output for `minder jobs history --json`.
type jobsHistoryJSON struct {
	Automation string                `json:"automation"`
	DeployID   string                `json:"deploy_id"`
	Runs       []jobHistoryEntryJSON `json:"runs"`
}

// buildJobsHistoryJSON converts a slice of jobs into the stable JSON shape
// for `minder jobs history --json`. Extracted so the shape can be exercised
// directly in tests without a live DB/CLI round-trip.
func buildJobsHistoryJSON(name, deployID string, jobs []*db.Job) jobsHistoryJSON {
	runs := make([]jobHistoryEntryJSON, len(jobs))
	for i, j := range jobs {
		entry := jobHistoryEntryJSON{
			JobID:      j.ID,
			Name:       j.Name,
			SourceType: j.SourceType.String,
			Status:     j.Status,
			CostUSD:    j.CostUSD,
		}
		if j.QueuedAt.Valid {
			entry.QueuedAt = j.QueuedAt.Time.Format("2006-01-02T15:04:05Z07:00")
		}
		if j.StartedAt.Valid {
			entry.StartedAt = j.StartedAt.Time.Format("2006-01-02T15:04:05Z07:00")
		}
		if j.CompletedAt.Valid {
			entry.CompletedAt = j.CompletedAt.Time.Format("2006-01-02T15:04:05Z07:00")
		}
		if j.PRNumber.Valid {
			entry.PRNumber = int(j.PRNumber.Int64)
		}
		if j.FailureReason.Valid {
			entry.FailureReason = j.FailureReason.String
		}
		runs[i] = entry
	}
	return jobsHistoryJSON{Automation: name, DeployID: deployID, Runs: runs}
}

func runJobsHistory(cmd *cobra.Command, args []string) error {
	name := args[0]
	limit := flagJobsHistoryLimit
	if limit <= 0 {
		limit = 10
	}

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return err
	}
	store := db.NewStore(conn)
	defer func() { _ = store.Close() }()

	deployID := flagJobsHistoryDeploy
	if deployID == "" {
		repoDir, err := resolveRepoDir(flagJobsRepo)
		if err != nil {
			return err
		}
		owner, repo, err := resolveOwnerRepo(repoDir)
		if err != nil {
			return err
		}
		deploys, err := store.ListDeployments()
		if err != nil {
			return err
		}
		for _, d := range deploys {
			if d.Owner == owner && d.Repo == repo {
				deployID = d.ID
				break
			}
		}
		if deployID == "" {
			return fmt.Errorf("no deployment found for %s/%s — pass --deploy explicitly or run 'minder deploy' first", owner, repo)
		}
	} else if _, err := store.GetDeployment(deployID); err != nil {
		return fmt.Errorf("deployment %q not found", deployID)
	}

	jobs, err := store.GetJobHistory(deployID, name, limit)
	if err != nil {
		return fmt.Errorf("query job history: %w", err)
	}

	if len(jobs) == 0 {
		// Distinguish "automation doesn't exist" from "never ran yet" by
		// checking jobs.yaml, when available, for the name.
		known := false
		if repoDir, rerr := resolveRepoDir(flagJobsRepo); rerr == nil {
			cfgPath := scheduler.ConfigPath(repoDir)
			if cfg, cerr := scheduler.LoadConfig(cfgPath); cerr == nil {
				_, known = cfg.Jobs[name]
			}
		}
		if !known {
			return fmt.Errorf("automation %q not found: no jobs.yaml entry and no run history for deployment %s", name, deployID)
		}
		if flagJobsHistoryJSON {
			return printJSON(buildJobsHistoryJSON(name, deployID, nil))
		}
		fmt.Printf("No history for automation %q in deployment %s.\n", name, deployID)
		return nil
	}

	if flagJobsHistoryJSON {
		return printJSON(buildJobsHistoryJSON(name, deployID, jobs))
	}

	fmt.Printf("History for %q (deployment %s):\n\n", name, deployID)
	for _, j := range jobs {
		queued := "-"
		if j.QueuedAt.Valid {
			queued = j.QueuedAt.Time.Format("2006-01-02 15:04")
		}
		started := "-"
		if j.StartedAt.Valid {
			started = j.StartedAt.Time.Format("2006-01-02 15:04")
		}
		completed := "-"
		if j.CompletedAt.Valid {
			completed = j.CompletedAt.Time.Format("2006-01-02 15:04")
		}
		pr := ""
		if j.PRNumber.Valid {
			pr = fmt.Sprintf(" PR#%d", j.PRNumber.Int64)
		}
		cost := ""
		if j.CostUSD > 0 {
			cost = fmt.Sprintf(" $%.2f", j.CostUSD)
		}
		failure := ""
		if j.FailureReason.Valid && j.FailureReason.String != "" {
			failure = " " + truncateStr(j.FailureReason.String, 40)
		}
		fmt.Printf("  job#%-5d queued=%-16s started=%-16s completed=%-16s %-10s%s%s%s\n",
			j.ID, queued, started, completed, j.Status, pr, cost, failure)
	}

	return nil
}
