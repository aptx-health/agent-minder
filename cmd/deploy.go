package cmd

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/dustinlange/agent-minder/internal/api"
	"github.com/dustinlange/agent-minder/internal/autopilot"
	"github.com/dustinlange/agent-minder/internal/claudecli"
	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/deploy"
	"github.com/dustinlange/agent-minder/internal/discovery"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
	"github.com/dustinlange/agent-minder/internal/poller"
	"github.com/spf13/cobra"
)

var (
	deployMaxAgents  int
	deployMaxTurns   int
	deployMaxBudget  float64
	deployBaseBranch string
	deployDryRun     bool
	deployYes        bool
	deployDaemon     bool
	deployForeground bool
	deployID         string
	deployProject    string
	deployServe      string
	deployAPIKey     string
	deployRemote     string
)

var deployCmd = &cobra.Command{
	Use:   "deploy [flags] <issue#> [issue#...]",
	Short: "Launch autopilot agents on specific issues",
	Long: `Launch autonomous Claude Code agents to work on specific GitHub issues.
Agents run as a background daemon in isolated git worktrees.

The command infers the repository from the current working directory and
creates an ephemeral deployment. Use subcommands to monitor progress.`,
	Example: `  # Launch agents on two issues
  agent-minder deploy 42 55

  # Dry run — show what would happen
  agent-minder deploy 42 55 --dry-run

  # Custom budget and turns
  agent-minder deploy 42 --max-turns 100 --max-budget 5.00

  # Inherit settings from an existing project
  agent-minder deploy 42 55 --project my-project

  # Check deployment status
  agent-minder deploy list
  agent-minder deploy status <deploy-id>
  agent-minder deploy stop <deploy-id>

  # Query a remote daemon
  agent-minder deploy list --remote vps:7749
  agent-minder deploy status <deploy-id> --remote vps:7749 --api-key secret`,
	Args: cobra.MinimumNArgs(0),
	RunE: runDeploy,
}

func init() {
	rootCmd.AddCommand(deployCmd)

	deployCmd.Flags().IntVar(&deployMaxAgents, "max-agents", 0, "Max concurrent agents (default: min(issues, 5), or from --project)")
	deployCmd.Flags().IntVar(&deployMaxTurns, "max-turns", 0, "Max turns per agent (default: 50, or from --project)")
	deployCmd.Flags().Float64Var(&deployMaxBudget, "max-budget", 0, "Max budget per agent in USD (default: 3.00, or from --project)")
	deployCmd.Flags().StringVar(&deployBaseBranch, "base-branch", "", "Base branch for PRs (default: auto-detect from origin)")
	deployCmd.Flags().BoolVar(&deployDryRun, "dry-run", false, "Show plan without launching")
	deployCmd.Flags().BoolVarP(&deployYes, "yes", "y", false, "Skip confirmation and launch immediately")
	deployCmd.Flags().StringVar(&deployProject, "project", "", "Inherit settings (agents, turns, budget, skip label, base branch) from an existing project")

	deployCmd.Flags().StringVar(&deployServe, "serve", "", "Enable HTTP API server on the given address (e.g. :7749)")
	deployCmd.Flags().BoolVar(&deployForeground, "foreground", false, "Run in foreground instead of daemonizing (for launchd/systemd)")

	// Persistent flags shared by all subcommands (list, status, stop).
	deployCmd.PersistentFlags().StringVar(&deployRemote, "remote", "", "Query a remote daemon at host:port (or set MINDER_REMOTE)")
	deployCmd.PersistentFlags().StringVar(&deployAPIKey, "api-key", "", "API key for HTTP server authentication (or set MINDER_API_KEY)")

	// Hidden flags for daemon re-exec.
	deployCmd.Flags().BoolVar(&deployDaemon, "daemon", false, "Run as background daemon")
	deployCmd.Flags().StringVar(&deployID, "deploy-id", "", "Deploy ID for daemon mode")
	_ = deployCmd.Flags().MarkHidden("daemon")
	_ = deployCmd.Flags().MarkHidden("deploy-id")
}

