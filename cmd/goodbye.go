package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var goodbyeCmd = &cobra.Command{
	Use:   "goodbye",
	Short: "Print a farewell message",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Goodbye from agent-minder")
	},
}

func init() {
	rootCmd.AddCommand(goodbyeCmd)
}
