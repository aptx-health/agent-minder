package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var enrollCmd = &cobra.Command{
	Use:   "enroll <project> <repo-dir>",
	Short: "Add a repo or worktree to an active project",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("enroll: not yet implemented")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(enrollCmd)
}
