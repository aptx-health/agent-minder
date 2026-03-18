package cmd

import (
	"fmt"
	"os"
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

	// Steps 3-4 (enrollment agent + validation) depend on future work.
	fmt.Println("\nEnrollment agent not yet available.")
	fmt.Println("The mechanical inventory above will be used as context when the enrollment agent is implemented.")

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
