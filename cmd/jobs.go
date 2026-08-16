package cmd

import (
	"fmt"
	"sort"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/picker"
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
	Use:   "run [name]",
	Short: "Manually trigger a scheduled job",
	Long: `Manually trigger a scheduled (cron) job from jobs.yaml.

Only proactive/scheduled jobs are shown — reactive trigger-based jobs
(label:*, milestone:*) fire automatically and cannot be triggered manually.

If no name is given, presents an interactive picker.

Examples:
  minder jobs run                # interactive picker
  minder jobs run weekly-deps    # trigger by name`,
	Args: cobra.MaximumNArgs(1),
	RunE: runJobsRun,
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
		switch {
		case def.IsTrigger():
			kind = "trigger"
			expr = def.Trigger
		case def.IsOneShot():
			kind = "one-shot"
			expr = def.ResolvedAt().Local().Format("2006-01-02 15:04")
		}

		fmt.Printf("  %-20s %-8s %-25s agent=%s", name, kind, expr, def.Agent)
		if def.Runtime != "" {
			fmt.Printf("  runtime=%s", def.Runtime)
		}
		if def.Model != "" {
			fmt.Printf("  model=%s", def.Model)
		}
		if def.Budget > 0 {
			fmt.Printf("  budget=$%.2f", def.Budget)
		}

		// Show DB state if available.
		if state, ok := schedState[name]; ok {
			switch {
			case def.IsOneShot():
				if state.Fired() {
					fmt.Printf("  fired=%s", state.LastRunAt.Time.Local().Format("2006-01-02 15:04"))
				} else {
					fmt.Printf("  pending")
				}
			default:
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
		}
		fmt.Println()
	}

	return nil
}

func runJobsRun(cmd *cobra.Command, args []string) error {
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

	var name string
	if len(args) > 0 {
		name = args[0]
		def, ok := cfg.Jobs[name]
		if !ok {
			return fmt.Errorf("job %q not found in jobs.yaml", name)
		}
		if def.IsTrigger() {
			return fmt.Errorf("job %q is a reactive trigger — it fires automatically when issues match its label filter", name)
		}
	} else {
		// Interactive picker — only scheduled (proactive) jobs.
		var labels []string
		for n, def := range cfg.Jobs {
			if def.IsTrigger() {
				continue
			}
			expr := def.Schedule
			if def.IsOneShot() {
				expr = def.ResolvedAt().Local().Format("2006-01-02 15:04")
			}
			label := fmt.Sprintf("%-20s  %-25s  agent=%s", n, expr, def.Agent)
			if def.Description != "" {
				label += "  " + truncateStr(def.Description, 40)
			}
			labels = append(labels, label)
		}
		if len(labels) == 0 {
			fmt.Println("No scheduled jobs found in jobs.yaml (only reactive triggers).")
			return nil
		}
		sort.Strings(labels)

		selected, err := picker.PickFromList(labels, "Manually trigger a scheduled job")
		if err != nil {
			return err
		}
		// Extract name from the formatted label (first field, trimmed).
		for n := range cfg.Jobs {
			if len(selected) >= len(n) && selected[:len(n)] == n {
				name = n
				break
			}
		}
		if name == "" {
			return fmt.Errorf("could not resolve selected job")
		}
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
