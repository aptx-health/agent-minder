package cmd

import (
	"github.com/spf13/cobra"
)

var resumeCmd = &cobra.Command{
	Use:   "resume <project>",
	Short: "Resume monitoring (alias for start — state persists in SQLite)",
	Args:  cobra.ExactArgs(1),
	RunE:  runStart, // Reuses start — the poller picks up from DB state.
}

func init() {
	rootCmd.AddCommand(resumeCmd)
}
