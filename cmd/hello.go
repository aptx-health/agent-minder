package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var helloCmd = &cobra.Command{
	Use:   "hello",
	Short: "Print a greeting message",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Hello from agent-minder")
	},
}

func init() {
	rootCmd.AddCommand(helloCmd)
}
