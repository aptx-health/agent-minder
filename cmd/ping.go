package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var pingCount int

var pingCmd = &cobra.Command{
	Use:   "ping",
	Short: "Print pong",
	Run: func(cmd *cobra.Command, args []string) {
		for range pingCount {
			fmt.Fprintln(cmd.OutOrStdout(), "pong")
		}
	},
}

func init() {
	pingCmd.Flags().IntVarP(&pingCount, "count", "n", 1, "number of times to print pong")
	rootCmd.AddCommand(pingCmd)
}
