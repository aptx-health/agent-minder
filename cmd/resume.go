package cmd

import (
	"github.com/spf13/cobra"
)

var resumeCmd = &cobra.Command{
	Use:   "resume <project>",
	Short: "Resume monitoring (alias for start — state persists in SQLite)",
	Long: `Resume monitoring for a project. This is an alias for 'start' — all
state is persisted in SQLite, so the poller picks up exactly where
it left off.`,
	Example: `  # Resume monitoring a project
  agent-minder resume my-project`,
	Args: cobra.ExactArgs(1),
	RunE: runStart, // Reuses start — the poller picks up from DB state.
}

func init() {
	rootCmd.AddCommand(resumeCmd)
}
