package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var pauseCmd = &cobra.Command{
	Use:   "pause <project>",
	Short: "Pause/resume is now handled in the TUI (press 'p')",
	Long: `This command is deprecated. Pause and resume are now handled inside
the TUI monitoring dashboard using the 'p' key.

Launch the dashboard with 'agent-minder start <project>' and press
'p' to toggle pause/resume.`,
	Example: `  # Launch the TUI and use 'p' to pause
  agent-minder start my-project`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Pause/resume is handled inside the TUI dashboard.")
		fmt.Printf("Run 'agent-minder start %s' and press 'p' to toggle.\n", args[0])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(pauseCmd)
}
