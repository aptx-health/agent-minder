package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is the current version of agent-minder.
const Version = "0.2.1-dev"

var rootCmd = &cobra.Command{
	Use:     "minder",
	Version: Version,
	Short:   "Self-hosted agent deploy daemon with dependency-aware dispatch",
	Long: `Agent-minder orchestrates Claude Code agents on GitHub issues.
It builds dependency graphs, dispatches agents in parallel worktrees,
learns from review feedback, and provides a real-time dashboard.`,
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
