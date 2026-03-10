package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var resumeCmd = &cobra.Command{
	Use:   "resume <project>",
	Short: "Restart monitoring from saved state",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("resume: not yet implemented")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(resumeCmd)
}
