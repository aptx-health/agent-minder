package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// setTestHome overrides HOME so that BaseDir() returns a temp-based path.
// Returns a cleanup function that restores the original HOME.
func setTestHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	orig := os.Getenv("HOME")
	t.Setenv("HOME", tmp)
	t.Cleanup(func() {
		// t.Setenv already restores, but ensure deploy dir is gone.
		_ = os.RemoveAll(filepath.Join(tmp, ".agent-minder"))
	})
	_ = orig
	return tmp
}

func TestWritePID_CreatesPIDFile(t *testing.T) {
	setTestHome(t)
	deployID := "test-write-pid"

	if err := WritePID(deployID); err != nil {
		t.Fatalf("WritePID failed: %v", err)
	}

	data, err := os.ReadFile(PIDPath(deployID))
	if err != nil {
		t.Fatalf("reading PID file: %v", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("PID file content is not a valid integer: %q", string(data))
	}
	if pid != os.Getpid() {
		t.Errorf("PID mismatch: got %d, want %d", pid, os.Getpid())
	}
}

func TestWritePID_CreatesDeployDir(t *testing.T) {
	setTestHome(t)
	deployID := "test-mkdir"

	if err := WritePID(deployID); err != nil {
		t.Fatalf("WritePID failed: %v", err)
	}

	info, err := os.Stat(DeployDir())
	if err != nil {
		t.Fatalf("DeployDir does not exist after WritePID: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("DeployDir is not a directory")
	}
}

func TestRemovePID_RemovesPIDFile(t *testing.T) {
	setTestHome(t)
	deployID := "test-remove-pid"

	if err := WritePID(deployID); err != nil {
		t.Fatalf("WritePID failed: %v", err)
	}

	RemovePID(deployID)

	if _, err := os.Stat(PIDPath(deployID)); !os.IsNotExist(err) {
		t.Errorf("PID file still exists after RemovePID")
	}
}

func TestRemovePID_NoopWhenMissing(t *testing.T) {
	setTestHome(t)
	// Should not panic or error when file doesn't exist.
	RemovePID("nonexistent-deploy")
}

func TestWriteHeartbeat_ReadHeartbeat_RoundTrip(t *testing.T) {
	setTestHome(t)
	deployID := "test-heartbeat-rt"

	// Ensure deploy dir exists.
	if err := os.MkdirAll(DeployDir(), 0755); err != nil {
		t.Fatalf("creating deploy dir: %v", err)
	}

	before := time.Now().UTC().Truncate(time.Second)

	if err := WriteHeartbeat(deployID); err != nil {
		t.Fatalf("WriteHeartbeat failed: %v", err)
	}

	after := time.Now().UTC().Add(time.Second)

	hb, err := ReadHeartbeat(deployID)
	if err != nil {
		t.Fatalf("ReadHeartbeat failed: %v", err)
	}

	if hb.Before(before) || hb.After(after) {
		t.Errorf("heartbeat time %v not in expected range [%v, %v]", hb, before, after)
	}
}

func TestReadHeartbeat_MissingFile(t *testing.T) {
	setTestHome(t)

	_, err := ReadHeartbeat("nonexistent")
	if err == nil {
		t.Error("expected error reading nonexistent heartbeat, got nil")
	}
}

func TestReadHeartbeat_MalformedContent(t *testing.T) {
	setTestHome(t)
	deployID := "test-malformed-hb"

	if err := os.MkdirAll(DeployDir(), 0755); err != nil {
		t.Fatalf("creating deploy dir: %v", err)
	}

	if err := os.WriteFile(HeartbeatPath(deployID), []byte("not-a-timestamp"), 0644); err != nil {
		t.Fatalf("writing malformed heartbeat: %v", err)
	}

	_, err := ReadHeartbeat(deployID)
	if err == nil {
		t.Error("expected error parsing malformed heartbeat, got nil")
	}
}

func TestIsRunning_CurrentProcess(t *testing.T) {
	setTestHome(t)
	deployID := "test-is-running"

	if err := WritePID(deployID); err != nil {
		t.Fatalf("WritePID failed: %v", err)
	}

	alive, pid := IsRunning(deployID)
	if !alive {
		t.Error("expected current process to be reported as running")
	}
	if pid != os.Getpid() {
		t.Errorf("PID mismatch: got %d, want %d", pid, os.Getpid())
	}
}

func TestIsRunning_NoPIDFile(t *testing.T) {
	setTestHome(t)

	alive, pid := IsRunning("nonexistent-deploy")
	if alive {
		t.Error("expected not running when PID file is missing")
	}
	if pid != 0 {
		t.Errorf("expected pid=0 for missing PID file, got %d", pid)
	}
}

func TestIsRunning_InvalidPIDContent(t *testing.T) {
	setTestHome(t)
	deployID := "test-invalid-pid"

	if err := os.MkdirAll(DeployDir(), 0755); err != nil {
		t.Fatalf("creating deploy dir: %v", err)
	}

	if err := os.WriteFile(PIDPath(deployID), []byte("not-a-number"), 0644); err != nil {
		t.Fatalf("writing invalid PID: %v", err)
	}

	alive, pid := IsRunning(deployID)
	if alive {
		t.Error("expected not running with invalid PID content")
	}
	if pid != 0 {
		t.Errorf("expected pid=0 for invalid PID content, got %d", pid)
	}
}

func TestIsRunning_DeadProcess(t *testing.T) {
	setTestHome(t)
	deployID := "test-dead-process"

	if err := os.MkdirAll(DeployDir(), 0755); err != nil {
		t.Fatalf("creating deploy dir: %v", err)
	}

	// Use a very high PID that almost certainly doesn't exist.
	// PID 4194304 is above the typical Linux max (4194304 is 2^22).
	deadPID := 4194300
	if err := os.WriteFile(PIDPath(deployID), []byte(strconv.Itoa(deadPID)), 0644); err != nil {
		t.Fatalf("writing dead PID: %v", err)
	}

	alive, pid := IsRunning(deployID)
	if alive {
		t.Errorf("expected dead PID %d to not be running", deadPID)
	}
	if pid != deadPID {
		t.Errorf("expected pid=%d, got %d", deadPID, pid)
	}
}

func TestWasCrashShutdown_StaleHeartbeat(t *testing.T) {
	setTestHome(t)
	deployID := "test-crash"

	if err := os.MkdirAll(DeployDir(), 0755); err != nil {
		t.Fatalf("creating deploy dir: %v", err)
	}

	// Write a stale heartbeat (2 minutes ago).
	staleTime := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339)
	if err := os.WriteFile(HeartbeatPath(deployID), []byte(staleTime), 0644); err != nil {
		t.Fatalf("writing stale heartbeat: %v", err)
	}

	// No PID file → process is not running → should detect crash.
	if !WasCrashShutdown(deployID) {
		t.Error("expected WasCrashShutdown=true with stale heartbeat and no running process")
	}
}

func TestWasCrashShutdown_RecentHeartbeat(t *testing.T) {
	setTestHome(t)
	deployID := "test-no-crash-recent"

	if err := os.MkdirAll(DeployDir(), 0755); err != nil {
		t.Fatalf("creating deploy dir: %v", err)
	}

	// Write a fresh heartbeat (just now).
	if err := WriteHeartbeat(deployID); err != nil {
		t.Fatalf("WriteHeartbeat failed: %v", err)
	}

	// Fresh heartbeat → not a crash (even without running process).
	if WasCrashShutdown(deployID) {
		t.Error("expected WasCrashShutdown=false with recent heartbeat")
	}
}

func TestWasCrashShutdown_NoHeartbeatFile(t *testing.T) {
	setTestHome(t)

	// No heartbeat file → not a crash.
	if WasCrashShutdown("nonexistent") {
		t.Error("expected WasCrashShutdown=false when heartbeat file is missing")
	}
}

func TestWasCrashShutdown_ProcessStillRunning(t *testing.T) {
	setTestHome(t)
	deployID := "test-still-alive"

	if err := os.MkdirAll(DeployDir(), 0755); err != nil {
		t.Fatalf("creating deploy dir: %v", err)
	}

	// Stale heartbeat but process (us) is running → not a crash.
	staleTime := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339)
	if err := os.WriteFile(HeartbeatPath(deployID), []byte(staleTime), 0644); err != nil {
		t.Fatalf("writing stale heartbeat: %v", err)
	}
	if err := WritePID(deployID); err != nil {
		t.Fatalf("WritePID failed: %v", err)
	}

	if WasCrashShutdown(deployID) {
		t.Error("expected WasCrashShutdown=false when process is still running")
	}
}

