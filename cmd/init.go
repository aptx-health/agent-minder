package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/discovery"
	"github.com/dustinlange/agent-minder/internal/onboarding"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init [repo-dir ...]",
	Short: "Bootstrap a new project from one or more repo directories",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
}

// goalTypes maps goal selection number to (type, default refresh seconds).
var goalTypes = []struct {
	Name       string
	Label      string
	DefaultSec int
}{
	{"feature", "Feature work — building or shipping something new", 300},
	{"bugfix", "Bug fix — tracking down and fixing a specific issue", 180},
	{"infrastructure", "Infrastructure — multi-repo infra, migration, or deployment", 300},
	{"maintenance", "Maintenance — docs, deps, cleanup, refactoring", 600},
	{"standby", "On-call / standby — monitoring, ready to respond", 900},
	{"other", "Other — describe it", 300},
}

func runInit(cmd *cobra.Command, args []string) error {
	reader := bufio.NewReader(os.Stdin)

	// 1. Scan each repo directory.
	fmt.Println("Scanning repositories...")
	var repos []*discovery.RepoInfo
	for _, dir := range args {
		info, err := discovery.ScanRepo(dir)
		if err != nil {
			fmt.Printf("  Warning: skipping %s: %v\n", dir, err)
			continue
		}
		fmt.Printf("  Found: %s (%s, branch: %s, %d recent commits)\n",
			info.Name, info.Path, info.Branch, len(info.RecentLogs))
		repos = append(repos, info)
	}

	if len(repos) == 0 {
		return fmt.Errorf("no valid repositories found")
	}

	// 2. Derive project name.
	suggested := discovery.DeriveProjectName(repos)
	fmt.Printf("\nProject name [%s]: ", suggested)
	projectName := readLine(reader)
	if projectName == "" {
		projectName = suggested
	}

	// Open database (creates if needed).
	dbPath := db.DefaultDBPath()
	if err := db.EnsureDir(dbPath); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}
	conn, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	// Check if project already exists.
	existing, _ := store.GetProject(projectName)
	if existing != nil {
		fmt.Printf("Warning: project %q already exists.\n", projectName)
		fmt.Print("Overwrite? [y/N]: ")
		answer := readLine(reader)
		if !strings.HasPrefix(strings.ToLower(answer), "y") {
			return fmt.Errorf("aborted")
		}
		if err := store.DeleteProject(existing.ID); err != nil {
			return fmt.Errorf("deleting existing project: %w", err)
		}
	}

	// 3. Goal selection.
	fmt.Println("\nWhat's the goal for this project?")
	for i, g := range goalTypes {
		fmt.Printf("  %d. %s\n", i+1, g.Label)
	}
	fmt.Print("\n> ")
	goalChoice := readLine(reader)
	goalIdx := 0
	if n := parseChoice(goalChoice, len(goalTypes)); n >= 0 {
		goalIdx = n
	}
	goal := goalTypes[goalIdx]

	fmt.Print("Describe the work: ")
	goalDesc := readLine(reader)

	// 4. Suggest topics.
	topics := discovery.SuggestTopics(projectName, repos)
	fmt.Printf("\nSuggested topics:\n")
	for _, t := range topics {
		fmt.Printf("  - %s\n", t)
	}
	fmt.Print("Accept? [Y/n]: ")
	answer := readLine(reader)
	if strings.HasPrefix(strings.ToLower(answer), "n") {
		fmt.Print("Enter topics (comma-separated): ")
		input := readLine(reader)
		topics = nil
		for _, t := range strings.Split(input, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				topics = append(topics, t)
			}
		}
		coordTopic := projectName + "/coord"
		hasCoord := false
		for _, t := range topics {
			if t == coordTopic {
				hasCoord = true
				break
			}
		}
		if !hasCoord {
			topics = append(topics, coordTopic)
			fmt.Printf("  Added coordination topic: %s\n", coordTopic)
		}
	}

	// 5. Configure settings.
	defaultMinutes := goal.DefaultSec / 60
	interval := promptMinutes(reader, "Enter poll interval in minutes", defaultMinutes, 1, 480)

	ttl := 48 * time.Hour

	fmt.Printf("Auto-enroll new worktrees? [Y/n]: ")
	autoEnroll := readLine(reader)
	autoEnrollBool := !strings.HasPrefix(strings.ToLower(autoEnroll), "n")

	// 6. Idle auto-pause.
	idlePauseMin := promptMinutes(reader, "Auto-pause after idle (minutes, 0=disabled)", 240, 0, 1440)
	idlePauseSec := int(idlePauseMin.Seconds())

	// 7. Autopilot skip label(s).
	fmt.Printf("\nAutopilot skip label(s) [no-agent]:\n")
	fmt.Printf("  (comma-separated, e.g. \"no-agent, manual, human-only\")\n> ")
	skipLabelInput := readLine(reader)
	skipLabel := ""
	if skipLabelInput != "" {
		skipLabel = skipLabelInput
		// Show a preview of parsed labels.
		var parsed []string
		for _, part := range strings.Split(skipLabel, ",") {
			s := strings.TrimSpace(part)
			if s != "" {
				parsed = append(parsed, s)
			}
		}
		if len(parsed) > 0 {
			var parts []string
			for _, l := range parsed {
				parts = append(parts, fmt.Sprintf("[%s]", l))
			}
			fmt.Printf("  Will match: %s\n", strings.Join(parts, " "))
		}
	}

	// 8. Select LLM provider from configured providers.
	llmProvider := "anthropic"
	configured := config.ConfiguredProviders()
	if len(configured) == 0 {
		fmt.Println("\nNo LLM providers configured. Run 'agent-minder setup' to add one.")
		fmt.Println("Defaulting to 'anthropic' (will use ANTHROPIC_API_KEY env var).")
	} else if len(configured) == 1 {
		llmProvider = configured[0]
		fmt.Printf("\nUsing LLM provider: %s\n", llmProvider)
	} else {
		fmt.Println("\nSelect LLM provider:")
		for i, p := range configured {
			fmt.Printf("  %d. %s\n", i+1, p)
		}
		fmt.Print("> ")
		choice := readLine(reader)
		if n := parseChoice(choice, len(configured)); n >= 0 {
			llmProvider = configured[n]
		}
	}

	// 9. Write to database.
	project := &db.Project{
		Name:                projectName,
		GoalType:            goal.Name,
		GoalDescription:     goalDesc,
		RefreshIntervalSec:  int(interval.Seconds()),
		AnalysisIntervalSec: int(interval.Seconds()),
		MessageTTLSec:       int(ttl.Seconds()),
		AutoEnrollWorktrees: autoEnrollBool,
		IdlePauseSec:        idlePauseSec,
		AutopilotSkipLabel:  skipLabel,
		MinderIdentity:      projectName + "/minder",
		LLMProvider:         llmProvider,
		LLMModel:            "claude-haiku-4-5",
		LLMSummarizerModel:  "claude-haiku-4-5",
		LLMAnalyzerModel:    "claude-sonnet-4-6",
	}
	if err := store.CreateProject(project); err != nil {
		return fmt.Errorf("creating project: %w", err)
	}

	// Add repos and worktrees.
	for _, ri := range repos {
		repo := &db.Repo{
			ProjectID: project.ID,
			Path:      ri.Path,
			ShortName: ri.ShortName,
		}
		if err := store.AddRepo(repo); err != nil {
			return fmt.Errorf("adding repo %s: %w", ri.Name, err)
		}

		// Add discovered worktrees.
		var wts []db.Worktree
		for _, wt := range ri.Worktrees {
			wts = append(wts, db.Worktree{
				Path:   wt.Path,
				Branch: wt.Branch,
			})
		}
		if len(wts) > 0 {
			if err := store.ReplaceWorktrees(repo.ID, wts); err != nil {
				fmt.Printf("  Warning: could not save worktrees for %s: %v\n", ri.Name, err)
			}
		}
	}

	// Add topics.
	for _, name := range topics {
		if err := store.AddTopic(&db.Topic{ProjectID: project.ID, Name: name}); err != nil {
			return fmt.Errorf("adding topic %s: %w", name, err)
		}
	}

	fmt.Printf("\nProject %q initialized!\n", projectName)
	fmt.Printf("  Goal: %s — %s\n", goal.Name, goalDesc)
	fmt.Printf("  Repos: %d\n", len(repos))
	fmt.Printf("  Topics: %s\n", strings.Join(topics, ", "))
	fmt.Printf("  DB: %s\n", dbPath)

	// 10. Offer autopilot enrollment for repos that lack onboarding.
	promptOnboarding(reader, repos)

	fmt.Printf("\nNext: run 'agent-minder start %s' to begin monitoring.\n", projectName)

	return nil
}

