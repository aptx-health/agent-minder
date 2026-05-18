package cmd

import (
	"fmt"

	"github.com/aptx-health/agent-minder/internal/reaper"
	"github.com/spf13/cobra"
)

var reaperCmd = &cobra.Command{
	Use:    "reaper",
	Short:  "Sweep orphaned processes left in agent worktrees",
	Hidden: true,
}

var reaperSweepCmd = &cobra.Command{
	Use:   "sweep <worktree-path>",
	Short: "Kill any process whose cwd is inside the given path",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		reaped, err := reaper.Sweep(args[0])
		if err != nil {
			return err
		}
		if len(reaped) == 0 {
			fmt.Println("no orphans found")
			return nil
		}
		for _, r := range reaped {
			fmt.Println(r)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(reaperCmd)
	reaperCmd.AddCommand(reaperSweepCmd)
}
