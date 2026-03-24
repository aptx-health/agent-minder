package deploy

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestHeartbeatRoundTrip(t *testing.T) {
	// Override HOME so Dir() points to a temp directory.
	tmpDir := t.TempDir()
	deploysDir := filepath.Join(tmpDir, ".agent-minder", "deploys")
	if err := os.MkdirAll(deploysDir, 0o755); err != nil {
		t.Fatalf("create deploys dir: %v", err)
	}
	t.Setenv("HOME", tmpDir)

	id := "test-heartbeat-roundtrip"

	// No heartbeat → zero time.
	if hb := ReadHeartbeat(id); !hb.IsZero() {
		t.Fatalf("expected zero time, got %v", hb)
	}

	// Write and read back.
	if err := WriteHeartbeat(id); err != nil {
		t.Fatalf("WriteHeartbeat: %v", err)
	}
	hb := ReadHeartbeat(id)
	if hb.IsZero() {
		t.Fatal("expected non-zero heartbeat after write")
	}
	if time.Since(hb) > 5*time.Second {
		t.Fatalf("heartbeat too old: %v", hb)
	}

	// Remove.
	RemoveHeartbeat(id)
	if hb := ReadHeartbeat(id); !hb.IsZero() {
		t.Fatalf("expected zero after remove, got %v", hb)
	}
}

func TestStartHeartbeatUpdates(t *testing.T) {
	tmpDir := t.TempDir()
	deploysDir := filepath.Join(tmpDir, ".agent-minder", "deploys")
	if err := os.MkdirAll(deploysDir, 0o755); err != nil {
		t.Fatalf("create deploys dir: %v", err)
	}
	t.Setenv("HOME", tmpDir)

	id := "test-heartbeat-ticker"
	stop := StartHeartbeat(id, 50*time.Millisecond)
	defer stop()

	// Give it time to write at least once.
	time.Sleep(100 * time.Millisecond)
	hb := ReadHeartbeat(id)
	if hb.IsZero() {
		t.Fatal("expected heartbeat to be written by ticker")
	}
}

func TestWasCrashShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	deploysDir := filepath.Join(tmpDir, ".agent-minder", "deploys")
	if err := os.MkdirAll(deploysDir, 0o755); err != nil {
		t.Fatalf("create deploys dir: %v", err)
	}
	t.Setenv("HOME", tmpDir)

	id := "test-crash-detect"

	// No PID file → not a crash.
	if WasCrashShutdown(id) {
		t.Fatal("should not detect crash without PID file")
	}

	// Write a PID file with a PID that definitely doesn't exist.
	fakePID := 1 << 30
	if err := os.WriteFile(PIDPath(id), []byte(strconv.Itoa(fakePID)), 0o644); err != nil {
		t.Fatalf("write fake PID: %v", err)
	}

	if !WasCrashShutdown(id) {
		t.Fatal("should detect crash when PID file exists but process is dead")
	}

	// CleanStalePID should remove both PID and heartbeat.
	if err := WriteHeartbeat(id); err != nil {
		t.Fatalf("WriteHeartbeat: %v", err)
	}
	CleanStalePID(id)

	if _, err := os.Stat(PIDPath(id)); !os.IsNotExist(err) {
		t.Fatal("PID file should be removed after CleanStalePID")
	}
	if _, err := os.Stat(HeartbeatPath(id)); !os.IsNotExist(err) {
		t.Fatal("heartbeat should be removed after CleanStalePID")
	}
}

func TestCleanOrphanedWorktrees(t *testing.T) {
	// Create a fake worktree directory structure.
	tmpDir := t.TempDir()
	worktreeBase := filepath.Join(tmpDir, ".agent-minder", "worktrees", "test-project")
	if err := os.MkdirAll(filepath.Join(worktreeBase, "issue-42"), 0o755); err != nil {
		t.Fatalf("create worktree dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(worktreeBase, "issue-55"), 0o755); err != nil {
		t.Fatalf("create worktree dir: %v", err)
	}

	// Override HOME so the function finds our temp worktree base.
	t.Setenv("HOME", tmpDir)

	// We don't have a real git repo, so git worktree remove will fail,
	// but the function falls back to os.RemoveAll.
	cleaned := cleanOrphanedWorktrees("test-project", tmpDir)
	if cleaned != 2 {
		t.Fatalf("expected 2 cleaned, got %d", cleaned)
	}

	// Verify directories are removed.
	entries, _ := os.ReadDir(worktreeBase)
	if len(entries) != 0 {
		t.Fatalf("expected empty dir after cleanup, got %d entries", len(entries))
	}
}