func runDeploy(cmd *cobra.Command, args []string) error {
	if deployDaemon {
		return runDeployDaemon()
	}

	if len(args) == 0 {
		return cmd.Help()
	}

	// Parse issue numbers.
	var issues []int
	for _, arg := range args {
		n, err := strconv.Atoi(arg)
		if err != nil || n <= 0 {
			return fmt.Errorf("invalid issue number: %q", arg)
		}
		issues = append(issues, n)
	}

	// Resolve git root from CWD.
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}
	repoDir, err := gitpkg.TopLevel(cwd)
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}

	// Infer owner/repo from origin remote.
	remoteURL := gitpkg.RemoteURL(repoDir)
	owner, repo := gitpkg.ParseGitHubRemote(remoteURL)
	if owner == "" || repo == "" {
		return fmt.Errorf("could not determine GitHub owner/repo from origin remote: %s", remoteURL)
	}

	// Resolve GitHub token.
	ghToken := config.GetIntegrationToken("github")
	if ghToken == "" {
		return fmt.Errorf("no GitHub token found — set GITHUB_TOKEN or run 'agent-minder setup'")
	}

	// Check agent definition.
	agentDefSource := autopilot.DetectAgentDef(repoDir)
	if agentDefSource == autopilot.AgentDefBuiltIn {
		fmt.Fprintln(os.Stderr, "Warning: No .claude/agents/autopilot.md found for this repo.")
		fmt.Fprintln(os.Stderr, "Agents will use built-in defaults. For better results, run:")
		fmt.Fprintf(os.Stderr, "  agent-minder init %s\n\n", repoDir)
	}

	// Open DB and check for duplicates.
	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	dupes, err := store.IssuesInRunningDeploys(owner, repo, issues)
	if err != nil {
		return fmt.Errorf("checking for duplicate issues: %w", err)
	}
	if len(dupes) > 0 {
		for issueNum, deployName := range dupes {
			fmt.Fprintf(os.Stderr, "Issue #%d is already active in deploy %q\n", issueNum, deployName)
		}
		return fmt.Errorf("cannot deploy duplicate issues — stop the existing deploy first")
	}

	// Generate unique deploy ID.
	existingProjects, _ := store.ListDeployProjects()
	existingIDs := make([]string, 0, len(existingProjects))
	for _, p := range existingProjects {
		existingIDs = append(existingIDs, p.Name)
	}
	id := deploy.GenerateUniqueID(existingIDs)

	// Fetch issue details from GitHub.
	ghClient := ghpkg.NewClient(ghToken)
	ctx := context.Background()
	var issueInfos []issueInfo
	for _, num := range issues {
		status, err := ghClient.FetchItem(ctx, owner, repo, num)
		if err != nil {
			return fmt.Errorf("fetch issue #%d: %w", num, err)
		}
		if status.ItemType != "issue" {
			return fmt.Errorf("#%d is a pull request, not an issue", num)
		}
		if status.State != "open" {
			return fmt.Errorf("issue #%d is %s, not open", num, status.State)
		}
		issueInfos = append(issueInfos, issueInfo{
			Number: num,
			Title:  status.Title,
			Body:   status.Body,
		})
	}

	// Build deploy settings: start with defaults, overlay --project, then CLI flags.
	maxAgents := len(issues)
	if maxAgents > 5 {
		maxAgents = 5
	}
	maxTurns := 50
	maxBudget := 3.00
	analyzerModel := "sonnet"
	skipLabel := "no-agent"
	baseBranch := ""

	if deployProject != "" {
		srcProject, err := store.GetProject(deployProject)
		if err != nil {
			return fmt.Errorf("--project %q not found: %w", deployProject, err)
		}
		if srcProject.AutopilotMaxAgents > 0 {
			maxAgents = min(len(issues), srcProject.AutopilotMaxAgents)
		}
		if srcProject.AutopilotMaxTurns > 0 {
			maxTurns = srcProject.AutopilotMaxTurns
		}
		if srcProject.AutopilotMaxBudgetUSD > 0 {
			maxBudget = srcProject.AutopilotMaxBudgetUSD
		}
		if srcProject.LLMAnalyzerModel != "" {
			analyzerModel = srcProject.LLMAnalyzerModel
		}
		if srcProject.AutopilotSkipLabel != "" {
			skipLabel = srcProject.AutopilotSkipLabel
		}
		if srcProject.AutopilotBaseBranch != "" {
			baseBranch = srcProject.AutopilotBaseBranch
		}
	}

	// CLI flags override project settings when explicitly set.
	if cmd.Flags().Changed("max-agents") {
		maxAgents = deployMaxAgents
	}
	if cmd.Flags().Changed("max-turns") {
		maxTurns = deployMaxTurns
	}
	if cmd.Flags().Changed("max-budget") {
		maxBudget = deployMaxBudget
	}
	if cmd.Flags().Changed("base-branch") {
		baseBranch = deployBaseBranch
	}

	// Resolve display base branch (for confirmation screen).
	displayBaseBranch := baseBranch
	baseBranchExplicit := baseBranch != ""
	if displayBaseBranch == "" {
		detected, _ := gitpkg.DefaultBranch(repoDir)
		if detected == "" {
			detected = "main"
		}
		displayBaseBranch = detected
	}

	// Show confirmation screen (unless --yes or --dry-run).
	if !deployYes && !deployDryRun {
		settings := &deploySettings{
			owner:              owner,
			repo:               repo,
			agentDefSource:     agentDefSource,
			issues:             issueInfos,
			maxAgents:          maxAgents,
			maxTurns:           maxTurns,
			maxBudget:          maxBudget,
			baseBranch:         displayBaseBranch,
			baseBranchExplicit: baseBranchExplicit,
			analyzerModel:      analyzerModel,
			skipLabel:          skipLabel,
		}
		confirmed, err := showDeployConfirmation(settings)
		if err != nil {
			return fmt.Errorf("confirmation: %w", err)
		}
		if !confirmed {
			fmt.Println("Deploy cancelled.")
			return nil
		}
		// Apply user edits back to local variables.
		maxAgents = settings.maxAgents
		maxTurns = settings.maxTurns
		maxBudget = settings.maxBudget
		analyzerModel = settings.analyzerModel
		skipLabel = settings.skipLabel
		if settings.baseBranchExplicit {
			baseBranch = settings.baseBranch
		}
	}

	// Create ephemeral project.
	project := &db.Project{
		Name:                  id,
		IsDeploy:              true,
		GoalType:              "deploy",
		GoalDescription:       fmt.Sprintf("Deploy agents for %s/%s issues", owner, repo),
		AutopilotMaxAgents:    maxAgents,
		AutopilotMaxTurns:     maxTurns,
		AutopilotMaxBudgetUSD: maxBudget,
		LLMAnalyzerModel:      analyzerModel,
		AutopilotSkipLabel:    skipLabel,
		AutopilotBaseBranch:   baseBranch,
		RefreshIntervalSec:    300,
		StatusIntervalSec:     300,
		AnalysisIntervalSec:   1800,
	}
	if err := store.CreateProject(project); err != nil {
		return fmt.Errorf("create deploy project: %w", err)
	}

	// Enroll the repo.
	info, err := discovery.ScanRepo(repoDir)
	if err != nil {
		return fmt.Errorf("scan repo: %w", err)
	}
	repoRecord := &db.Repo{
		ProjectID: project.ID,
		Path:      info.Path,
		ShortName: info.ShortName,
	}
	if err := store.AddRepo(repoRecord); err != nil {
		return fmt.Errorf("enroll repo: %w", err)
	}

	// Create autopilot tasks.
	for _, ii := range issueInfos {
		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			Owner:        owner,
			Repo:         repo,
			IssueNumber:  ii.Number,
			IssueTitle:   ii.Title,
			IssueBody:    ii.Body,
			Dependencies: "[]",
			Status:       "queued",
		}
		if err := store.CreateAutopilotTask(task); err != nil {
			return fmt.Errorf("create task for #%d: %w", ii.Number, err)
		}
	}

	// Dry run — just print summary.
	if deployDryRun {
		fmt.Printf("Deploy %s (dry run)\n", id)
		fmt.Printf("Repo:  %s/%s\n", owner, repo)
		fmt.Printf("Agent def: %s\n\n", agentDefSource.Description())
		for _, ii := range issueInfos {
			fmt.Printf("  #%-6d %s\n", ii.Number, ii.Title)
		}
		fmt.Printf("\nMax agents: %d | Max turns: %d | Max budget: $%.2f\n", maxAgents, maxTurns, maxBudget)
		if deployProject != "" {
			fmt.Printf("Settings from: %s\n", deployProject)
		}
		fmt.Println("\nNo agents launched (--dry-run)")
		// Clean up the ephemeral project since we're not launching.
		_ = store.DeleteProject(project.ID)
		return nil
	}

	// Foreground mode: run daemon inline (for launchd/systemd).
	if deployForeground {
		deployID = id
		_ = os.Setenv("GITHUB_TOKEN", ghToken)
		// Persist deploy-id so launchd restarts can skip project creation.
		if err := deploy.SaveForegroundID("deploy", id); err != nil {
			log.Printf("Warning: could not save foreground deploy-id: %v", err)
		}
		fmt.Fprintf(os.Stderr, "Deploy %s starting in foreground (PID %d)\n", id, os.Getpid())
		return runDeployDaemon()
	}

	// Re-exec as daemon.
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}
	daemonArgs := []string{"deploy", "--daemon", "--deploy-id", id}
	if deployServe != "" {
		daemonArgs = append(daemonArgs, "--serve", deployServe)
	}
	if deployAPIKey != "" {
		daemonArgs = append(daemonArgs, "--api-key", deployAPIKey)
	}
	daemonCmd := exec.Command(exe, daemonArgs...)
	daemonCmd.Env = append(os.Environ(), "GITHUB_TOKEN="+ghToken)
	daemonCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	// Ensure deploy directory exists for log file.
	if err := os.MkdirAll(deploy.Dir(), 0o755); err != nil {
		return fmt.Errorf("create deploy dir: %w", err)
	}
	logFile, err := os.Create(deploy.LogPath(id))
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}
	daemonCmd.Stdout = logFile
	daemonCmd.Stderr = logFile

	if err := daemonCmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start daemon: %w", err)
	}
	_ = logFile.Close()

	// Print launch summary.
	fmt.Printf("Deploy %s launched\n", id)
	fmt.Printf("Repo:  %s/%s\n\n", owner, repo)
	for _, ii := range issueInfos {
		fmt.Printf("  #%-6d %s\n", ii.Number, ii.Title)
	}
	fmt.Printf("\nMax agents: %d | Max turns: %d | Max budget: $%.2f\n", maxAgents, maxTurns, maxBudget)
	if deployProject != "" {
		fmt.Printf("Settings from: %s\n", deployProject)
	}
	fmt.Printf("\nLog:    %s\n", deploy.LogPath(id))
	fmt.Printf("Status: agent-minder deploy status %s\n", id)
	fmt.Printf("Stop:   agent-minder deploy stop %s\n", id)
	return nil
}

