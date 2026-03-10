package cmd

import (
	"fmt"
	"time"

	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/dustinlange/agent-minder/internal/state"
	"github.com/spf13/cobra"
)

var pauseCmd = &cobra.Command{
	Use:   "pause <project>",
	Short: "Stop the polling loop",
	Args:  cobra.ExactArgs(1),
	RunE:  runPause,
}

func init() {
	rootCmd.AddCommand(pauseCmd)
}

func runPause(cmd *cobra.Command, args []string) error {
	project := args[0]

	cfg, err := config.Load(project)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if cfg.ClaudeSessionID == "" {
		return fmt.Errorf("project %q is not running — nothing to pause", project)
	}

	sessionID := cfg.ClaudeSessionID

	// Record pause time in state file.
	st, err := state.Load(project)
	if err == nil && st.Raw != "" {
		pauseNote := fmt.Sprintf("\n## Paused\n- Time: %s\n- Session: %s\n",
			time.Now().UTC().Format("2006-01-02 15:04:05 UTC"), sessionID)
		if err := state.Save(project, st.Raw+pauseNote); err != nil {
			fmt.Printf("Warning: could not update state file: %v\n", err)
		}
	}

	// Clear session ID to mark as paused.
	cfg.ClaudeSessionID = ""
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	fmt.Printf("Paused project %q (was session: %s)\n", project, sessionID)
	fmt.Printf("State file preserved. Use 'agent-minder resume %s' to restart.\n", project)

	return nil
}
