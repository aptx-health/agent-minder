package reaper

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestParseEtime(t *testing.T) {
	cases := map[string]time.Duration{
		"05":         5 * time.Second,
		"01:30":      90 * time.Second,
		"02:00:00":   2 * time.Hour,
		"1-00:00:00": 24 * time.Hour,
		"3-04:05:06": 3*24*time.Hour + 4*time.Hour + 5*time.Minute + 6*time.Second,
	}
	for in, want := range cases {
		if got := parseEtime(in); got != want {
			t.Errorf("parseEtime(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestSweepKillsProcessInWorktree spawns a sleeping child whose cwd lives
// inside a temp worktree dir, then verifies Sweep finds and kills it.
func TestSweepKillsProcessInWorktree(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	worktree := t.TempDir()

	cmd := exec.Command("sleep", "30")
	cmd.Dir = worktree
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})

	// Give the child a moment to settle so lsof sees its cwd.
	time.Sleep(200 * time.Millisecond)

	reaped, err := Sweep(worktree)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(reaped) == 0 {
		t.Fatalf("expected to reap at least one process, got 0")
	}
	found := false
	for _, r := range reaped {
		if r.PID == pid {
			found = true
			if r.Signal == "" {
				t.Errorf("reaped pid %d missing signal", pid)
			}
		}
	}
	if !found {
		t.Errorf("expected pid %d in reaped list, got %+v", pid, reaped)
	}

	// Verify the process is no longer running (may be a zombie until parent
	// Waits — zombies are dead but still in the process table, which is fine).
	state, _ := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "state=").Output()
	st := string(state)
	if st != "" && st[0] != 'Z' && st[0] != '\n' {
		t.Errorf("pid %d state %q after Sweep; expected gone or zombie", pid, st)
	}
}

// TestSweepIgnoresProcessOutsideWorktree confirms we don't kill processes
// whose cwd is a sibling/parent of the worktree.
func TestSweepIgnoresProcessOutsideWorktree(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	parent := t.TempDir()
	worktree := filepath.Join(parent, "wt")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(parent, "other")
	if err := os.Mkdir(sibling, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sleep", "30")
	cmd.Dir = sibling
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})

	time.Sleep(200 * time.Millisecond)

	reaped, err := Sweep(worktree)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	for _, r := range reaped {
		if r.PID == pid {
			t.Errorf("Sweep killed sibling-dir pid %d (worktree=%s): %s", pid, worktree, r)
		}
	}
	// Sanity: sibling pid still alive.
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("sibling pid %d unexpectedly dead: %v", pid, err)
	}
}

// TestSweepRespectsHoldFile confirms a worktree with .minder-hold is spared.
func TestSweepRespectsHoldFile(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, HoldFileName), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sleep", "30")
	cmd.Dir = worktree
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})
	time.Sleep(200 * time.Millisecond)

	reaped, err := Sweep(worktree)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(reaped) != 0 {
		t.Errorf("expected hold file to spare worktree, got %d reaps", len(reaped))
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("held pid %d unexpectedly dead: %v", pid, err)
	}
}

// TestSweepEmptyPath returns no error and no reaps.
func TestSweepEmptyPath(t *testing.T) {
	r, err := Sweep("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r) != 0 {
		t.Errorf("expected empty result, got %d", len(r))
	}
}

// Smoke: snapshotMeta should populate command + ppid for a known live PID.
func TestSnapshotMetaSelf(t *testing.T) {
	pid := os.Getpid()
	if _, err := exec.Command("ps", "-p", strconv.Itoa(pid)).Output(); err != nil {
		t.Skipf("ps unavailable for process inspection: %v", err)
	}

	meta := snapshotMeta([]int{pid})
	m, ok := meta[pid]
	if !ok {
		t.Fatalf("no meta for self pid %d", pid)
	}
	if m.Command == "" {
		t.Errorf("empty command for self")
	}
	if m.PPID == 0 {
		t.Errorf("ppid=0 for self pid %d (parent=%d)", pid, os.Getppid())
	}
	_ = strconv.Itoa // keep import
}
