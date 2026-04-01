package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/aptx-health/agent-minder/internal/claudecli"
	"github.com/aptx-health/agent-minder/internal/daemon"
	"github.com/aptx-health/agent-minder/internal/db"
	gitpkg "github.com/aptx-health/agent-minder/internal/git"
	"github.com/aptx-health/agent-minder/internal/supervisor"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var deployCmd = &cobra.Command{
	Use:   "deploy [issues...]",
	Short: "Launch agents on GitHub issues",
	Long: `Deploy agents to work on specific GitHub issues, or watch for new issues
matching a label or milestone filter.

Examples:
  minder deploy 42 55 60                     # Work on specific issues
  minder deploy --watch label:agent-ready    # Watch for labeled issues
  minder deploy 42 --serve :7749             # With HTTP API
  minder deploy 42 --foreground              # Don't daemonize`,
	RunE: runDeploy,
}

var (
	flagRepo        string
	flagWatch       string
	flagServe       string
	flagAPIKey      string
	flagForeground  bool
	flagMaxAgents   int
	flagMaxTurns    int
	flagBudget      float64
	flagTotalBudget float64
	flagModel       string
	flagAutoMerge   bool
	flagBaseBranch  string
	flagSkipLabel   string
	flagDaemon      bool
	flagDeployID    string
)

func init() {
	rootCmd.AddCommand(deployCmd)

	deployCmd.Flags().StringVar(&flagRepo, "repo", ".", "Repository directory")
	deployCmd.Flags().StringVar(&flagWatch, "watch", "", "Watch filter (label:<name> or milestone:<name>)")
	deployCmd.Flags().StringVar(&flagServe, "serve", "", "Start HTTP API on address (e.g. :7749)")
	deployCmd.Flags().StringVar(&flagAPIKey, "api-key", "", "Require API key for HTTP access")
	deployCmd.Flags().BoolVar(&flagForeground, "foreground", false, "Don't daemonize (for systemd)")
	deployCmd.Flags().IntVar(&flagMaxAgents, "max-agents", 3, "Concurrent agent slots")
	deployCmd.Flags().IntVar(&flagMaxTurns, "max-turns", 50, "Per-task turn limit")
	deployCmd.Flags().Float64Var(&flagBudget, "budget", 5.0, "Per-task budget USD")
	deployCmd.Flags().Float64Var(&flagTotalBudget, "total-budget", 25.0, "Total deployment budget USD")
	deployCmd.Flags().StringVar(&flagModel, "model", "sonnet", "Analyzer model")
	deployCmd.Flags().BoolVar(&flagAutoMerge, "auto-merge", false, "Auto-merge low-risk reviewed PRs")
	deployCmd.Flags().StringVar(&flagBaseBranch, "base-branch", "", "Base branch (default: auto-detect)")
	deployCmd.Flags().StringVar(&flagSkipLabel, "skip-label", "no-agent", "Label to skip issues")

	// Hidden flags for daemon re-exec.
	deployCmd.Flags().BoolVar(&flagDaemon, "daemon", false, "")
	deployCmd.Flags().StringVar(&flagDeployID, "deploy-id", "", "")
	_ = deployCmd.Flags().MarkHidden("daemon")
	_ = deployCmd.Flags().MarkHidden("deploy-id")
}