type issueInfo struct {
	Number int
	Title  string
	Body   string
}

// deploySettings holds mutable deploy configuration for the confirmation screen.
type deploySettings struct {
	owner          string
	repo           string
	agentDefSource autopilot.AgentDefSource
	issues         []issueInfo

	maxAgents          int
	maxTurns           int
	maxBudget          float64
	baseBranch         string
	baseBranchExplicit bool
	analyzerModel      string
	skipLabel          string
}

func printDeploySettings(s *deploySettings) {
	fmt.Println()
	fmt.Printf("  Deploy Settings\n")
	fmt.Printf("  %s\n", strings.Repeat("─", 50))
	fmt.Printf("  Repo:       %s/%s\n", s.owner, s.repo)
	fmt.Printf("  Agent def:  %s\n", s.agentDefSource.Description())
	fmt.Println()
	fmt.Printf("  Issues:\n")
	for _, ii := range s.issues {
		fmt.Printf("    #%-6d %s\n", ii.Number, ii.Title)
	}
	fmt.Println()
	branchNote := ""
	if !s.baseBranchExplicit {
		branchNote = " (auto-detected)"
	}
	fmt.Printf("  [1] Max agents:      %d\n", s.maxAgents)
	fmt.Printf("  [2] Max turns:       %d\n", s.maxTurns)
	fmt.Printf("  [3] Max budget:      $%.2f\n", s.maxBudget)
	fmt.Printf("  [4] Base branch:     %s%s\n", s.baseBranch, branchNote)
	fmt.Printf("  [5] Analyzer model:  %s\n", s.analyzerModel)
	fmt.Printf("  [6] Skip label:      %s\n", s.skipLabel)
	fmt.Printf("  %s\n", strings.Repeat("─", 50))
}