func readLine(reader *bufio.Reader) string {
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

func parseChoice(s string, max int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	if n < 1 || n > max {
		return 0
	}
	return n - 1
}

// promptOnboarding checks which repos lack onboarding and offers to run the
// onboarding agent interactively for each one.
func promptOnboarding(reader *bufio.Reader, repos []*discovery.RepoInfo) {
	var unenrolled []*discovery.RepoInfo
	for _, ri := range repos {
		if !onboarding.Exists(ri.Path) {
			unenrolled = append(unenrolled, ri)
		}
	}
	if len(unenrolled) == 0 {
		return
	}

	fmt.Printf("\n%d of %d repo(s) in the project do not have autopilot onboarding.\n", len(unenrolled), len(repos))
	fmt.Println("Onboarding improves agent performance and reliability.")
	fmt.Printf("Would you like to onboard %s now?\n", pluralRepos(len(unenrolled)))
	fmt.Print("Onboarding is optional. It takes roughly 1 minute per repo and requires some manual input. (y/N) ")
	answer := readLine(reader)
	if !strings.HasPrefix(strings.ToLower(answer), "y") {
		return
	}

	for i, ri := range unenrolled {
		fmt.Printf("\nOnboarding repo %d of %d: %s...\n", i+1, len(unenrolled), ri.ShortName)

		if err := runRepoOnboarding(ri); err != nil {
			fmt.Printf("  ✗ Onboarding failed for %s: %v\n", ri.ShortName, err)
			if i < len(unenrolled)-1 {
				fmt.Print("  Continue with remaining repos? (Y/n) ")
				cont := readLine(reader)
				if strings.HasPrefix(strings.ToLower(cont), "n") {
					fmt.Println("  Skipping remaining repos.")
					return
				}
			}
			continue
		}
		fmt.Printf("  ✓ Onboarded %s\n", ri.ShortName)
	}
}

// runRepoOnboarding writes the initial onboarding file with inventory and
// launches the onboarding agent interactively to complete the setup.
func runRepoOnboarding(ri *discovery.RepoInfo) error {
	// Write initial onboarding YAML with mechanical inventory.
	obFile := onboarding.New(ri.Inventory)
	obPath := onboarding.FilePath(ri.Path)
	if err := onboarding.Write(obPath, obFile); err != nil {
		return fmt.Errorf("writing initial onboarding file: %w", err)
	}

	// Build the prompt for the onboarding agent.
	prompt := buildOnboardingPrompt(ri, obPath)

	// Launch claude --agent onboarding interactively.
	cmd := exec.Command("claude", "--agent", "onboarding", "-p", prompt)
	cmd.Dir = ri.Path
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("onboarding agent: %w", err)
	}
	return nil
}

