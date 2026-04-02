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
to generate .agent-minder/onboarding.yaml and customize agent instructions.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runEnroll,
}

func init() {
	rootCmd.AddCommand(enrollCmd)
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
	if onboarding.Exists(info.Path) {
		fmt.Printf("\nOnboarding file already exists: %s\n", onboarding.FilePath(info.Path))
		fmt.Println("Delete it first if you want to re-enroll.")
		return nil
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

	var installed, skipped []string
	for _, tmpl := range supervisor.AgentTemplates() {
		if !tmpl.Required {
			continue
		}
		if source, ok := existingNames[tmpl.Name]; ok {
			skipped = append(skipped, fmt.Sprintf("%s (already exists at %s level)", tmpl.Name, source))
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

## Your goals

### 1. Onboarding configuration
- Ask about the test command (verify it works by running it)
- Ask about the build command
- Ask about any lint commands
- Ask about the base branch for PRs (main, dev, develop, etc.) — check git branch output
- Ask about special instructions for agents working in this repo
- Determine which tools agents need (start with defaults, add as needed)
- Update the onboarding file with the gathered information (including base_branch in the context section)

### 2. Customize agent definitions
Agent definition files are in .claude/agents/*.md. Each file has YAML frontmatter
between --- markers, followed by instruction text.

CRITICAL RULES for agent definitions:
- The YAML frontmatter (between the --- markers) must NOT be modified. It contains
  contract fields that the orchestrator parses. Changing it will break the agent.
- You may ONLY modify the instruction text BELOW the closing --- marker.
- Customize the instructions based on what you learn about the repo: coding conventions,
  test patterns, important directories, review standards, etc.
- Keep instructions concise and actionable.

Reference frontmatter for each agent type:
%s

### 3. Validate
- Run validation to ensure the onboarding config is correct
- Check that all agent definitions in .claude/agents/ are parseable

Be concise and efficient. Most repos need minimal configuration.`,
		filePath, info.Path, info.Inventory.Languages, agentReport, templateRef.String())
}
