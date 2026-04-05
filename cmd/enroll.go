package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/aptx-health/agent-minder/internal/discovery"
	gitpkg "github.com/aptx-health/agent-minder/internal/git"
	"github.com/aptx-health/agent-minder/internal/onboarding"
	"github.com/aptx-health/agent-minder/internal/supervisor"
	"github.com/spf13/cobra"
)

var enrollCmd = &cobra.Command{
	Use:   "enroll [repo-dir]",
	Short: "Scan a repo and generate onboarding configuration",
	Long: `Scans a repository for build/test commands, languages, and CI config,
installs required agent definitions, then launches an interactive Claude agent
to generate .agent-minder/onboarding.yaml, customize agent instructions,
and configure jobs.yaml with trigger routes and cron schedules.

Use --force to re-enroll a repo that's already been set up.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runEnroll,
}

var flagEnrollForce bool

func init() {
	rootCmd.AddCommand(enrollCmd)
	enrollCmd.Flags().BoolVar(&flagEnrollForce, "force", false, "Re-enroll, replacing existing configuration")
}

func runEnroll(cmd *cobra.Command, args []string) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}

	if !gitpkg.IsRepo(dir) {
		return fmt.Errorf("%s is not a git repository", dir)
	}

	repoDir, err := gitpkg.TopLevel(dir)
	if err != nil {
		return fmt.Errorf("resolve repo: %w", err)
	}

	fmt.Println("Scanning repository...")
	info, err := discovery.ScanRepo(repoDir)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}

	// Check for existing enrollment.
	if onboarding.Exists(info.Path) && !flagEnrollForce {
		fmt.Printf("\nOnboarding file already exists: %s\n", onboarding.FilePath(info.Path))
		fmt.Println("Use --force to re-enroll.")
		return nil
	}
	if flagEnrollForce {
		fmt.Println("Re-enrolling (--force)...")
	}

	// Install required agent definitions.
	agentReport := installAgentDefs(repoDir)

	// Create initial file with inventory.
	f := onboarding.New(info.Inventory)
	filePath := onboarding.FilePath(info.Path)
	if err := onboarding.Write(filePath, f); err != nil {
		return fmt.Errorf("write onboarding: %w", err)
	}
	fmt.Printf("Created initial onboarding file: %s\n", filePath)

	// Launch interactive Claude agent to fill in context + permissions.
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		fmt.Println("\nClaude CLI not found. The inventory has been saved.")
		fmt.Println("Install Claude CLI and re-run 'minder enroll' to complete setup.")
		return nil
	}

	systemPrompt := buildEnrollSystemPrompt(info, filePath, agentReport)
	agentCmd := exec.Command(claudePath, "--append-system-prompt", systemPrompt,
		"Begin the onboarding interview for this repository.")
	agentCmd.Dir = info.Path
	agentCmd.Stdin = os.Stdin
	agentCmd.Stdout = os.Stdout
	agentCmd.Stderr = os.Stderr

	fmt.Println("\nLaunching onboarding agent...")
	fmt.Println("The agent will ask about your repository and generate configuration.")

	if err := agentCmd.Run(); err != nil {
		fmt.Printf("\nAgent exited with error: %v\n", err)
		fmt.Println("The inventory has been saved. Re-run 'minder enroll' to try again.")
		return nil
	}

	// Validate agent definitions after the agent finishes.
	if errs := supervisor.ValidateAgentDefs(repoDir); len(errs) > 0 {
		fmt.Println("\nAgent definition validation errors:")
		for _, e := range errs {
			fmt.Printf("  - %s\n", e)
		}
		fmt.Println("Fix these issues in .claude/agents/ and re-run validation.")
	} else {
		fmt.Println("\nAll agent definitions validated successfully.")
	}

	fmt.Println("Onboarding complete.")
	return nil
}

// installAgentDefs discovers existing agents and installs missing required ones.
// Returns a report string for the onboarding agent prompt.
func installAgentDefs(repoDir string) string {
	existing := supervisor.DiscoverAgents(repoDir)
	existingNames := map[string]string{} // name → source
	for _, a := range existing {
		existingNames[a.Name] = a.Source
	}

	var installed, skipped, optional []string
	for _, tmpl := range supervisor.AgentTemplates() {
		if _, ok := existingNames[tmpl.Name]; ok {
			skipped = append(skipped, fmt.Sprintf("%s (already exists at %s level)", tmpl.Name, existingNames[tmpl.Name]))
			continue
		}
		if !tmpl.Required {
			optional = append(optional, tmpl.Name)
			continue
		}
		path, err := supervisor.InstallAgentDef(repoDir, tmpl)
		if err != nil {
			fmt.Printf("Warning: failed to install %s agent: %v\n", tmpl.Name, err)
			continue
		}
		installed = append(installed, tmpl.Name)
		fmt.Printf("Installed agent definition: %s\n", path)
	}

	var report strings.Builder
	if len(installed) > 0 {
		fmt.Fprintf(&report, "Installed agent definitions: %s\n", strings.Join(installed, ", "))
	}
	if len(skipped) > 0 {
		fmt.Fprintf(&report, "Existing agent definitions: %s\n", strings.Join(skipped, "; "))
	}
	if len(optional) > 0 {
		fmt.Fprintf(&report, "Optional agents available: %s\n", strings.Join(optional, ", "))
	}
	return report.String()
}

func buildEnrollSystemPrompt(info *discovery.RepoInfo, filePath, agentReport string) string {
	// Build template reference for the agent.
	var templateRef strings.Builder
	for _, tmpl := range supervisor.AgentTemplates() {
		fmt.Fprintf(&templateRef, "\n### %s.md\nFrontmatter (DO NOT MODIFY):\n```yaml\n---\n%s\n---\n```\n",
			tmpl.Name, tmpl.Frontmatter)
	}

	return fmt.Sprintf(`You are an onboarding agent for agent-minder. Your job is to interview the user
about their repository and fill in the onboarding configuration file.

The repository has been scanned and an initial file created at: %s

Repository: %s
Languages: %v

%s

## Your goals (in order)

### 1. Onboarding configuration
- Ask about the test command (verify it works by running it)
- Ask about the build command
- Ask about any lint commands
- Ask about the base branch for PRs (main, dev, develop, etc.) — check git branch output
- Ask about special instructions for agents working in this repo
- Determine which tools agents need (start with defaults, add as needed)
- Update the onboarding file with the gathered information (including base_branch in the context section)

### 2. Choose which agents to install
Present all available agent types and let the user choose which to install.
Required agents (autopilot, reviewer) are always installed. Optional agents:

- **bug-fixer**: Specialized bug-fixing agent. Triggered by the "bug" label on issues.
  Relevant for any repo.
- **dependency-updater**: Scans for outdated deps, updates, tests, and PRs.
  Relevant if the repo has package managers (go.mod, package.json, requirements.txt, Cargo.toml).
- **security-scanner**: Runs security audit tools and reports findings as issues.
  Relevant for any repo, especially those with dependencies.
- **doc-updater**: Reviews recent changes and updates documentation.
  Relevant if the repo has README.md, CHANGELOG.md, or API docs.
- **spike**: Research and discovery agent. Investigates questions, feasibility analysis,
  and security impact assessment. Triggered by the "spike" label. Does NOT write code —
  posts structured findings as a comment. Relevant for any repo.

### 3. Run research sub-agents (IMPORTANT — do this BEFORE writing agent definitions)
For EACH agent the user wants installed (including required agents), spawn a research
sub-agent using the Bash tool to run:

  claude -p --model sonnet "Research this codebase for writing an optimized <agent-name> agent definition. Focus on: <focus areas>. Output ONLY the instruction body markdown — no frontmatter, no preamble."

Run ALL research sub-agents in parallel (use & and wait) to save time:

  (claude -p --model sonnet "..." > /tmp/research-autopilot.md 2>/dev/null &
   claude -p --model sonnet "..." > /tmp/research-reviewer.md 2>/dev/null &
   claude -p --model sonnet "..." > /tmp/research-bug-fixer.md 2>/dev/null &
   wait)

Each research agent should focus on what matters for its agent type:

- **autopilot**: Architecture, entry points, build/test/lint commands, git conventions,
  commit message style, PR conventions, CLAUDE.md guidance, common pitfalls.
- **reviewer**: Code review standards, test coverage expectations, lint/format rules,
  CI checks, common anti-patterns, what to watch for in PRs.
- **bug-fixer**: Test frameworks, how to reproduce bugs, error handling patterns,
  how to write regression tests in this project's style, key bug-prone areas.
- **dependency-updater**: Package managers and config files, exact commands to check
  and update deps, lock file handling, version constraints, multiple ecosystems.
- **security-scanner**: Available security tools, audit commands, sensitive code areas,
  secrets management, existing security config files.
- **doc-updater**: Existing docs, documentation style, doc build tools, areas that
  drift when code changes, doc linting tools.
- **spike**: Key domain areas, common question patterns, where important design
  decisions are documented, external services/integrations the repo depends on.

After all research agents complete, read each output file.

### 4. Write agent definitions using research results
Agent definition files go in .claude/agents/<name>.md. Each file has YAML frontmatter
between --- markers, followed by instruction text.

CRITICAL RULES:
- The YAML frontmatter (between the --- markers) must NOT be modified. It contains
  contract fields that the orchestrator parses. Changing it will break the agent.
- You may ONLY customize the instruction text BELOW the closing --- marker.
- Use the research sub-agent output as the foundation for each instruction body.
  Refine it: ensure it references actual commands, directories, and conventions
  found in THIS repo. Remove any generic filler that doesn't add value.
- Keep instructions concise and actionable.

Reference frontmatter for each agent type (use these EXACTLY):
%s

### 5. Configure jobs.yaml
Create or update .agent-minder/jobs.yaml with trigger routes and cron schedules
based on which agents the user installed:

- For **bug-fixer**: add a trigger route: trigger: "label:bug"
- For **autopilot**: add a trigger route: trigger: "label:agent-ready"
- For **dependency-updater**: add a weekly cron schedule (e.g., Monday morning)
- For **security-scanner**: add a weekly cron schedule (e.g., Wednesday morning)
- For **doc-updater**: add a weekly cron schedule (e.g., Friday morning)
- For **spike**: add a trigger route: trigger: "label:spike"

Ask the user what cron times work for them. Use UTC times in the cron expressions.

Example jobs.yaml structure:

  jobs:
    bug-fix:
      trigger: "label:bug"
      agent: bug-fixer
      description: "Fix bugs automatically"
      budget: 5.0

    weekly-deps:
      schedule: "0 15 * * 1"
      agent: dependency-updater
      description: "Check for outdated dependencies"
      budget: 3.0

### 6. Validate
- Validate agent defs: minder agents list --repo .
- Validate jobs.yaml: minder jobs list --repo .
  Both use the actual Go YAML parser. If minder is not on PATH, use go run ./cmd/minder.
  Do NOT use Python or Ruby YAML parsers — they have incompatibilities with Go's parser.
- If validation fails, fix the issue and re-validate

Be concise and efficient. The research agents do the heavy lifting — your job is to
orchestrate the interview, launch research, and assemble the results into polished
agent definitions.`,
		filePath, info.Path, info.Inventory.Languages, agentReport, templateRef.String())
}
