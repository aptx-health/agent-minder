package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var pauseCmd = &cobra.Command{
	Use:   "pause <project>",
	Short: "Stop the polling loop",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("pause: not yet implemented")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(pauseCmd)
}
