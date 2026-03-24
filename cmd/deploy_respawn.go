package cmd

import (
	"fmt"

	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/deploy"
	"github.com/spf13/cobra"
)

var deployRespawnCmd = &cobra.Command{
	Use:   "respawn <deploy-id>",
	Short: "Respawn a crashed deployment daemon",
	Long: `Respawn a deploy daemon that exited uncleanly (crash, OOM, reboot).

On startup the new daemon will:
  - Detect the previous crash via stale PID file
  - Reset running tasks back to queued
  - Clean up orphaned git worktrees
  - Resume processing queued tasks

If the daemon is already running, this command will error.`,
	Args: cobra.ExactArgs(1),
	RunE: runDeployRespawn,
}

func init() {
	deployCmd.AddCommand(deployRespawnCmd)
}

func runDeployRespawn(cmd *cobra.Command, args []string) error {
	id := args[0]

	// Verify the deploy project exists.
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

	// Check if already running.
	if alive, pid := deploy.IsRunning(id); alive {
		return fmt.Errorf("deploy %s is already running (PID %d) — use 'deploy stop' first", id, pid)
	}

	// Check if there's actually work remaining.
	tasks, err := store.GetAutopilotTasks(project.ID)
	if err != nil {
		return fmt.Errorf("get tasks: %w", err)
	}
	hasWork := false
	for _, t := range tasks {
		if t.Status == "queued" || t.Status == "running" || t.Status == "review" ||
			t.Status == "reviewing" || t.Status == "blocked" || t.Status == "manual" {
			hasWork = true
			break
		}
	}
	if !hasWork {
		fmt.Printf("Deploy %s has no remaining work — all tasks are in terminal state.\n", id)
		deploy.CleanStalePID(id)
		return nil
	}

	// Clean stale PID before respawn.
	deploy.CleanStalePID(id)

	// Respawn the daemon.
	if err := deploy.RespawnDaemon(id); err != nil {
		return fmt.Errorf("respawn: %w", err)
	}

	fmt.Printf("Deploy %s respawned.\n", id)
	fmt.Printf("Log:    %s\n", deploy.LogPath(id))
	fmt.Printf("Status: agent-minder deploy status %s\n", id)
	return nil
}
