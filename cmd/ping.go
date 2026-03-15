package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var pingCmd = &cobra.Command{
	Use:   "ping",
	Short: "Print pong",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("pong")
	},
}

func init() {
	rootCmd.AddCommand(pingCmd)
}
