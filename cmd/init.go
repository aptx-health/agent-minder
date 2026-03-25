package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/discovery"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	"github.com/dustinlange/agent-minder/internal/onboarding"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init [repo-dir ...]",
	Short: "Bootstrap a new project from one or more repo directories",
	Long: `Interactive wizard that scans one or more repository directories, derives a
project name, and walks you through goal selection, poll interval, topic
configuration, LLM provider selection, and optional autopilot onboarding.

All state is stored in SQLite at ~/.agent-minder/minder.db.`,
	Example: `  # Initialize a project from a single repo
  agent-minder init ~/repos/my-app

  # Initialize from multiple repos (creates a multi-repo project)
  agent-minder init ~/repos/frontend ~/repos/backend ~/repos/shared-lib`,
	Args: cobra.MinimumNArgs(1),
	RunE: runInit,
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

	// 8. Base branch for autopilot worktrees and PRs.
	// Auto-detect from the first repo, but always ask the user to confirm.
	detectedBranch := "main"
	if len(repos) > 0 {
		if detected, err := gitpkg.DefaultBranch(repos[0].Path); err == nil && detected != "" {
			detectedBranch = detected
		}
	}
	fmt.Printf("\nAutopilot base branch (worktrees and PR targets) [%s]: ", detectedBranch)
	baseBranchInput := readLine(reader)
	baseBranch := detectedBranch
	if baseBranchInput != "" {
		baseBranch = baseBranchInput
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
		AutopilotBaseBranch: baseBranch,
		MinderIdentity:      projectName + "/minder",
		LLMSummarizerModel:  "haiku",
		LLMAnalyzerModel:    "opus",
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
// launches an interactive Claude session with onboarding instructions injected
// via --append-system-prompt. On failure, the partially-written onboarding
// file is removed so the repo can be retried on the next init.
func runRepoOnboarding(ri *discovery.RepoInfo) error {
	// Write initial onboarding YAML with mechanical inventory.
	obFile := onboarding.New(ri.Inventory)
	obPath := onboarding.FilePath(ri.Path)
	if err := onboarding.Write(obPath, obFile); err != nil {
		return fmt.Errorf("writing initial onboarding file: %w", err)
	}

	// Build the system prompt: onboarding instructions + inventory context.
	systemPrompt := onboardingSystemPrompt + "\n\n" + buildInventoryContext(ri, obPath)

	// Launch interactive Claude session with onboarding instructions injected.
	// The positional prompt sends the first message so the agent starts
	// immediately with its interview questions.
	cmd := exec.Command("claude", "--append-system-prompt", systemPrompt,
		"Begin the onboarding interview for this repository.")
	cmd.Dir = ri.Path
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		// Clean up the partial onboarding file so the repo isn't
		// incorrectly skipped on the next init run.
		_ = os.Remove(obPath)
		return fmt.Errorf("onboarding agent: %w", err)
	}
	return nil
}