// buildOnboardingPrompt constructs the prompt passed to the onboarding agent,
// summarizing the repo path, onboarding file path, and mechanical inventory.
func buildOnboardingPrompt(ri *discovery.RepoInfo, obPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repo directory: %s\n", ri.Path)
	fmt.Fprintf(&b, "Onboarding file path: %s\n\n", obPath)

	fmt.Fprintln(&b, "## Mechanical Inventory")
	if len(ri.Inventory.Languages) > 0 {
		fmt.Fprintf(&b, "Languages: %s\n", strings.Join(ri.Inventory.Languages, ", "))
	}
	if len(ri.Inventory.PackageManagers) > 0 {
		fmt.Fprintf(&b, "Package managers: %s\n", strings.Join(ri.Inventory.PackageManagers, ", "))
	}
	if len(ri.Inventory.BuildFiles) > 0 {
		fmt.Fprintf(&b, "Build files: %s\n", strings.Join(ri.Inventory.BuildFiles, ", "))
	}
	if len(ri.Inventory.CI) > 0 {
		fmt.Fprintf(&b, "CI: %s\n", strings.Join(ri.Inventory.CI, ", "))
	}
	if ri.Inventory.Tooling.Secrets != "" {
		fmt.Fprintf(&b, "Secrets: %s\n", ri.Inventory.Tooling.Secrets)
	}
	if ri.Inventory.Tooling.Process != "" {
		fmt.Fprintf(&b, "Process manager: %s\n", ri.Inventory.Tooling.Process)
	}
	if ri.Inventory.Tooling.Containers != "" {
		fmt.Fprintf(&b, "Containers: %s\n", ri.Inventory.Tooling.Containers)
	}
	if len(ri.Inventory.Tooling.Env) > 0 {
		fmt.Fprintf(&b, "Env tools: %s\n", strings.Join(ri.Inventory.Tooling.Env, ", "))
	}

	fmt.Fprintln(&b, "\n## Existing Claude Config")
	fmt.Fprintf(&b, "settings.json: %v\n", ri.Inventory.ExistingClaude.SettingsJSON)
	fmt.Fprintf(&b, "CLAUDE.md: %v\n", ri.Inventory.ExistingClaude.ClaudeMD)
	fmt.Fprintf(&b, "Agent definitions: %v\n", ri.Inventory.ExistingClaude.AgentDef)

	return b.String()
}

// pluralRepos returns "these N repos" or "this repo" depending on count.
func pluralRepos(n int) string {
	if n == 1 {
		return "this repo"
	}
	return fmt.Sprintf("these %d repos", n)
}

// promptMinutes asks the user for a duration in minutes, reprompting on invalid input.
func promptMinutes(reader *bufio.Reader, label string, defaultVal, minVal, maxVal int) time.Duration {
	for {
		fmt.Printf("\n%s [%d]: ", label, defaultVal)
		input := readLine(reader)
		if input == "" {
			return time.Duration(defaultVal) * time.Minute
		}
		n, err := strconv.Atoi(input)
		if err != nil {
			fmt.Printf("  Please enter a number between %d and %d.\n", minVal, maxVal)
			continue
		}
		if n < minVal {
			fmt.Printf("  Minimum is %d minute(s).\n", minVal)
			continue
		}
		if n > maxVal {
			fmt.Printf("  Maximum is %d minutes (%d hours).\n", maxVal, maxVal/60)
			continue
		}
		return time.Duration(n) * time.Minute
	}
}
