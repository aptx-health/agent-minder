package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version info set from main.go via goreleaser ldflags.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version of agent-minder",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("agent-minder %s (commit: %s, built: %s)\n", Version, Commit, Date)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
