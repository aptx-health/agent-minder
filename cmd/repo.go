package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dustinlange/agent-minder/internal/discovery"
	"github.com/dustinlange/agent-minder/internal/onboarding"
	"github.com/spf13/cobra"
)

// defaultAgentLogDir returns the default directory for autopilot agent logs.
func defaultAgentLogDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".agent-minder", "agents")
}

var repoCmd = &cobra.Command{
	Use:   "repo",
	Short: "Manage repository enrollment for autopilot",
	Long: `Commands for enrolling repositories, checking enrollment status, and
refreshing enrollment data. Enrollment configures a repo for optimal
autopilot agent performance by scanning its build system, tooling,
and existing configuration.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var repoEnrollCmd = &cobra.Command{
	Use:   "enroll <dir>",
	Short: "Guided enrollment wizard for a repository",
	Long: `Scans a repository for language, build system, tooling, and existing
configuration, then guides you through enrollment for optimal autopilot
performance.`,
	Example: `  # Enroll a repository for autopilot
  agent-minder repo enroll ~/repos/my-app

  # Enroll with project-scoped agent log scanning
  agent-minder repo enroll ~/repos/my-app --project my-project`,
	Args: cobra.ExactArgs(1),
	RunE: runRepoEnroll,
}

var repoStatusCmd = &cobra.Command{
	Use:   "status <dir>",
	Short: "Show enrollment state for a repository",
	Long: `Displays the current enrollment state including mechanical inventory
and any existing enrollment file data.`,
	Example: `  # Check enrollment status
  agent-minder repo status ~/repos/my-app`,
	Args: cobra.ExactArgs(1),
	RunE: runRepoStatus,
}

var repoRefreshCmd = &cobra.Command{
	Use:   "refresh <dir>",
	Short: "Re-scan repository and update enrollment data",
	Long: `Re-runs the mechanical inventory scan and updates the enrollment file
with fresh results. Useful after adding new build tools, changing CI,
or modifying the project structure.`,
	Example: `  # Refresh enrollment data after adding new tooling
  agent-minder repo refresh ~/repos/my-app`,
	Args: cobra.ExactArgs(1),
	RunE: runRepoRefresh,
}

func init() {
	rootCmd.AddCommand(repoCmd)
	repoCmd.AddCommand(repoEnrollCmd)
	repoCmd.AddCommand(repoStatusCmd)
	repoCmd.AddCommand(repoRefreshCmd)

	// --project flag scopes agent log scanning to a specific project.
	for _, cmd := range []*cobra.Command{repoEnrollCmd, repoStatusCmd} {
		cmd.Flags().String("project", "", "project name to scope agent log scanning (e.g., minder-improvement)")
	}
}

func runRepoEnroll(cmd *cobra.Command, args []string) error {
	dir := args[0]

	// Step 1: Mechanical inventory.
	fmt.Println("Scanning repository...")
	info, err := discovery.ScanRepo(dir)
	if err != nil {
		return fmt.Errorf("scanning %s: %w", dir, err)
	}

	// Check for existing enrollment file.
	hasEnrollment := onboarding.Exists(info.Path)

	// Scan agent logs for prior permission failures (scoped to project).
	projectName, _ := cmd.Flags().GetString("project")
	logDir := defaultAgentLogDir()
	permFailures := discovery.ScanAgentLogs(logDir, projectName)
	var availableProjects []string
	if projectName == "" {
		availableProjects = discovery.ListAgentLogProjects(logDir)
	}

	// Step 2: Present overview.
	fmt.Println()
	printRepoHeader(info)
	printInventory(info.Inventory)
	printEnrollmentStatus(hasEnrollment, info.Path)
	printPermissionFailures(permFailures, projectName, availableProjects)

	if hasEnrollment {
		fmt.Println("\nEnrollment file already exists.")
		fmt.Println("Run 'agent-minder repo refresh' to re-scan, or 'agent-minder repo status' for details.")
		return nil
	}

	// Step 3: Create initial onboarding file with inventory (agent will fill in context + permissions).
	f := onboarding.New(info.Inventory)
	filePath := onboarding.FilePath(info.Path)
	if err := onboarding.Write(filePath, f); err != nil {
		return fmt.Errorf("writing initial onboarding file: %w", err)
	}
	fmt.Printf("\nCreated initial onboarding file: %s\n", filePath)

	// Step 4: Launch onboarding agent.
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		fmt.Println("\nClaude CLI not found on PATH. Install it to run the onboarding agent.")
		fmt.Println("The mechanical inventory has been saved. Re-run 'agent-minder repo enroll' after installing Claude CLI.")
		return nil
	}

	enrollPrompt := buildEnrollmentPrompt(info, filePath, permFailures)
	systemPrompt := onboardingSystemPrompt + "\n\n" + enrollPrompt
	agentCmd := exec.Command(claudePath, "--append-system-prompt", systemPrompt,
		"Begin the onboarding interview for this repository.")
	agentCmd.Dir = info.Path
	agentCmd.Stdin = os.Stdin
	agentCmd.Stdout = os.Stdout
	agentCmd.Stderr = os.Stderr

	fmt.Println("\nLaunching onboarding agent...")
	fmt.Println("The agent will ask you a few questions about your repository, then generate configuration files.")
	fmt.Println()

	if err := agentCmd.Run(); err != nil {
		fmt.Printf("\nOnboarding agent exited with error: %v\n", err)
		fmt.Println("The mechanical inventory has been saved. You can re-run 'agent-minder repo enroll' to try again.")
		return nil
	}

	// Step 5: Post-agent validation.
	fmt.Println()
	printPostAgentSummary(info.Path)

	return nil
}

func runRepoStatus(cmd *cobra.Command, args []string) error {
	dir := args[0]

	info, err := discovery.ScanRepo(dir)
	if err != nil {
		return fmt.Errorf("scanning %s: %w", dir, err)
	}

	printRepoHeader(info)

	// Show enrollment file if it exists.
	if onboarding.Exists(info.Path) {
		f, err := onboarding.Parse(onboarding.FilePath(info.Path))
		if err != nil {
			return fmt.Errorf("parsing enrollment file: %w", err)
		}
		printEnrollmentFile(f)
	} else {
		fmt.Println("\nEnrollment: not enrolled")
	}

	// Always show current inventory.
	fmt.Println()
	printInventory(info.Inventory)

	// Show permission failures if any (scoped to project).
	projectName, _ := cmd.Flags().GetString("project")
	logDir := defaultAgentLogDir()
	permFailures := discovery.ScanAgentLogs(logDir, projectName)
	var availableProjects []string
	if projectName == "" {
		availableProjects = discovery.ListAgentLogProjects(logDir)
	}
	printPermissionFailures(permFailures, projectName, availableProjects)

	return nil
}

func runRepoRefresh(cmd *cobra.Command, args []string) error {
	dir := args[0]

	fmt.Println("Re-scanning repository...")
	info, err := discovery.ScanRepo(dir)
	if err != nil {
		return fmt.Errorf("scanning %s: %w", dir, err)
	}

	filePath := onboarding.FilePath(info.Path)
	if !onboarding.Exists(info.Path) {
		fmt.Println("No enrollment file found. Run 'agent-minder repo enroll' first.")
		fmt.Println()
		printRepoHeader(info)
		printInventory(info.Inventory)
		return nil
	}

	// Parse existing file, update inventory, write back.
	f, err := onboarding.Parse(filePath)
	if err != nil {
		return fmt.Errorf("parsing enrollment file: %w", err)
	}

	f.Inventory = info.Inventory
	f.ScannedAt = time.Now().UTC()
	if err := onboarding.Write(filePath, f); err != nil {
		return fmt.Errorf("writing enrollment file: %w", err)
	}

	fmt.Println("Enrollment file updated with fresh inventory.")
	fmt.Println()
	printRepoHeader(info)
	printInventory(info.Inventory)

	return nil
}

// ---------- display helpers ----------

func printRepoHeader(info *discovery.RepoInfo) {
	fmt.Printf("Repository: %s\n", info.Name)
	fmt.Printf("  Path:   %s\n", info.Path)
	fmt.Printf("  Branch: %s\n", info.Branch)
	if info.RemoteURL != "" {
		fmt.Printf("  Remote: %s\n", info.RemoteURL)
	}
}

func printInventory(inv onboarding.Inventory) {
	fmt.Println("\nInventory:")
	printField("Languages", inv.Languages)
	printField("Package managers", inv.PackageManagers)
	printField("Build files", inv.BuildFiles)
	printField("CI", inv.CI)

	// Tooling subsection.
	hasTooling := inv.Tooling.Secrets != "" || inv.Tooling.Process != "" ||
		inv.Tooling.Containers != "" || len(inv.Tooling.Env) > 0
	if hasTooling {
		fmt.Println("  Tooling:")
		if inv.Tooling.Secrets != "" {
			fmt.Printf("    Secrets:    %s\n", inv.Tooling.Secrets)
		}
		if inv.Tooling.Process != "" {
			fmt.Printf("    Process:    %s\n", inv.Tooling.Process)
		}
		if inv.Tooling.Containers != "" {
			fmt.Printf("    Containers: %s\n", inv.Tooling.Containers)
		}
		if len(inv.Tooling.Env) > 0 {
			fmt.Printf("    Env:        %s\n", strings.Join(inv.Tooling.Env, ", "))
		}
	}

	// Claude config.
	fmt.Println("  Claude Code config:")
	fmt.Printf("    CLAUDE.md:        %s\n", checkMark(inv.ExistingClaude.ClaudeMD))
	fmt.Printf("    settings.json:    %s\n", checkMark(inv.ExistingClaude.SettingsJSON))
	fmt.Printf("    Agent definition: %s\n", checkMark(inv.ExistingClaude.AgentDef))
}

func printEnrollmentStatus(hasEnrollment bool, repoPath string) {
	if hasEnrollment {
		fmt.Printf("\nEnrollment file: %s\n", onboarding.FilePath(repoPath))
	} else {
		fmt.Println("\nEnrollment file: not found")
	}
}

func printEnrollmentFile(f *onboarding.File) {
	fmt.Println("\nEnrollment:")
	fmt.Printf("  Version:    %d\n", f.Version)
	fmt.Printf("  Scanned at: %s\n", f.ScannedAt.Format("2006-01-02 15:04:05 UTC"))

	if f.Context.BuildCommand != "" || f.Context.TestCommand != "" || f.Context.LintCommand != "" {
		fmt.Println("  Context:")
		if f.Context.BuildCommand != "" {
			fmt.Printf("    Build: %s\n", f.Context.BuildCommand)
		}
		if f.Context.TestCommand != "" {
			fmt.Printf("    Test:  %s\n", f.Context.TestCommand)
		}
		if f.Context.LintCommand != "" {
			fmt.Printf("    Lint:  %s\n", f.Context.LintCommand)
		}
		if f.Context.SpecialInstructions != "" {
			fmt.Printf("    Notes: %s\n", f.Context.SpecialInstructions)
		}
		if len(f.Context.ToolsNeeded) > 0 {
			fmt.Printf("    Tools: %s\n", strings.Join(f.Context.ToolsNeeded, ", "))
		}
	}

	if len(f.Permissions.AllowedTools) > 0 {
		fmt.Println("  Permissions:")
		for _, tool := range f.Permissions.AllowedTools {
			fmt.Printf("    - %s\n", tool)
		}
	}

	fmt.Printf("  Validation: %s\n", f.Validation.Status)
	if len(f.Validation.Failures) > 0 {
		for _, failure := range f.Validation.Failures {
			fmt.Printf("    - %s\n", failure)
		}
	}
}

func printPermissionFailures(failures []string, projectName string, availableProjects []string) {
	scope := "all projects"
	if projectName != "" {
		scope = fmt.Sprintf("project %q", projectName)
	}
	if len(failures) == 0 {
		fmt.Printf("\nPrior agent runs (%s): no permission failures detected\n", scope)
	} else {
		fmt.Printf("\nPrior agent runs (%s): %d permission failure(s) detected\n", scope, len(failures))
		for _, f := range failures {
			fmt.Printf("  - %s\n", f)
		}
	}
	if projectName == "" && len(availableProjects) > 0 {
		fmt.Printf("  Hint: use --project to scope. Available: %s\n", strings.Join(availableProjects, ", "))
	}
}

func printField(label string, values []string) {
	if len(values) == 0 {
		fmt.Printf("  %-18s (none)\n", label+":")
		return
	}
	fmt.Printf("  %-18s %s\n", label+":", strings.Join(values, ", "))
}

func checkMark(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// buildEnrollmentPrompt constructs the initial prompt for the onboarding agent
// with the mechanical inventory context.
func buildEnrollmentPrompt(info *discovery.RepoInfo, onboardingPath string, permFailures []string) string {
	var b strings.Builder

	b.WriteString("## Mechanical Inventory\n\n")
	fmt.Fprintf(&b, "Repo directory: %s\n", info.Path)
	fmt.Fprintf(&b, "Onboarding file path: %s\n\n", onboardingPath)

	inv := info.Inventory

	writeList := func(label string, items []string) {
		if len(items) > 0 {
			fmt.Fprintf(&b, "- %s: %s\n", label, strings.Join(items, ", "))
		}
	}

	writeList("Languages", inv.Languages)
	writeList("Package managers", inv.PackageManagers)
	writeList("Build files", inv.BuildFiles)
	writeList("CI", inv.CI)

	if inv.Tooling.Secrets != "" {
		fmt.Fprintf(&b, "- Secrets manager: %s\n", inv.Tooling.Secrets)
	}
	if inv.Tooling.Process != "" {
		fmt.Fprintf(&b, "- Process manager: %s\n", inv.Tooling.Process)
	}
	if inv.Tooling.Containers != "" {
		fmt.Fprintf(&b, "- Containers: %s\n", inv.Tooling.Containers)
	}
	if len(inv.Tooling.Env) > 0 {
		fmt.Fprintf(&b, "- Env management: %s\n", strings.Join(inv.Tooling.Env, ", "))
	}

	b.WriteString("\n### Existing Claude Code config\n\n")
	fmt.Fprintf(&b, "- CLAUDE.md: %s\n", checkMark(inv.ExistingClaude.ClaudeMD))
	fmt.Fprintf(&b, "- settings.json: %s\n", checkMark(inv.ExistingClaude.SettingsJSON))
	fmt.Fprintf(&b, "- Agent definition: %s\n", checkMark(inv.ExistingClaude.AgentDef))

	if len(permFailures) > 0 {
		b.WriteString("\n### Prior agent permission failures\n\n")
		for _, f := range permFailures {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\nConsider adding permissions to cover these tool patterns.\n")
	}

	return b.String()
}

// printPostAgentSummary checks which artifacts the onboarding agent created
// and prints a summary.
func printPostAgentSummary(repoDir string) {
	fmt.Println("Enrollment summary:")

	artifacts := []struct {
		label string
		path  string
	}{
		{"Onboarding file", onboarding.FilePath(repoDir)},
		{"Claude settings", filepath.Join(repoDir, ".claude", "settings.json")},
		{"Autopilot agent", filepath.Join(repoDir, ".claude", "agents", "autopilot.md")},
		{"Reviewer agent", filepath.Join(repoDir, ".claude", "agents", "reviewer.md")},
	}

	for _, a := range artifacts {
		if _, err := os.Stat(a.path); err == nil {
			fmt.Printf("  ✓ %s: %s\n", a.label, a.path)
		} else {
			fmt.Printf("  - %s: not created\n", a.label)
		}
	}
}