func TestCleanStalePID_RemovesBothFiles(t *testing.T) {
	setTestHome(t)
	deployID := "test-clean-stale"

	if err := os.MkdirAll(DeployDir(), 0755); err != nil {
		t.Fatalf("creating deploy dir: %v", err)
	}

	// Create both PID and heartbeat files.
	if err := WritePID(deployID); err != nil {
		t.Fatalf("WritePID failed: %v", err)
	}
	if err := WriteHeartbeat(deployID); err != nil {
		t.Fatalf("WriteHeartbeat failed: %v", err)
	}

	// Verify both exist.
	if _, err := os.Stat(PIDPath(deployID)); err != nil {
		t.Fatalf("PID file should exist before cleanup: %v", err)
	}
	if _, err := os.Stat(HeartbeatPath(deployID)); err != nil {
		t.Fatalf("heartbeat file should exist before cleanup: %v", err)
	}

	CleanStalePID(deployID)

	if _, err := os.Stat(PIDPath(deployID)); !os.IsNotExist(err) {
		t.Error("PID file still exists after CleanStalePID")
	}
	if _, err := os.Stat(HeartbeatPath(deployID)); !os.IsNotExist(err) {
		t.Error("heartbeat file still exists after CleanStalePID")
	}
}

func TestCleanStalePID_NoopWhenMissing(t *testing.T) {
	setTestHome(t)
	// Should not panic when files don't exist.
	CleanStalePID("nonexistent-deploy")
}

