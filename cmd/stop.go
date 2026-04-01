package cmd

import (
	"fmt"
	"os"
	"syscall"

	"github.com/aptx-health/agent-minder/internal/daemon"
	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:   "stop [deploy-id]",
	Short: "Stop a running deployment",
	Args:  cobra.ExactArgs(1),
	RunE:  runStop,
}

var (
	flagStopRemote string
	flagStopKey    string
)

func init() {
	rootCmd.AddCommand(stopCmd)
	stopCmd.Flags().StringVar(&flagStopRemote, "remote", "", "Remote daemon address")
	stopCmd.Flags().StringVar(&flagStopKey, "api-key", "", "API key")
}

func runStop(cmd *cobra.Command, args []string) error {
	deployID := args[0]

	// Remote mode.
	if flagStopRemote != "" {
		client := daemon.NewClient("http://"+flagStopRemote, flagStopKey)
		if err := client.Stop(); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
		fmt.Println("Stop signal sent.")
		return nil
	}

	// Local mode — send SIGTERM to the daemon.
	alive, pid := daemon.IsRunning(deployID)
	if !alive {
		fmt.Printf("Deploy %s is not running.\n", deployID)
		daemon.CleanStalePID(deployID)
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("send SIGTERM to %d: %w", pid, err)
	}

	fmt.Printf("Sent SIGTERM to deploy %s (PID %d)\n", deployID, pid)
	return nil
}
