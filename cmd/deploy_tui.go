package cmd

import (
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dustinlange/agent-minder/internal/api"
	"github.com/dustinlange/agent-minder/internal/remotetui"
	"github.com/spf13/cobra"
)

var (
	tuiRemote       string
	tuiAPIKey       string
	tuiTaskPoll     int
	tuiAnalysisPoll int
)

var deployTuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Live dashboard for a remote deploy daemon",
	Long: `Launch a k9s-like TUI that connects to a remote daemon's status API,
providing live-updating task status, dependency graph visualization,
analysis results, and agent log streaming.`,
	Example: `  # Connect to a remote daemon
  agent-minder deploy tui --remote vps:7749

  # With API key authentication
  agent-minder deploy tui --remote vps:7749 --api-key mykey

  # Custom poll intervals
  agent-minder deploy tui --remote vps:7749 --task-poll 3 --analysis-poll 15`,
	RunE: runDeployTui,
}

func init() {
	deployCmd.AddCommand(deployTuiCmd)

	deployTuiCmd.Flags().StringVar(&tuiRemote, "remote", "", "Remote daemon address (host:port)")
	deployTuiCmd.Flags().StringVar(&tuiAPIKey, "api-key", "", "API key for authentication (or MINDER_API_KEY env var)")
	deployTuiCmd.Flags().IntVar(&tuiTaskPoll, "task-poll", 5, "Task status poll interval in seconds")
	deployTuiCmd.Flags().IntVar(&tuiAnalysisPoll, "analysis-poll", 30, "Analysis poll interval in seconds")

	_ = deployTuiCmd.MarkFlagRequired("remote")
}

func runDeployTui(_ *cobra.Command, _ []string) error {
	// Resolve API key from flag or env var.
	apiKey := tuiAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("MINDER_API_KEY")
	}

	client := api.NewClient(tuiRemote, apiKey)

	// Verify connectivity before launching TUI.
	_, err := client.GetStatus()
	if err != nil {
		return fmt.Errorf("cannot connect to %s: %w", tuiRemote, err)
	}

	taskPoll := time.Duration(tuiTaskPoll) * time.Second
	analysisPoll := time.Duration(tuiAnalysisPoll) * time.Second

	model := remotetui.New(client, tuiRemote, taskPoll, analysisPoll)
	p := tea.NewProgram(model)

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}
	return nil
}