func runDeploy(cmd *cobra.Command, args []string) error {
	// If this is a daemon re-exec, run the daemon directly.
	if flagDaemon && flagDeployID != "" {
		return runDaemon(flagDeployID)
	}

	// Parse issue numbers.
	var issues []int
	for _, arg := range args {
		num, err := strconv.Atoi(arg)
		if err != nil {
			return fmt.Errorf("invalid issue number %q", arg)
		}
		issues = append(issues, num)
	}

	if len(issues) == 0 && flagWatch == "" {
		return fmt.Errorf("provide issue numbers or --watch filter")
	}

	// Resolve repo.
	repoDir, err := resolveRepoDir(flagRepo)
	if err != nil {
		return err
	}

	owner, repo, err := resolveOwnerRepo(repoDir)
	if err != nil {
		return err
	}

	ghToken := os.Getenv("GITHUB_TOKEN")
	if ghToken == "" {
		return fmt.Errorf("GITHUB_TOKEN environment variable is required")
	}

	// Auto-detect base branch if not specified.
	baseBranch := flagBaseBranch
	if baseBranch == "" {
		baseBranch, _ = gitpkg.DefaultBranch(repoDir)
		if baseBranch == "" {
			baseBranch = "main"
		}
	}

	// Determine mode.
	mode := "issues"
	if flagWatch != "" {
		mode = "watch"
	}

	// Create deployment record.
	deployID := uuid.New().String()[:8]
	deploy := &db.Deployment{
		ID:             deployID,
		RepoDir:        repoDir,
		Owner:          owner,
		Repo:           repo,
		Mode:           mode,
		MaxAgents:      flagMaxAgents,
		MaxTurns:       flagMaxTurns,
		MaxBudgetUSD:   flagBudget,
		AnalyzerModel:  flagModel,
		SkipLabel:      flagSkipLabel,
		AutoMerge:      flagAutoMerge,
		ReviewEnabled:  true,
		TotalBudgetUSD: flagTotalBudget,
		BaseBranch:     baseBranch,
	}
	if flagWatch != "" {
		deploy.WatchFilter.String = flagWatch
		deploy.WatchFilter.Valid = true
	}

	// Open DB.
	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	store := db.NewStore(conn)

	if err := store.CreateDeployment(deploy); err != nil {
		_ = store.Close()
		return fmt.Errorf("create deployment: %w", err)
	}

	fmt.Printf("Deploy %s: %s/%s (%s)\n", deployID, owner, repo, mode)
	fmt.Printf("  Issues: %v\n", issues)
	fmt.Printf("  Agents: %d, Turns: %d, Budget: $%.2f/task, Total: $%.2f\n",
		flagMaxAgents, flagMaxTurns, flagBudget, flagTotalBudget)

	if flagForeground || flagServe == "" {
		// Run in foreground.
		_ = store.Close()
		return runForeground(deployID, issues)
	}

	// Daemonize: re-exec with --daemon flag.
	_ = store.Close()

	daemonArgs := []string{os.Args[0], "deploy", "--daemon", "--deploy-id", deployID}
	if flagServe != "" {
		daemonArgs = append(daemonArgs, "--serve", flagServe)
	}
	if flagAPIKey != "" {
		daemonArgs = append(daemonArgs, "--api-key", flagAPIKey)
	}
	// Pass through all config flags.
	daemonArgs = append(daemonArgs, "--repo", repoDir)
	daemonArgs = append(daemonArgs, "--max-agents", strconv.Itoa(flagMaxAgents))
	daemonArgs = append(daemonArgs, "--max-turns", strconv.Itoa(flagMaxTurns))
	daemonArgs = append(daemonArgs, "--budget", fmt.Sprintf("%.2f", flagBudget))
	daemonArgs = append(daemonArgs, "--total-budget", fmt.Sprintf("%.2f", flagTotalBudget))
	daemonArgs = append(daemonArgs, "--model", flagModel)
	daemonArgs = append(daemonArgs, "--base-branch", baseBranch)
	if flagAutoMerge {
		daemonArgs = append(daemonArgs, "--auto-merge")
	}
	if flagWatch != "" {
		daemonArgs = append(daemonArgs, "--watch", flagWatch)
	}

	logPath := daemon.LogPath(deployID)
	pid, err := daemon.Daemonize(daemonArgs, logPath)
	if err != nil {
		return fmt.Errorf("daemonize: %w", err)
	}

	fmt.Printf("  Daemon PID: %d\n", pid)
	fmt.Printf("  Log: %s\n", logPath)
	if flagServe != "" {
		fmt.Printf("  API: http://localhost%s\n", flagServe)
	}
	fmt.Printf("\nCheck status: minder status %s\n", deployID)
	fmt.Printf("Stop: minder stop %s\n", deployID)

	return nil
}

// runForeground runs the supervisor in the current process.
func runForeground(deployID string, issues []int) error {
	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return err
	}
	store := db.NewStore(conn)
	defer func() { _ = store.Close() }()

	deploy, err := store.GetDeployment(deployID)
	if err != nil {
		return fmt.Errorf("get deployment: %w", err)
	}

	ghToken := os.Getenv("GITHUB_TOKEN")
	completer := claudecli.NewCLICompleter()

	// Prepare: fetch issues, create tasks, build dep graph.
	fmt.Println("Preparing...")
	result, err := supervisor.Prepare(context.Background(), store, completer, deploy, issues, ghToken)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}

	fmt.Printf("  Jobs: %d\n", result.Total)
	fmt.Printf("  Agent def: %s\n", result.AgentDef.Description())

	// If there are dep graph options, auto-select the first (most conservative).
	if len(result.Options) > 0 {
		opt := result.Options[0]
		fmt.Printf("  Dep graph: %s (%d unblocked)\n", opt.Name, opt.Unblocked)
		if err := supervisor.ApplyDepOption(store, deploy, opt); err != nil {
			return fmt.Errorf("apply dep graph: %w", err)
		}
	}

	// Create and launch supervisor.
	sup := supervisor.New(store, deploy, deploy.RepoDir, deploy.Owner, deploy.Repo, ghToken)

	// Handle signals.
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nStopping...")
		cancel()
		sup.Stop()
	}()

	// Start HTTP API if requested.
	if flagServe != "" {
		srv := daemon.NewServer(daemon.ServerConfig{
			Store:    store,
			DeployID: deployID,
			APIKey:   flagAPIKey,
		})
		srv.StopDaemon = func() { cancel(); sup.Stop() }
		srv.BudgetResume = sup.ResumeBudget
		srv.IsBudgetPaused = sup.IsBudgetPaused

		go func() { _ = srv.ListenAndServe(flagServe) }()
		fmt.Printf("  API: http://localhost%s\n", flagServe)
	}

	// Print events in foreground mode.
	go func() {
		for evt := range sup.Events() {
			fmt.Printf("[%s] %s: %s\n", evt.Time.Format("15:04:05"), evt.Type, evt.Summary)
		}
	}()

	fmt.Println("Launching agents...")
	sup.Launch(ctx)

	// Wait for completion.
	<-sup.Done()
	fmt.Println("Done.")
	return nil
}

