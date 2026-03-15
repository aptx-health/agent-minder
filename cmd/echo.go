package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var echoCmd = &cobra.Command{
	Use:   "echo [args...]",
	Short: "Print back its arguments",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println(strings.Join(args, " "))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(echoCmd)
}
