package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version info injected from main.go (set by goreleaser ldflags).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// SetVersionInfo is called from main() to pass ldflags-injected values.
func SetVersionInfo(v, c, d string) {
	version = v
	commit = c
	date = d
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version of agent-minder",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("agent-minder %s (commit: %s, built: %s)\n", version, commit, date)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