// runDaemon is the entry point for the background daemon process.
func runDaemon(deployID string) error {
	// Write PID file and start heartbeat.
	if err := daemon.WritePID(deployID); err != nil {
		return fmt.Errorf("write PID: %w", err)
	}
	stopHB := daemon.StartHeartbeat(deployID)
	// Cleanup order (defers run LIFO): stop heartbeat → remove heartbeat → remove PID.
	defer daemon.RemovePID(deployID)
	defer daemon.RemoveHeartbeat(deployID)
	defer stopHB()

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return err
	}
	store := db.NewStore(conn)
	defer func() { _ = store.Close() }()

	// Crash recovery.
	if daemon.WasCrashShutdown(deployID) {
		recovered, _ := daemon.RecoverDaemonState(store, deployID)
		if recovered > 0 {
			fmt.Printf("Recovered %d stale running jobs\n", recovered)
		}
	}

	deploy, err := store.GetDeployment(deployID)
	if err != nil {
		return fmt.Errorf("get deployment: %w", err)
	}

	ghToken := os.Getenv("GITHUB_TOKEN")
	completer := claudecli.NewCLICompleter()

	// Check if jobs already exist (daemon restart).
	jobs, _ := store.GetJobs(deployID)
	if len(jobs) == 0 {
		if deploy.Mode == "watch" {
			// Watch mode — supervisor will discover issues.
		} else {
			fmt.Println("Warning: no jobs found and not in watch mode")
		}
	}

	sup := supervisor.New(store, deploy, deploy.RepoDir, deploy.Owner, deploy.Repo, ghToken)
	sup.SetDaemonMode(deploy.Mode == "watch")

	if len(jobs) == 0 && deploy.Mode == "issues" {
		fmt.Println("Note: daemon started but no jobs found. Waiting for watch events or manual job creation.")
	}

	// Handle signals.
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
		sup.Stop()
	}()

	// Start HTTP API if configured.
	if flagServe != "" {
		srv := daemon.NewServer(daemon.ServerConfig{
			Store:    store,
			DeployID: deployID,
			APIKey:   flagAPIKey,
		})
		srv.StopDaemon = func() { cancel(); sup.Stop() }
		srv.BudgetResume = sup.ResumeBudget
		srv.IsBudgetPaused = sup.IsBudgetPaused

		go func() { _ = srv.ListenAndServe(flagServe) }()
	}

	// Check if we have jobs to run.
	existingJobs, _ := store.GetJobs(deployID)
	if len(existingJobs) == 0 && deploy.Mode != "watch" {
		fmt.Println("No jobs to run. Exiting.")
		return nil
	}

	// Auto-select dep graph if needed and not already set.
	if _, err := store.GetDepGraph(deployID); err != nil && len(existingJobs) > 1 {
		options, err := supervisor.BuildDepOptionsFromStore(ctx, completer, store, deploy)
		if err == nil && len(options) > 0 {
			_ = supervisor.ApplyDepOption(store, deploy, options[0])
		}
	}

	_ = completer // used above

	sup.Launch(ctx)
	<-sup.Done()

	return nil
}

// --- Helpers ---

func resolveRepoDir(dir string) (string, error) {
	if !gitpkg.IsRepo(dir) {
		return "", fmt.Errorf("%s is not a git repository", dir)
	}
	return gitpkg.TopLevel(dir)
}

func resolveOwnerRepo(repoDir string) (string, string, error) {
	remoteURL := gitpkg.RemoteURL(repoDir)
	if remoteURL == "" {
		return "", "", fmt.Errorf("no origin remote found in %s", repoDir)
	}
	owner, repo := gitpkg.ParseGitHubRemote(remoteURL)
	if owner == "" || repo == "" {
		return "", "", fmt.Errorf("could not parse GitHub owner/repo from %s", remoteURL)
	}
	// Strip .git suffix if present.
	repo = strings.TrimSuffix(repo, ".git")
	return owner, repo, nil
}
