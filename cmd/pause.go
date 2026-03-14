package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var pauseCmd = &cobra.Command{
	Use:   "pause <project>",
	Short: "Pause/resume is now handled in the TUI (press 'p')",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Pause/resume is handled inside the TUI dashboard.")
		fmt.Printf("Run 'agent-minder start %s' and press 'p' to toggle.\n", args[0])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(pauseCmd)
}
