package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status <project>",
	Short: "Show catch-up summary for the human (no AI call)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("status: not yet implemented")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
