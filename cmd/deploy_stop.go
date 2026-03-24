package cmd

import (
	"fmt"
	"syscall"
	"time"

	"github.com/dustinlange/agent-minder/internal/api"
	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/deploy"
	"github.com/spf13/cobra"
)

var deployStopCmd = &cobra.Command{
	Use:   "stop <deploy-id>",
	Short: "Stop a running deployment",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDeployStop,
}

func init() {
	deployCmd.AddCommand(deployStopCmd)
}

func runDeployStop(_ *cobra.Command, args []string) error {
	// Remote mode: send stop via HTTP API.
	if client := remoteClient(); client != nil {
		return runDeployStopRemote(client)
	}

	if len(args) == 0 {
		return fmt.Errorf("deploy ID required (or use --remote to stop a remote daemon)")
	}
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

func runDeployStopRemote(client *api.Client) error {
	// First get the status so we can show the deploy ID.
	status, err := client.GetStatus()
	if err != nil {
		return fmt.Errorf("remote: %w", err)
	}

	if !status.Alive {
		fmt.Printf("Deploy %s is not running.\n", status.DeployID)
		return nil
	}

	fmt.Printf("Stopping deploy %s (remote, PID %d)...\n", status.DeployID, status.PID)
	if err := client.Stop(); err != nil {
		return fmt.Errorf("remote stop: %w", err)
	}

	fmt.Printf("Stop signal sent to deploy %s.\n", status.DeployID)
	return nil
}
