package opencode

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestShutdownWhenIdleIsSafe covers the common teardown path: no job ever
// started a server, so Shutdown must be a no-op rather than panic on the nil
// proc. This is what the daemon/supervisor hook relies on for stateless runs.
func TestShutdownWhenIdleIsSafe(t *testing.T) {
	restore := swapProc(t, nil)
	defer restore()
	Shutdown() // must not panic with no running server
	Shutdown() // and remain safe when called again
}

// TestShutdownReapsRunningProcess proves Shutdown actually kills the shared
// server process and clears the manager, using a real killable process (sleep)
// in place of `opencode serve` so the test needs no opencode binary.
func TestShutdownReapsRunningProcess(t *testing.T) {
	restore := swapProc(t, nil)
	defer restore()

	cmd := exec.Command("sleep", "120")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	sharedManager.mu.Lock()
	sharedManager.proc = &serverProc{baseURL: "http://127.0.0.1:0", cmd: cmd}
	sharedManager.mu.Unlock()

	Shutdown()

	sharedManager.mu.Lock()
	proc := sharedManager.proc
	sharedManager.mu.Unlock()
	if proc != nil {
		t.Fatal("Shutdown did not clear sharedManager.proc")
	}

	// The process must be dead: signal 0 to a reaped pid returns an error.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
			break // reaped
		}
		if time.Now().After(deadline) {
			t.Fatal("process still alive after Shutdown")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// swapProc snapshots and clears the package-level shared server proc so a test
// can install its own, restoring the original on cleanup to avoid leaking state
// into sibling tests.
func swapProc(t *testing.T, p *serverProc) func() {
	t.Helper()
	sharedManager.mu.Lock()
	saved := sharedManager.proc
	sharedManager.proc = p
	sharedManager.mu.Unlock()
	return func() {
		sharedManager.mu.Lock()
		sharedManager.proc = saved
		sharedManager.mu.Unlock()
	}
}
