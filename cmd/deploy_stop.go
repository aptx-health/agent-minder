package cmd

import (
	"fmt"
	"syscall"
	"time"

	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/deploy"
	"github.com/spf13/cobra"
)

var deployStopCmd = &cobra.Command{
	Use:   "stop <deploy-id>",
	Short: "Stop a running deployment",
	Args:  cobra.ExactArgs(1),
	RunE:  runDeployStop,
}

func init() {
	deployCmd.AddCommand(deployStopCmd)
}

func runDeployStop(cmd *cobra.Command, args []string) error {
	id := args[0]

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	project, err := store.GetProject(id)
	if err != nil {
		return fmt.Errorf("deploy %q not found", id)
	}
	if !project.IsDeploy {
		return fmt.Errorf("%q is not a deploy project", id)
	}

	alive, pid := deploy.IsRunning(id)
	if !alive {
		fmt.Printf("Deploy %s is not running.\n", id)
		return nil
	}

	fmt.Printf("Stopping deploy %s (PID %d)...\n", id, pid)
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("send SIGTERM: %w", err)
	}

	// Poll for up to 30 seconds.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		alive, _ := deploy.IsRunning(id)
		if !alive {
			break
		}
		time.Sleep(1 * time.Second)
	}

	// Mark remaining running tasks as stopped.
	tasks, _ := store.GetAutopilotTasks(project.ID)
	stopped := 0
	for _, t := range tasks {
		if t.Status == "running" || t.Status == "queued" {
			_ = store.UpdateAutopilotTaskStatus(t.ID, "stopped")
			stopped++
		}
	}

	_ = deploy.RemovePID(id)
	fmt.Printf("Deploy %s stopped.", id)
	if stopped > 0 {
		fmt.Printf(" %d tasks marked as stopped.", stopped)
	}
	fmt.Println()
	return nil
}
