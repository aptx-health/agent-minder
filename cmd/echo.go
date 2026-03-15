package cmd

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

var echoReverse bool

var echoCmd = &cobra.Command{
	Use:   "echo [args...]",
	Short: "Print back its arguments",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if echoReverse {
			slices.Reverse(args)
		}
		fmt.Println(strings.Join(args, " "))
		return nil
	},
}

func init() {
	echoCmd.Flags().BoolVar(&echoReverse, "reverse", false, "Print arguments in reverse order")
	rootCmd.AddCommand(echoCmd)
}
