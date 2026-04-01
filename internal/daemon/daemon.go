// Package daemon provides the deploy daemon lifecycle: PID files, heartbeat, crash recovery.
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
)

// BaseDir returns the agent-minder state directory.
func BaseDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".agent-minder"
	}
	return filepath.Join(home, ".agent-minder")
}

// DeployDir returns the deploys directory.
func DeployDir() string {
	return filepath.Join(BaseDir(), "deploys")
}

// PIDPath returns the PID file path for a deployment.
func PIDPath(deployID string) string {
	return filepath.Join(DeployDir(), deployID+".pid")
}

// HeartbeatPath returns the heartbeat file path for a deployment.
func HeartbeatPath(deployID string) string {
	return filepath.Join(DeployDir(), deployID+".heartbeat")
}

// LogPath returns the daemon log path for a deployment.
func LogPath(deployID string) string {
	return filepath.Join(BaseDir(), "agents", deployID+".log")
}

// WritePID writes the current process PID to the PID file.
func WritePID(deployID string) error {
	if err := os.MkdirAll(DeployDir(), 0755); err != nil {
		return err
	}
	return os.WriteFile(PIDPath(deployID), []byte(strconv.Itoa(os.Getpid())), 0644)
}

// RemovePID removes the PID file.
func RemovePID(deployID string) {
	_ = os.Remove(PIDPath(deployID))
}

// IsRunning checks if a daemon is running for the given deployment.
func IsRunning(deployID string) (bool, int) {
	data, err := os.ReadFile(PIDPath(deployID))
	if err != nil {
		return false, 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false, 0
	}
	// Check if process exists.
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, pid
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false, pid
	}
	return true, pid
}

// WriteHeartbeat writes the current timestamp to the heartbeat file.
func WriteHeartbeat(deployID string) error {
	return os.WriteFile(HeartbeatPath(deployID), []byte(time.Now().UTC().Format(time.RFC3339)), 0644)
}

// ReadHeartbeat reads the last heartbeat time.
func ReadHeartbeat(deployID string) (time.Time, error) {
	data, err := os.ReadFile(HeartbeatPath(deployID))
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
}

// RemoveHeartbeat removes the heartbeat file.
func RemoveHeartbeat(deployID string) {
	_ = os.Remove(HeartbeatPath(deployID))
}

// StartHeartbeat starts a goroutine that writes heartbeat every 30s.
// Returns a stop function that blocks until the goroutine has fully exited,
// guaranteeing no further writes after it returns.
func StartHeartbeat(deployID string) func() {
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		_ = WriteHeartbeat(deployID)
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_ = WriteHeartbeat(deployID)
			}
		}
	}()
	return func() {
		close(done)
		<-exited
	}
}

// WasCrashShutdown checks if the previous daemon crashed (stale heartbeat).
func WasCrashShutdown(deployID string) bool {
	hb, err := ReadHeartbeat(deployID)
	if err != nil {
		return false
	}
	// If heartbeat is older than 60s and process is not running, it was a crash.
	alive, _ := IsRunning(deployID)
	return !alive && time.Since(hb) > 60*time.Second
}

// CleanStalePID removes stale PID and heartbeat files.
func CleanStalePID(deployID string) {
	RemovePID(deployID)
	RemoveHeartbeat(deployID)
}

// RecoverDaemonState resets running jobs to queued after a crash.
func RecoverDaemonState(store *db.Store, deployID string) (int64, error) {
	return store.TransitionStaleRunningJobs(deployID)
}

// Daemonize re-executes the current process as a background daemon.
// Returns the child PID on success.
func Daemonize(args []string, logPath string) (int, error) {
	// Ensure log directory exists.
	_ = os.MkdirAll(filepath.Dir(logPath), 0755)

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return 0, fmt.Errorf("open daemon log: %w", err)
	}

	exe, err := os.Executable()
	if err != nil {
		_ = logFile.Close()
		return 0, fmt.Errorf("find executable: %w", err)
	}

	attr := &os.ProcAttr{
		Dir: ".",
		Env: os.Environ(),
		Files: []*os.File{
			os.Stdin,
			logFile,
			logFile,
		},
		Sys: &syscall.SysProcAttr{
			Setsid: true, // New process group — survives parent exit.
		},
	}

	proc, err := os.StartProcess(exe, args, attr)
	if err != nil {
		_ = logFile.Close()
		return 0, fmt.Errorf("start daemon: %w", err)
	}

	pid := proc.Pid
	_ = proc.Release()
	_ = logFile.Close()

	return pid, nil
}