// buildInventoryContext formats the mechanical inventory into a context block
// that is appended to the onboarding system prompt.
func buildInventoryContext(ri *discovery.RepoInfo, obPath string) string {
	var b strings.Builder
	fmt.Fprintln(&b, "## Repo Context")
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

// onboardingSystemPrompt contains the behavioral instructions for the onboarding
// session. This is injected via --append-system-prompt so there is no dependency
// on an external agent definition file. Keep in sync with agents/onboarding.md.
const onboardingSystemPrompt = `You are an onboarding agent for agent-minder. Your job is to interview the user about their repository, then generate configuration files that enable autonomous agents to work in this repo safely and effectively.

The mechanical inventory and repo context are provided below. Use them as your starting point.

## Step 1: Ask targeted questions

Based on the inventory, ask the user **at most 5 targeted questions** about things that cannot be mechanically detected. Tailor your questions to what the inventory actually found — skip questions that don't apply.

Choose from questions like these (adapt wording to match what was detected):

- **Secrets management**: "I see doppler.yaml — do agents need doppler run -- to access env vars, or are secrets available through other means?"
- **Process management**: "I see Procfile.dev — do you use Overmind or Foreman? Do agents need to start services before running tests?"
- **Build commands**: "What's the canonical command to build the project? I see a Makefile — is make build the right command, or something else?"
- **Test commands**: "What's the canonical command to run tests? Are there integration tests that need external services (databases, APIs, etc.)?"
- **Lint commands**: "I see golangci-lint in your tooling — is golangci-lint run the standard lint command?"
- **Custom tooling**: "Is there anything unusual about your build, test, or deploy workflow that an autonomous agent should know about?"
- **Special constraints**: "Are there files or directories that agents should never modify? Any commands that are dangerous to run?"

Guidelines:
- Do NOT ask about things the inventory already answers definitively
- Do NOT do a full interview — keep it focused and efficient
- If the inventory is comprehensive and the repo is straightforward, you may ask fewer than 5 questions
- Wait for the user's answers before proceeding to artifact generation

## Step 2: Generate artifacts

Based on the inventory and the user's answers, generate four artifacts. Present each to the user for review before writing to disk.

### Artifact 1: Onboarding file

Update the existing onboarding file's context and permissions sections with the user-provided information. The onboarding file was already created by the mechanical scan with the inventory section populated — you are filling in the remaining sections.

The context fields to populate:
- build_command: e.g., "go build ./...", "make build", "npm run build"
- test_command: e.g., "go test ./...", "make test", "npm test"
- lint_command: e.g., "golangci-lint run", "npm run lint"
- special_instructions: Free-text notes for agents (secrets prefixes, service startup, etc.)
- tools_needed: CLI tools agents need: [go, git, gh, make, golangci-lint, etc.]

The permissions.allowed_tools list rules:
- Format: Use spaces inside Bash() patterns — e.g., "Bash(git *)", "Bash(go build *)". Do NOT use colons — the colon syntax silently fails to match commands in settings.json and onboarding.yaml.
- Each entry is "Bash(<command> *)" where <command> comes from tools_needed and the build/test/lint commands
- For multi-word commands, use spaces between all words: "Bash(go build *)", "Bash(doppler run -- *)"
- Do NOT include overly broad patterns like "Bash(*)" — be specific
- Be THOROUGH — agents will be blocked if any needed permission is missing. It is much better to include a permission that isn't used than to omit one that is needed.

Always include these baseline permissions:
- "Read", "Edit", "Write", "Glob", "Grep" — agents need file access
- "Bash(ls *)", "Bash(mkdir *)", "Bash(which *)", "Bash(wc *)" — basic filesystem operations
- Git (use individual subcommands, not "Bash(git *)"): "Bash(git add *)", "Bash(git commit *)", "Bash(git checkout *)", "Bash(git log *)", "Bash(git diff *)", "Bash(git status *)", "Bash(git branch *)", "Bash(git show *)", "Bash(git remote *)", "Bash(git fetch *)", "Bash(git push *)", "Bash(git pull *)", "Bash(git rebase *)", "Bash(git worktree *)", "Bash(git stash *)", "Bash(git merge *)"
- GitHub CLI: "Bash(gh issue *)", "Bash(gh pr *)", "Bash(gh api *)"

Add language/framework-specific permissions based on detected inventory:
- Go: "Bash(go test *)", "Bash(go build *)", "Bash(go vet *)", "Bash(go get *)", "Bash(go mod *)", "Bash(go fmt *)", "Bash(go doc *)", "Bash(go env *)", "Bash(go run *)", "Bash(gofmt *)"
- Node/JS: "Bash(npm *)", "Bash(npx *)" (or yarn/pnpm equivalents), "Bash(node *)"
- Python: "Bash(python *)", "Bash(pip *)", "Bash(pytest *)" (or poetry/uv equivalents)
- Ruby: "Bash(bundle *)", "Bash(ruby *)", "Bash(rake *)"
- Rust: "Bash(cargo *)", "Bash(rustc *)"

Add tooling-specific permissions:
- Linters: "Bash(golangci-lint *)", "Bash(eslint *)", "Bash(flake8 *)", etc.
- Formatters: "Bash(prettier *)", "Bash(black *)", etc.
- Pre-commit hooks: "Bash(lefthook *)", "Bash(pre-commit *)", "Bash(husky *)"
- Build tools: "Bash(make *)", "Bash(cmake *)", etc.
- Secrets manager if detected: "Bash(doppler run -- *)", "Bash(vault *)", etc.
- Process manager if detected: "Bash(overmind *)", "Bash(foreman *)", etc.
- Container tools if detected: "Bash(docker *)", "Bash(docker-compose *)", etc.
- File search: "Bash(fd *)" or "Bash(find *)", "Bash(rg *)" if available

Rules for updating the onboarding file:
- Use the existing onboarding file structure — do NOT rewrite the inventory section
- Read the current onboarding file, update the context and permissions sections, and write back
- Do NOT modify the validation section — it is managed by a separate process
- Keep tools_needed to tools that agents will actually invoke (not libraries or runtimes they won't call directly)
- special_instructions should be concise, actionable text that an agent can follow

### Artifact 2: .claude/settings.json

Generate a Claude Code settings file with permissions derived from the onboarding file's permissions.allowed_tools list. The onboarding file is the source of truth; settings.json is the runtime artifact that Claude Code reads.

If .claude/settings.json already exists, read it first and merge your permissions with the existing configuration — do NOT overwrite other settings.

The permissions.allow array should contain exactly the same entries as the onboarding file's permissions.allowed_tools list.

### Artifact 3: .claude/agents/autopilot.md

Generate a project-specific autopilot agent definition only if .claude/agents/autopilot.md does not already exist in the target repo. If one already exists, skip this artifact and tell the user.

If a global autopilot template exists at ~/.claude/agents/autopilot.md, use it as the base and customize it. Otherwise, generate a fresh definition.

Customize with:
- A "Project-specific guidance" section listing build, test, lint commands and special instructions
- The standard autopilot workflow sections (first steps, pre-check, decision, constraints)

### Artifact 4: .claude/agents/reviewer.md

Generate a project-specific reviewer agent definition only if .claude/agents/reviewer.md does not already exist in the target repo. If one already exists, skip this artifact and tell the user.

If a global reviewer template exists at ~/.claude/agents/reviewer.md, use it as the base and customize it. Otherwise, generate a fresh definition.

Customize with:
- A "Project-specific guidance" section listing test, lint commands and special instructions
- Language-specific things to watch for during review (e.g., Go: goroutine leaks, deferred closes; JS: async/await pitfalls)
- The standard reviewer workflow sections (first steps, review process, fix protocol, structured assessment, constraints)

## Step 3: Review with user

Before writing any files, present all artifacts to the user in a clear format. Ask: "Does this look correct? I'll write these files when you confirm. Let me know if you'd like to change anything."

## Step 4: Write files

After the user confirms:
1. Write the updated onboarding file to the onboarding file path
2. Write .claude/settings.json to the repo directory (creating .claude/ if needed)
3. Write .claude/agents/autopilot.md to the repo directory (if generating one)
4. Write .claude/agents/reviewer.md to the repo directory (if generating one)

Report what was written and their paths.

## Important constraints

- Only write files within the target repository directory
- Never overwrite existing files without reading them first and merging appropriately
- Keep all generated content minimal and actionable
- The onboarding file schema is fixed — do not add fields or change the YAML structure
- If you're unsure about a tool or command, ask the user rather than guessing`

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
