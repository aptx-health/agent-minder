package deploy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/dustinlange/agent-minder/internal/config"
)

// Dir returns the deploy directory path.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".agent-minder", "deploys")
	}
	return filepath.Join(home, ".agent-minder", "deploys")
}

// PIDPath returns the PID file path for a deploy.
func PIDPath(id string) string {
	return filepath.Join(Dir(), id+".pid")
}

// LogPath returns the log file path for a deploy.
func LogPath(id string) string {
	return filepath.Join(Dir(), id+".log")
}

// IsRunning checks if a deploy daemon is still running.
// Returns (alive, pid).
func IsRunning(id string) (bool, int) {
	data, err := os.ReadFile(PIDPath(id))
	if err != nil {
		return false, 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false, 0
	}
	// Signal 0 checks if process exists without actually sending a signal.
	err = syscall.Kill(pid, 0)
	return err == nil, pid
}

// WritePID writes the current process PID to the deploy's PID file.
func WritePID(id string) error {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create deploy dir: %w", err)
	}
	return os.WriteFile(PIDPath(id), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// RemovePID removes the PID file for a deploy.
func RemovePID(id string) error {
	return os.Remove(PIDPath(id))
}

// RespawnDaemon re-launches the deploy daemon for the given deploy ID.
// Used when a task is restarted but the daemon has already exited.
func RespawnDaemon(id string) error {
	// Resolve GitHub token.
	ghToken := os.Getenv("GITHUB_TOKEN")
	if ghToken == "" {
		ghToken = config.GetIntegrationToken("github")
	}
	if ghToken == "" {
		return fmt.Errorf("no GitHub token found — set GITHUB_TOKEN or run 'agent-minder setup'")
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}

	daemonCmd := exec.Command(exe, "deploy", "--daemon", "--deploy-id", id)
	daemonCmd.Env = append(os.Environ(), "GITHUB_TOKEN="+ghToken)
	daemonCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return fmt.Errorf("create deploy dir: %w", err)
	}
	logFile, err := os.OpenFile(LogPath(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	daemonCmd.Stdout = logFile
	daemonCmd.Stderr = logFile

	if err := daemonCmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start daemon: %w", err)
	}
	_ = logFile.Close()
	return nil
}
