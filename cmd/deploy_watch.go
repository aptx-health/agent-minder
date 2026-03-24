package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/dustinlange/agent-minder/internal/autopilot"
	"github.com/dustinlange/agent-minder/internal/claudecli"
	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/deploy"
	"github.com/dustinlange/agent-minder/internal/discovery"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
	"github.com/spf13/cobra"
)

var (
	watchMilestone          string
	watchLabel              string
	watchInterval           int
	watchMaxAgents          int
	watchMaxTurns           int
	watchMaxBudget          float64
	watchTotalBudget        float64
	watchBudgetPauseRunning bool
	watchDryRun             bool
	watchDaemon             bool
	watchDeployID           string
	watchProject            string
)

var deployWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Continuously poll GitHub for issues and auto-deploy agents",
	Long: `Watch for new open issues matching a milestone or label filter and
automatically launch autopilot agents on them. Runs as a long-lived daemon
that keeps polling even after all current tasks complete.`,
	Example: `  # Watch a milestone
  agent-minder deploy watch --milestone "v1.0"

  # Watch a label
  agent-minder deploy watch --label "ready-for-agent"

  # Custom poll interval and budget
  agent-minder deploy watch --milestone "v1.0" --poll-interval 120 --max-budget 5.00`,
	RunE: runDeployWatch,
}

func init() {
	deployCmd.AddCommand(deployWatchCmd)

	deployWatchCmd.Flags().StringVar(&watchMilestone, "milestone", "", "Watch for issues in this milestone")
	deployWatchCmd.Flags().StringVar(&watchLabel, "label", "", "Watch for issues with this label")
	deployWatchCmd.Flags().IntVar(&watchInterval, "poll-interval", 300, "GitHub poll interval in seconds (default: 300)")
	deployWatchCmd.Flags().IntVar(&watchMaxAgents, "max-agents", 5, "Max concurrent agents")
	deployWatchCmd.Flags().IntVar(&watchMaxTurns, "max-turns", 50, "Max turns per agent")
	deployWatchCmd.Flags().Float64Var(&watchMaxBudget, "max-budget", 3.00, "Max budget per agent in USD")
	deployWatchCmd.Flags().Float64Var(&watchTotalBudget, "total-budget", 0, "Total spend ceiling in USD (0 = no limit); auto-pause when hit")
	deployWatchCmd.Flags().BoolVar(&watchBudgetPauseRunning, "budget-pause-running", false, "Also stop running agents when total budget ceiling is hit")
	deployWatchCmd.Flags().BoolVar(&watchDryRun, "dry-run", false, "Show matching issues without launching")
	deployWatchCmd.Flags().StringVar(&watchProject, "project", "", "Inherit settings from an existing project")

	// Hidden flags for daemon re-exec.
	deployWatchCmd.Flags().BoolVar(&watchDaemon, "daemon", false, "Run as background daemon")
	deployWatchCmd.Flags().StringVar(&watchDeployID, "deploy-id", "", "Deploy ID for daemon mode")
	_ = deployWatchCmd.Flags().MarkHidden("daemon")
	_ = deployWatchCmd.Flags().MarkHidden("deploy-id")
}

