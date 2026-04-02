package cmd

import (
	"fmt"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/scheduler"
	"github.com/spf13/cobra"
)

var jobsCmd = &cobra.Command{
	Use:   "jobs",
	Short: "Manage scheduled jobs",
}

var jobsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured job schedules",
	RunE:  runJobsList,
}

var jobsRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Manually trigger a scheduled job",
	Args:  cobra.ExactArgs(1),
	RunE:  runJobsRun,
}

var flagJobsRepo string

func init() {
	rootCmd.AddCommand(jobsCmd)
	jobsCmd.AddCommand(jobsListCmd)
	jobsCmd.AddCommand(jobsRunCmd)

	jobsCmd.PersistentFlags().StringVar(&flagJobsRepo, "repo", ".", "Repository directory")
}

func runJobsList(cmd *cobra.Command, args []string) error {
	repoDir, err := resolveRepoDir(flagJobsRepo)
	if err != nil {
		return err
	}

	// Load jobs.yaml.
	cfgPath := scheduler.ConfigPath(repoDir)
	cfg, err := scheduler.LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("load jobs.yaml: %w", err)
	}

	// Also check DB for schedule state.
	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return err
	}
	store := db.NewStore(conn)
	defer func() { _ = store.Close() }()

	// Build lookup of schedule state from DB.
	schedState := make(map[string]*db.JobSchedule)
	deploys, _ := store.ListDeployments()
	for _, d := range deploys {
		scheds, _ := store.GetSchedules(d.ID)
		for _, s := range scheds {
			schedState[s.Name] = s
		}
	}

	fmt.Printf("Schedules from %s:\n\n", cfgPath)
	for name, def := range cfg.Jobs {
		kind := "cron"
		expr := def.Schedule
		if def.IsTrigger() {
			kind = "trigger"
			expr = def.Trigger
		}

		fmt.Printf("  %-20s %-8s %-25s agent=%s", name, kind, expr, def.Agent)
		if def.Budget > 0 {
			fmt.Printf("  budget=$%.2f", def.Budget)
		}

		// Show DB state if available.
		if state, ok := schedState[name]; ok {
			if state.LastRunAt.Valid {
				fmt.Printf("  last=%s", state.LastRunAt.Time.Format("2006-01-02 15:04"))
			}
			if state.NextRunAt.Valid {
				next := state.NextRunAt.Time
				if next.After(time.Now()) {
					fmt.Printf("  next=%s", next.Format("2006-01-02 15:04"))
				}
			}
		}
		fmt.Println()
	}

	return nil
}

func runJobsRun(cmd *cobra.Command, args []string) error {
	name := args[0]

	repoDir, err := resolveRepoDir(flagJobsRepo)
	if err != nil {
		return err
	}

	owner, repo, err := resolveOwnerRepo(repoDir)
	if err != nil {
		return err
	}

	// Load config.
	cfgPath := scheduler.ConfigPath(repoDir)
	cfg, err := scheduler.LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("load jobs.yaml: %w", err)
	}

	if _, ok := cfg.Jobs[name]; !ok {
		return fmt.Errorf("job %q not found in jobs.yaml", name)
	}

	// Open DB and find active deployment.
	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return err
	}
	store := db.NewStore(conn)
	defer func() { _ = store.Close() }()

	// Find the most recent deployment for this repo.
	deploys, _ := store.ListDeployments()
	var deployID string
	for _, d := range deploys {
		if d.Owner == owner && d.Repo == repo {
			deployID = d.ID
			break
		}
	}

	if deployID == "" {
		return fmt.Errorf("no deployment found for %s/%s — run 'minder deploy' first", owner, repo)
	}

	// Create scheduler and sync.
	sched := scheduler.New(store, deployID, owner, repo, cfg)
	if err := sched.SyncSchedules(); err != nil {
		return err
	}

	// Fire the job.
	jobID, err := sched.RunOnce(name)
	if err != nil {
		return err
	}

	fmt.Printf("Triggered job %q (job ID %d)\n", name, jobID)
	fmt.Println("The daemon will pick it up if running, or use 'minder deploy --foreground' to process it.")

	return nil
}
