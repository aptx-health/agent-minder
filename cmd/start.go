package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var startCmd = &cobra.Command{
	Use:   "start <project>",
	Short: "Launch monitoring loop via Claude CLI",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("start: not yet implemented")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(startCmd)
}
