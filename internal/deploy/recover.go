// Package deploy provides daemon lifecycle management for deploy mode.
package deploy

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/dustinlange/agent-minder/internal/db"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
)

// HeartbeatPath returns the heartbeat file path for a deploy.
func HeartbeatPath(id string) string {
	return filepath.Join(Dir(), id+".heartbeat")
}

// WriteHeartbeat writes the current timestamp to the heartbeat file.
func WriteHeartbeat(id string) error {
	return os.WriteFile(HeartbeatPath(id), []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
}

// ReadHeartbeat returns the last heartbeat time, or zero if no heartbeat exists.
func ReadHeartbeat(id string) time.Time {
	data, err := os.ReadFile(HeartbeatPath(id))
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, string(data))
	if err != nil {
		return time.Time{}
	}
	return t
}

// RemoveHeartbeat removes the heartbeat file.
func RemoveHeartbeat(id string) {
	_ = os.Remove(HeartbeatPath(id))
}

// StartHeartbeat starts a goroutine that writes heartbeat every interval.
// Returns a stop function.
func StartHeartbeat(id string, interval time.Duration) func() {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		_ = WriteHeartbeat(id) // write immediately
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = WriteHeartbeat(id)
			}
		}
	}()
	return func() { close(stop) }
}

// WasCrashShutdown detects if the previous daemon exited uncleanly.
// Returns true if a PID file exists but the process is not running.
func WasCrashShutdown(id string) bool {
	_, err := os.Stat(PIDPath(id))
	if os.IsNotExist(err) {
		return false
	}
	alive, _ := IsRunning(id)
	return !alive
}

// CleanStalePID removes the PID and heartbeat files left by a crashed daemon.
func CleanStalePID(id string) {
	_ = RemovePID(id)
	RemoveHeartbeat(id)
}

// RecoverDaemonState performs post-crash recovery for a deploy daemon:
//   - Resets stale running tasks to queued
//   - Cleans up orphaned git worktrees
//   - Also resets any "reviewing" tasks back to "review" (review agent was interrupted)
//
// Returns the number of tasks recovered and any error.
func RecoverDaemonState(store *db.Store, project *db.Project, repoDir string) (int, error) {
	recovered := 0

	// Reset running tasks to queued.
	reset, err := store.TransitionStaleRunningTasks(project.ID)
	if err != nil {
		return 0, fmt.Errorf("reset stale running tasks: %w", err)
	}
	if reset > 0 {
		log.Printf("Recovery: reset %d stale running tasks to queued", reset)
		recovered += reset
	}

	// Reset reviewing tasks back to review (review agent was interrupted).
	tasks, err := store.GetAutopilotTasks(project.ID)
	if err != nil {
		return recovered, fmt.Errorf("get tasks: %w", err)
	}
	for _, t := range tasks {
		if t.Status == "reviewing" {
			if err := store.UpdateAutopilotTaskStatus(t.ID, "review"); err != nil {
				log.Printf("Recovery: failed to reset reviewing task #%d: %v", t.IssueNumber, err)
				continue
			}
			log.Printf("Recovery: reset reviewing task #%d back to review", t.IssueNumber)
			recovered++
		}
	}

	// Clean orphaned worktrees.
	cleaned := cleanOrphanedWorktrees(project.Name, repoDir)
	if cleaned > 0 {
		log.Printf("Recovery: cleaned %d orphaned worktrees", cleaned)
	}

	// Prune git's worktree bookkeeping.
	_ = gitpkg.WorktreePrune(repoDir)

	return recovered, nil
}

// cleanOrphanedWorktrees removes worktree directories that don't correspond to
// any active (running/review/queued) task. Returns the count cleaned.
func cleanOrphanedWorktrees(projectName, repoDir string) int {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	worktreeBase := filepath.Join(home, ".agent-minder", "worktrees", projectName)
	entries, err := os.ReadDir(worktreeBase)
	if err != nil {
		return 0
	}
	cleaned := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(worktreeBase, e.Name())
		if err := gitpkg.WorktreeRemove(repoDir, path); err != nil {
			_ = os.RemoveAll(path)
		}
		cleaned++
	}
	return cleaned
}
