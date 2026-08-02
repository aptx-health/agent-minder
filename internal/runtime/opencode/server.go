package opencode

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// defaultBin is the opencode binary name resolved on PATH.
const defaultBin = "opencode"

// serverProc is a running `opencode serve` process and its base URL.
type serverProc struct {
	baseURL string
	cmd     *exec.Cmd
}

// serverManager owns a single shared `opencode serve` per process. opencode's
// HTTP API is multi-directory (every session/prompt carries a `directory`
// query param), so one server serves every job's worktree — this realizes the
// "shared server per deployment" decision from design/opencode-mapping.md.
//
// Runtime instances are constructed fresh per registry Lookup, so the manager
// must live at package scope to be shared across jobs.
type serverManager struct {
	mu   sync.Mutex
	proc *serverProc
}

var sharedManager = &serverManager{}

// ensure returns the base URL of a healthy shared server, lazily starting one
// on first use. bin is the opencode binary; env is merged over the process
// environment for the serve process (provider API keys, GITHUB_TOKEN). Because
// the server is shared, env is applied only when the server is first started;
// callers should treat provider credentials as deployment-level.
func (m *serverManager) ensure(ctx context.Context, bin string, env map[string]string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.proc != nil && m.proc.alive() {
		return m.proc.baseURL, nil
	}

	port, err := freePort()
	if err != nil {
		return "", fmt.Errorf("opencode: allocate port: %w", err)
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Detached from ctx: the shared server outlives any single Run.
	cmd := exec.Command(bin, "serve", "--hostname", "127.0.0.1", "--port", fmt.Sprintf("%d", port))
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// Inject the headless-autonomous permission policy so tool actions don't
	// stall on approval (no interactive approver exists in minder's model). Only
	// applied when the operator hasn't already set OPENCODE_PERMISSION.
	cmd.Env = withHeadlessPermission(cmd.Env)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("opencode: start serve: %w", err)
	}

	if err := waitHealthy(ctx, baseURL, 30*time.Second); err != nil {
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("opencode: server did not become healthy: %w", err)
	}

	m.proc = &serverProc{baseURL: baseURL, cmd: cmd}
	return baseURL, nil
}

// shutdown stops the shared server if running. Intended for process teardown.
func (m *serverManager) shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.proc != nil && m.proc.cmd != nil && m.proc.cmd.Process != nil {
		_ = m.proc.cmd.Process.Kill()
		_, _ = m.proc.cmd.Process.Wait()
	}
	m.proc = nil
}

// Shutdown stops the package-level shared opencode server. Safe to call when no
// server is running. Callers (e.g. the daemon on stop) should invoke this.
func Shutdown() { sharedManager.shutdown() }

func (p *serverProc) alive() bool {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return false
	}
	// A finished process has ProcessState set once Wait has run; we don't Wait
	// on the detached server, so probe health instead.
	return probe(p.baseURL)
}

// freePort asks the OS for an ephemeral port, then releases it for opencode to
// bind. The brief window between close and re-bind is an accepted small race.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitHealthy polls GET /doc until the server responds or the deadline passes.
func waitHealthy(ctx context.Context, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if probe(baseURL) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func probe(baseURL string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodGet, baseURL+"/doc", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