func runDeployWatch(cmd *cobra.Command, _ []string) error {
	if watchDaemon {
		return runWatchDaemon()
	}

	// Validate filter flags.
	if watchMilestone == "" && watchLabel == "" {
		return fmt.Errorf("either --milestone or --label is required")
	}
	if watchMilestone != "" && watchLabel != "" {
		return fmt.Errorf("--milestone and --label are mutually exclusive")
	}

	filterType := "label"
	filterValue := watchLabel
	if watchMilestone != "" {
		filterType = "milestone"
		filterValue = watchMilestone
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

	remoteURL := gitpkg.RemoteURL(repoDir)
	owner, repo := gitpkg.ParseGitHubRemote(remoteURL)
	if owner == "" || repo == "" {
		return fmt.Errorf("could not determine GitHub owner/repo from origin remote: %s", remoteURL)
	}

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

	// Open DB.
	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	// Do initial GitHub search to show the user what will be watched.
	ghClient := ghpkg.NewClient(ghToken)
	ctx := context.Background()

	issues, err := fetchMatchingIssues(ctx, ghClient, owner, repo, filterType, filterValue)
	if err != nil {
		return err
	}

	// Build deploy settings: defaults, then --project overlay, then CLI flags.
	maxAgents := watchMaxAgents
	maxTurns := watchMaxTurns
	maxBudget := watchMaxBudget
	totalBudget := watchTotalBudget
	budgetPauseRunning := watchBudgetPauseRunning
	analyzerModel := "sonnet"
	skipLabel := "no-agent"
	baseBranch := ""

	if watchProject != "" {
		srcProject, err := store.GetProject(watchProject)
		if err != nil {
			return fmt.Errorf("--project %q not found: %w", watchProject, err)
		}
		if srcProject.AutopilotMaxAgents > 0 {
			maxAgents = srcProject.AutopilotMaxAgents
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
		if srcProject.TotalBudgetUSD > 0 {
			totalBudget = srcProject.TotalBudgetUSD
		}
		budgetPauseRunning = srcProject.BudgetPauseRunning
	}

	if cmd.Flags().Changed("max-agents") {
		maxAgents = watchMaxAgents
	}
	if cmd.Flags().Changed("max-turns") {
		maxTurns = watchMaxTurns
	}
	if cmd.Flags().Changed("max-budget") {
		maxBudget = watchMaxBudget
	}
	if cmd.Flags().Changed("total-budget") {
		totalBudget = watchTotalBudget
	}
	if cmd.Flags().Changed("budget-pause-running") {
		budgetPauseRunning = watchBudgetPauseRunning
	}

	// Filter out skip-labeled issues for display.
	var filtered []ghpkg.ItemStatus
	for _, issue := range issues {
		if !hasSkipLabel(issue.Labels, skipLabel) {
			filtered = append(filtered, issue)
		}
	}

	// Dry run — just show what would be watched.
	if watchDryRun {
		fmt.Printf("Watch mode (dry run)\n")
		fmt.Printf("Repo:      %s/%s\n", owner, repo)
		fmt.Printf("Filter:    %s = %q\n", filterType, filterValue)
		fmt.Printf("Agent def: %s\n\n", agentDefSource.Description())
		if len(filtered) == 0 {
			fmt.Println("No matching issues found.")
		} else {
			fmt.Printf("Current matching issues (%d):\n", len(filtered))
			for _, issue := range filtered {
				fmt.Printf("  #%-6d %s\n", issue.Number, issue.Title)
			}
		}
		fmt.Printf("\nMax agents: %d | Max turns: %d | Max budget: $%.2f\n", maxAgents, maxTurns, maxBudget)
		if totalBudget > 0 {
			fmt.Printf("Total budget ceiling: $%.2f", totalBudget)
			if budgetPauseRunning {
				fmt.Print(" (will stop running agents)")
			}
			fmt.Println()
		}
		fmt.Printf("Poll interval: %ds\n", watchInterval)
		fmt.Println("\nNo agents launched (--dry-run)")
		return nil
	}

	// Generate unique deploy ID.
	existingProjects, _ := store.ListDeployProjects()
	existingIDs := make([]string, 0, len(existingProjects))
	for _, p := range existingProjects {
		existingIDs = append(existingIDs, p.Name)
	}
	id := deploy.GenerateUniqueID(existingIDs)

	// Create ephemeral project with watch semantics.
	project := &db.Project{
		Name:                  id,
		IsDeploy:              true,
		GoalType:              "watch",
		GoalDescription:       fmt.Sprintf("Watch %s=%q for %s/%s", filterType, filterValue, owner, repo),
		AutopilotFilterType:   filterType,
		AutopilotFilterValue:  filterValue,
		AutopilotMaxAgents:    maxAgents,
		AutopilotMaxTurns:     maxTurns,
		AutopilotMaxBudgetUSD: maxBudget,
		TotalBudgetUSD:        totalBudget,
		BudgetPauseRunning:    budgetPauseRunning,
		LLMAnalyzerModel:      analyzerModel,
		AutopilotSkipLabel:    skipLabel,
		AutopilotBaseBranch:   baseBranch,
		RefreshIntervalSec:    watchInterval,
		StatusIntervalSec:     300,
		AnalysisIntervalSec:   1800,
	}
	if err := store.CreateProject(project); err != nil {
		return fmt.Errorf("create watch project: %w", err)
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

	// Re-exec as daemon.
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}
	daemonCmd := exec.Command(exe, "deploy", "watch", "--daemon", "--deploy-id", id)
	daemonCmd.Env = append(os.Environ(), "GITHUB_TOKEN="+ghToken)
	daemonCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

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
	fmt.Printf("Watch %s launched\n", id)
	fmt.Printf("Repo:      %s/%s\n", owner, repo)
	fmt.Printf("Filter:    %s = %q\n", filterType, filterValue)
	if len(filtered) > 0 {
		fmt.Printf("\nCurrent matching issues (%d):\n", len(filtered))
		for _, issue := range filtered {
			fmt.Printf("  #%-6d %s\n", issue.Number, issue.Title)
		}
	}
	fmt.Printf("\nMax agents: %d | Max turns: %d | Max budget: $%.2f\n", maxAgents, maxTurns, maxBudget)
	if totalBudget > 0 {
		fmt.Printf("Total budget ceiling: $%.2f", totalBudget)
		if budgetPauseRunning {
			fmt.Print(" (will stop running agents)")
		}
		fmt.Println()
	}
	fmt.Printf("Poll interval: %ds\n", watchInterval)
	fmt.Printf("\nLog:    %s\n", deploy.LogPath(id))
	fmt.Printf("Status: agent-minder deploy status %s\n", id)
	fmt.Printf("Stop:   agent-minder deploy stop %s\n", id)
	return nil
}

// runWatchDaemon is the long-lived daemon process for deploy watch.
// It continuously polls GitHub for new issues matching the stored filter,
// queues them as autopilot tasks, and launches/re-launches the supervisor.
func runWatchDaemon() error {
	if watchDeployID == "" {
		return fmt.Errorf("--deploy-id required in daemon mode")
	}

	// Write PID file.
	if err := deploy.WritePID(watchDeployID); err != nil {
		return fmt.Errorf("write PID: %w", err)
	}
	defer func() { _ = deploy.RemovePID(watchDeployID) }()

	log.Printf("Watch daemon %s starting (PID %d)", watchDeployID, os.Getpid())

	// Open DB, load project.
	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	project, err := store.GetProject(watchDeployID)
	if err != nil {
		return fmt.Errorf("load watch project: %w", err)
	}

	// Load enrolled repo.
	repos, err := store.GetRepos(project.ID)
	if err != nil || len(repos) == 0 {
		return fmt.Errorf("no repos enrolled for watch %s", watchDeployID)
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

	ghClient := ghpkg.NewClient(ghToken)
	completer := claudecli.NewCLICompleter()

	// Signal handling.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// Poll interval from project refresh setting (clamped to ≥30s).
	pollInterval := time.Duration(project.RefreshIntervalSec) * time.Second
	if pollInterval < 30*time.Second {
		pollInterval = 30 * time.Second
	}

	log.Printf("Watch %s: filter=%s value=%q interval=%s max_agents=%d",
		watchDeployID, project.AutopilotFilterType, project.AutopilotFilterValue,
		pollInterval, project.AutopilotMaxAgents)

	var supervisor *autopilot.Supervisor

	for {
		// Poll GitHub for matching issues and queue new ones.
		newCount, pollErr := watchPollAndQueue(ctx, store, ghClient, project, owner, repo)
		if pollErr != nil {
			log.Printf("Watch poll error: %v", pollErr)
		} else if newCount > 0 {
			log.Printf("Watch %s: queued %d new issue(s)", watchDeployID, newCount)
		}

		// Launch or re-launch supervisor if there are queued tasks and no active supervisor.
		if supervisor == nil || !supervisor.IsActive() {
			if hasQueuedTasks(store, project.ID) {
				supervisor = autopilot.New(store, project, completer, repoDir, owner, repo, ghToken)
				supervisor.Launch(ctx)

				// Drain events to log, exiting when supervisor finishes.
				go drainEvents(supervisor)

				log.Printf("Watch %s: launched supervisor", watchDeployID)
			}
		}

		// Wait for next poll, supervisor completion, or shutdown signal.
		timer := time.NewTimer(pollInterval)

		var doneCh <-chan struct{}
		if supervisor != nil {
			doneCh = supervisor.Done()
		}

		select {
		case sig := <-sigCh:
			timer.Stop()
			log.Printf("Received signal %v, stopping...", sig)
			if supervisor != nil && supervisor.IsActive() {
				supervisor.Stop()
			}
			cancel()
			if supervisor != nil {
				select {
				case <-supervisor.Done():
				case <-time.After(30 * time.Second):
					log.Printf("Timeout waiting for agents to stop")
				}
			}
			log.Printf("Watch %s stopped", watchDeployID)
			return nil

		case <-timer.C:
			// Next poll cycle.

		case <-doneCh:
			timer.Stop()
			log.Printf("Watch %s: supervisor finished, will poll for more work", watchDeployID)
			supervisor = nil
		}
	}
}

// fetchMatchingIssues queries GitHub for open issues matching the given filter.
func fetchMatchingIssues(ctx context.Context, ghClient *ghpkg.Client, owner, repo, filterType, filterValue string) ([]ghpkg.ItemStatus, error) {
	switch filterType {
	case "milestone":
		milestones, err := ghClient.ListMilestones(ctx, owner, repo)
		if err != nil {
			return nil, fmt.Errorf("list milestones: %w", err)
		}
		var msNum int
		for _, ms := range milestones {
			if ms.Value == filterValue {
				msNum = ms.ID
				break
			}
		}
		if msNum == 0 {
			return nil, fmt.Errorf("milestone %q not found in %s/%s", filterValue, owner, repo)
		}
		result, err := ghClient.ListIssuesByMilestone(ctx, owner, repo, msNum)
		if err != nil {
			return nil, fmt.Errorf("search by milestone: %w", err)
		}
		return result.Items, nil

	case "label":
		result, err := ghClient.SearchIssues(ctx, owner, repo, ghpkg.FilterLabel, filterValue)
		if err != nil {
			return nil, fmt.Errorf("search by label: %w", err)
		}
		return result.Items, nil

	default:
		return nil, fmt.Errorf("unknown filter type: %s", filterType)
	}
}

// watchPollAndQueue fetches issues from GitHub matching the watch filter,
// creates autopilot_tasks for any new ones, and returns the count of newly queued issues.
func watchPollAndQueue(ctx context.Context, store *db.Store, ghClient *ghpkg.Client, project *db.Project, owner, repo string) (int, error) {
	issues, err := fetchMatchingIssues(ctx, ghClient, owner, repo, project.AutopilotFilterType, project.AutopilotFilterValue)
	if err != nil {
		return 0, err
	}

	// Get existing tasks to deduplicate.
	existingTasks, err := store.GetAutopilotTasks(project.ID)
	if err != nil {
		return 0, fmt.Errorf("get existing tasks: %w", err)
	}
	tracked := make(map[int]bool, len(existingTasks))
	for _, t := range existingTasks {
		tracked[t.IssueNumber] = true
	}

	skipLabel := project.AutopilotSkipLabel
	queued := 0

	for _, issue := range issues {
		// Skip already tracked (any status).
		if tracked[issue.Number] {
			continue
		}

		// Skip if the issue has the skip label.
		if hasSkipLabel(issue.Labels, skipLabel) {
			continue
		}

		// Only open issues.
		if issue.State != "open" {
			continue
		}

		// Fetch full issue body (list/search results may have truncated bodies).
		body := issue.Body
		if body == "" {
			fetched, fetchErr := ghClient.FetchItem(ctx, owner, repo, issue.Number)
			if fetchErr != nil {
				log.Printf("Warning: could not fetch body for #%d: %v", issue.Number, fetchErr)
			} else {
				body = fetched.Body
			}
		}

		task := &db.AutopilotTask{
			ProjectID:    project.ID,
			Owner:        owner,
			Repo:         repo,
			IssueNumber:  issue.Number,
			IssueTitle:   issue.Title,
			IssueBody:    body,
			Dependencies: "[]",
			Status:       "queued",
		}
		if createErr := store.CreateAutopilotTask(task); createErr != nil {
			log.Printf("Warning: could not create task for #%d: %v", issue.Number, createErr)
			continue
		}
		queued++
	}

	return queued, nil
}

// hasQueuedTasks returns true if the project has any tasks in "queued" status.
func hasQueuedTasks(store *db.Store, projectID int64) bool {
	tasks, err := store.GetAutopilotTasks(projectID)
	if err != nil {
		return false
	}
	for _, t := range tasks {
		if t.Status == "queued" {
			return true
		}
	}
	return false
}

// hasSkipLabel returns true if the label list contains the skip label.
func hasSkipLabel(labels []string, skipLabel string) bool {
	for _, l := range labels {
		if l == skipLabel {
			return true
		}
	}
	return false
}

// drainEvents logs supervisor events until the supervisor finishes.
func drainEvents(s *autopilot.Supervisor) {
	for {
		select {
		case evt, ok := <-s.Events():
			if !ok {
				return
			}
			log.Printf("[%s] %s", evt.Type, evt.Summary)
		case <-s.Done():
			// Drain any remaining buffered events.
			for {
				select {
				case evt := <-s.Events():
					log.Printf("[%s] %s", evt.Type, evt.Summary)
				default:
					return
				}
			}
		}
	}
}
