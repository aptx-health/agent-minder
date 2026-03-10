package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "agent-minder",
	Short: "Coordination layer on top of agent-msg",
	Long:  "A CLI tool that monitors multiple repositories, watches the message bus, tracks git activity, and keeps both AI agents and the human operator informed about cross-repo state.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