// showDeployConfirmation displays settings and lets the user edit them.
// Returns true if the user confirms, false if cancelled.
func showDeployConfirmation(s *deploySettings) (bool, error) {
	reader := bufio.NewReader(os.Stdin)

	for {
		printDeploySettings(s)
		fmt.Printf("\n  Edit [1-6], Enter to launch, q to cancel: ")

		line, err := reader.ReadString('\n')
		if err != nil {
			return false, err
		}
		input := strings.TrimSpace(line)

		switch input {
		case "":
			return true, nil
		case "q", "Q", "n", "N":
			return false, nil
		case "1":
			if v, ok := promptDeployInt(reader, "Max agents"); ok {
				s.maxAgents = v
			}
		case "2":
			if v, ok := promptDeployInt(reader, "Max turns"); ok {
				s.maxTurns = v
			}
		case "3":
			if v, ok := promptDeployFloat(reader, "Max budget (USD)"); ok {
				s.maxBudget = v
			}
		case "4":
			if v, ok := promptDeployString(reader, "Base branch"); ok {
				s.baseBranch = v
				s.baseBranchExplicit = true
			}
		case "5":
			if v, ok := promptDeployString(reader, "Analyzer model"); ok {
				s.analyzerModel = v
			}
		case "6":
			if v, ok := promptDeployString(reader, "Skip label"); ok {
				s.skipLabel = v
			}
		default:
			fmt.Printf("  Unknown option: %s\n", input)
		}
	}
}

