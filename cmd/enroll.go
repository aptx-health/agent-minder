package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/aptx-health/agent-minder/internal/discovery"
	gitpkg "github.com/aptx-health/agent-minder/internal/git"
	"github.com/aptx-health/agent-minder/internal/onboarding"
	"github.com/spf13/cobra"
)

var enrollCmd = &cobra.Command{
	Use:   "enroll [repo-dir]",
	Short: "Scan a repo and generate onboarding configuration",
	Long: `Scans a repository for build/test commands, languages, and CI config,
then launches an interactive Claude agent to generate .agent-minder/onboarding.yaml.`,
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

	systemPrompt := buildEnrollSystemPrompt(info, filePath)
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

	fmt.Println("\nOnboarding complete.")
	return nil
}

func buildEnrollSystemPrompt(info *discovery.RepoInfo, filePath string) string {
	return fmt.Sprintf(`You are an onboarding agent for agent-minder. Your job is to interview the user
about their repository and fill in the onboarding configuration file.

The repository has been scanned and an initial file created at: %s

Repository: %s
Languages: %v

Your goals:
1. Ask about the test command (verify it works by running it)
2. Ask about the build command
3. Ask about any lint commands
4. Ask about special instructions for agents working in this repo
5. Determine which tools agents need (start with defaults, add as needed)
6. Update the onboarding file with the gathered information
7. Run validation to ensure the config is correct

Be concise and efficient. Most repos need minimal configuration.`, filePath, info.Path, info.Inventory.Languages)
}
