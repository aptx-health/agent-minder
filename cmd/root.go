package cmd

import (
	"fmt"
	"os"

	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "agent-minder",
	Short: "Coordination layer on top of agent-msg",
	Long: `A CLI tool that monitors multiple repositories, watches the message bus,
tracks git activity, and keeps both AI agents and the human operator
informed about cross-repo state.

Common workflows:

  # First-time setup — configure API keys and integrations
  agent-minder setup

  # Initialize a project from one or more repos
  agent-minder init ~/repos/my-app ~/repos/my-lib

  # Launch the TUI monitoring dashboard
  agent-minder start my-project

  # Check project status without launching the TUI
  agent-minder status my-project

  # Track GitHub issues or PRs
  agent-minder track my-project owner/repo#42

  # List all configured projects
  agent-minder list

Use "agent-minder <command> --help" for more information about a command.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		config.Init()
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