func promptDeployInt(reader *bufio.Reader, label string) (int, bool) {
	fmt.Printf("  %s: ", label)
	v, err := strconv.Atoi(readLine(reader))
	if err != nil || v <= 0 {
		fmt.Println("  Invalid number, keeping current value.")
		return 0, false
	}
	return v, true
}

func promptDeployFloat(reader *bufio.Reader, label string) (float64, bool) {
	fmt.Printf("  %s: ", label)
	input := strings.TrimPrefix(readLine(reader), "$")
	v, err := strconv.ParseFloat(input, 64)
	if err != nil || v <= 0 {
		fmt.Println("  Invalid amount, keeping current value.")
		return 0, false
	}
	return v, true
}

func promptDeployString(reader *bufio.Reader, label string) (string, bool) {
	fmt.Printf("  %s: ", label)
	v := readLine(reader)
	if v == "" {
		fmt.Println("  Empty input, keeping current value.")
		return "", false
	}
	return v, true
}

func runDeployDaemon() error {
	if deployID == "" {
		return fmt.Errorf("--deploy-id required in daemon mode")
	}

	// Idempotent startup: if a daemon is already running, error out.
	if alive, pid := deploy.IsRunning(deployID); alive {
		return fmt.Errorf("deploy %s is already running (PID %d)", deployID, pid)
	}

	// Detect crash from previous run and clean up stale PID/heartbeat.
	if deploy.WasCrashShutdown(deployID) {
		log.Printf("Detected previous crash for deploy %s — cleaning up stale PID", deployID)
		deploy.CleanStalePID(deployID)
	}

	// Write PID file.
	if err := deploy.WritePID(deployID); err != nil {
		return fmt.Errorf("write PID: %w", err)
	}
	defer func() {
		_ = deploy.RemovePID(deployID)
		deploy.RemoveHeartbeat(deployID)
	}()

	// Start heartbeat writer (30s interval).
	stopHeartbeat := deploy.StartHeartbeat(deployID, 30*time.Second)
	defer stopHeartbeat()

	log.Printf("Deploy daemon %s starting (PID %d)", deployID, os.Getpid())

	// Open DB, load project.
	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	project, err := store.GetProject(deployID)
	if err != nil {
		return fmt.Errorf("load deploy project: %w", err)
	}

	// Load enrolled repo.
	repos, err := store.GetRepos(project.ID)
	if err != nil || len(repos) == 0 {
		return fmt.Errorf("no repos enrolled for deploy %s", deployID)
	}
	repoDir := repos[0].Path

	// Infer owner/repo.
	remoteURL := gitpkg.RemoteURL(repoDir)
	owner, repo := gitpkg.ParseGitHubRemote(remoteURL)
	if owner == "" {
		return fmt.Errorf("cannot determine GitHub owner/repo")
	}

	ghToken := os.Getenv("GITHUB_TOKEN")
	if ghToken == "" {
		ghToken = config.GetIntegrationToken("github")
	}

	// Recover from previous unclean shutdown: reset stale tasks, clean worktrees.
	recovered, err := deploy.RecoverDaemonState(store, project, repoDir)
	if err != nil {
		log.Printf("Recovery warning: %v", err)
	} else if recovered > 0 {
		log.Printf("Recovered %d tasks from previous session", recovered)
	}

	// Create completer and supervisor.
	completer := claudecli.NewCLICompleter()
	supervisor := autopilot.New(store, project, completer, repoDir, owner, repo, ghToken)

	// Create analysis poller (publisher nil — bus publishing optional in daemon mode).
	analysisPoller := poller.New(store, project, completer, nil)
	analysisPoller.SetAutopilotDepGraphFunc(supervisor.DepGraph)

	// Signal handling.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, stopping...", sig)
		supervisor.Stop()
		cancel()
	}()

	// Start the analysis poller (runs status polls on timer, analysis on-demand).
	analysisPoller.Start(ctx)
	defer analysisPoller.Stop()

	// Launch — tasks are already queued with empty deps, so they'll fill immediately.
	supervisor.Launch(ctx)

	// In-progress guard for on-demand analysis trigger.
	var pollInProgress atomic.Bool
	triggerPoll := func() error {
		if !pollInProgress.CompareAndSwap(false, true) {
			return fmt.Errorf("analysis already in progress")
		}
		go func() {
			defer pollInProgress.Store(false)
			log.Printf("On-demand analysis poll triggered via API")
			analysisPoller.PollNow(ctx)
			log.Printf("On-demand analysis poll completed")
		}()
		return nil
	}

	// Start HTTP API server + Discord bot if --serve is set.
	svc := startDaemonServices(ctx, serviceConfig{
		Store:     store,
		ProjectID: project.ID,
		DeployID:  deployID,
		StopDaemon: func() {
			log.Printf("Stop requested via API")
			supervisor.Stop()
			cancel()
		},
		TriggerPoll:    triggerPoll,
		BudgetResume:   supervisor.ResumeBudget,
		IsBudgetPaused: supervisor.IsBudgetPaused,
	})

	// Drain supervisor events to log.
	go func() {
		for evt := range supervisor.Events() {
			log.Printf("[%s] %s", evt.Type, evt.Summary)
		}
	}()

	// Drain poller events to log.
	go func() {
		for evt := range analysisPoller.Events() {
			log.Printf("[analysis/%s] %s", evt.Type, evt.Summary)
		}
	}()

	// Wait for supervisor to finish or signal.
	select {
	case <-supervisor.Done():
		log.Printf("Deploy %s completed", deployID)
	case <-ctx.Done():
		log.Printf("Deploy %s stopped by signal", deployID)
		// Give supervisor time to clean up.
		select {
		case <-supervisor.Done():
		case <-time.After(30 * time.Second):
			log.Printf("Timeout waiting for agents to stop")
		}
	}

	// Gracefully shut down API server + Discord bot.
	if svc != nil {
		svc.Shutdown()
	}

	return nil
}

// remoteClient returns an API client if --remote (or MINDER_REMOTE) is set,
// or nil if the command should use the local database.
func remoteClient() *api.Client {
	addr := deployRemote
	if addr == "" {
		addr = os.Getenv("MINDER_REMOTE")
	}
	if addr == "" {
		return nil
	}
	apiKey := deployAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("MINDER_API_KEY")
	}
	return api.NewClient(addr, apiKey)
}
