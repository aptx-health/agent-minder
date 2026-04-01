package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch interactive TUI dashboard",
	Long:  "Connect to a running deployment's HTTP API and display a live dashboard.",
	RunE:  runTUI,
}

var (
	flagTUIRemote string
	flagTUIKey    string
)

func init() {
	rootCmd.AddCommand(tuiCmd)
	tuiCmd.Flags().StringVar(&flagTUIRemote, "remote", "", "Remote daemon address (host:port)")
	tuiCmd.Flags().StringVar(&flagTUIKey, "api-key", "", "API key")
}

func runTUI(cmd *cobra.Command, args []string) error {
	if flagTUIRemote == "" {
		return fmt.Errorf("--remote is required (e.g. --remote localhost:7749)")
	}

	// TODO: Phase 6 — launch bubbletea TUI connected to daemon API.
	fmt.Printf("TUI not yet implemented. Use 'minder status --remote %s' for now.\n", flagTUIRemote)
	return nil
}
