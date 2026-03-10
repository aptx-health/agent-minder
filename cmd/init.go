package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init [repo-dir ...]",
	Short: "Bootstrap a new project from one or more repo directories",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("init: not yet implemented")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}