func TestRemoveHeartbeat_RemovesFile(t *testing.T) {
	setTestHome(t)
	deployID := "test-remove-hb"

	if err := os.MkdirAll(DeployDir(), 0755); err != nil {
		t.Fatalf("creating deploy dir: %v", err)
	}

	if err := WriteHeartbeat(deployID); err != nil {
		t.Fatalf("WriteHeartbeat failed: %v", err)
	}

	RemoveHeartbeat(deployID)

	if _, err := os.Stat(HeartbeatPath(deployID)); !os.IsNotExist(err) {
		t.Error("heartbeat file still exists after RemoveHeartbeat")
	}
}

func TestRemoveHeartbeat_NoopWhenMissing(t *testing.T) {
	setTestHome(t)
	// Should not panic when file doesn't exist.
	RemoveHeartbeat("nonexistent-deploy")
}

func TestPIDPath_Format(t *testing.T) {
	setTestHome(t)
	path := PIDPath("my-deploy")
	if !strings.HasSuffix(path, "my-deploy.pid") {
		t.Errorf("PIDPath should end with deploy ID + .pid, got %s", path)
	}
	if !strings.Contains(path, ".agent-minder") {
		t.Errorf("PIDPath should be under .agent-minder, got %s", path)
	}
}

func TestHeartbeatPath_Format(t *testing.T) {
	setTestHome(t)
	path := HeartbeatPath("my-deploy")
	if !strings.HasSuffix(path, "my-deploy.heartbeat") {
		t.Errorf("HeartbeatPath should end with deploy ID + .heartbeat, got %s", path)
	}
}

func TestWritePID_OverwritesExisting(t *testing.T) {
	setTestHome(t)
	deployID := "test-overwrite"

	if err := os.MkdirAll(DeployDir(), 0755); err != nil {
		t.Fatalf("creating deploy dir: %v", err)
	}

	// Write a fake PID first.
	if err := os.WriteFile(PIDPath(deployID), []byte("99999"), 0644); err != nil {
		t.Fatalf("writing fake PID: %v", err)
	}

	// WritePID should overwrite.
	if err := WritePID(deployID); err != nil {
		t.Fatalf("WritePID failed: %v", err)
	}

	data, err := os.ReadFile(PIDPath(deployID))
	if err != nil {
		t.Fatalf("reading PID file: %v", err)
	}

	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid != os.Getpid() {
		t.Errorf("expected PID %d after overwrite, got %d", os.Getpid(), pid)
	}
}

func TestStartHeartbeat_WritesImmediately(t *testing.T) {
	setTestHome(t)
	deployID := "test-start-hb"

	if err := os.MkdirAll(DeployDir(), 0755); err != nil {
		t.Fatalf("creating deploy dir: %v", err)
	}

	stop := StartHeartbeat(deployID)
	defer stop()

	// StartHeartbeat writes in a goroutine; wait briefly for it.
	var hb time.Time
	var err error
	for i := 0; i < 50; i++ {
		hb, err = ReadHeartbeat(deployID)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("heartbeat should exist immediately after StartHeartbeat: %v", err)
	}

	if time.Since(hb) > 5*time.Second {
		t.Errorf("heartbeat too old: %v ago", time.Since(hb))
	}
}

func TestStartHeartbeat_StopBlocksUntilExit(t *testing.T) {
	setTestHome(t)
	deployID := "test-stop-blocks"

	if err := os.MkdirAll(DeployDir(), 0755); err != nil {
		t.Fatalf("creating deploy dir: %v", err)
	}

	stop := StartHeartbeat(deployID)

	// Stop should return (not deadlock).
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()

	select {
	case <-done:
		// OK — stop returned.
	case <-time.After(5 * time.Second):
		t.Fatal("StartHeartbeat stop function did not return within 5s")
	}
}
