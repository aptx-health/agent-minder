package daemon

import (
	"context"
	"os"
	"testing"
	"time"
)

// shortHome returns a short-path temp directory suitable for use as HOME in
// Unix socket tests. sockaddr_un has a small fixed-size path buffer (104
// bytes on macOS); t.TempDir()'s test-name-embedded paths routinely exceed
// it once ".agent-minder/deploys/<id>.sock" is appended.
func shortHome(t *testing.T) string {
	t.Helper()
	base := "/tmp"
	if _, err := os.Stat(base); err != nil {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "amsock")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// waitForSocket polls until the socket file exists or the deadline passes.
func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s was not created in time", path)
}

func TestUnixSocket_RoundTripAndPermissions(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	srv, _ := testServer(t)
	deployID := "test-deploy"

	go func() { _ = srv.ListenAndServeUnix(deployID) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.ShutdownUnix(ctx)
	})

	path := SocketPath(deployID)
	waitForSocket(t, path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("socket permissions = %o, want 0600", perm)
	}

	// Round-trip via the Go client — no API key required over the socket.
	client := NewUnixClient(deployID)
	status, err := client.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus over unix socket: %v", err)
	}
	if status.DeployID != deployID {
		t.Errorf("DeployID = %q, want %q", status.DeployID, deployID)
	}
}

func TestUnixSocket_StaleSocketRecovery(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	srv, _ := testServer(t)
	deployID := "test-deploy"

	path := SocketPath(deployID)
	if err := os.MkdirAll(DeployDir(), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Simulate a stale socket left behind by a crashed process: no PID file
	// is present, so IsRunning(deployID) reports dead.
	if err := os.WriteFile(path, []byte{}, 0600); err != nil {
		t.Fatalf("write stale socket file: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServeUnix(deployID) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.ShutdownUnix(ctx)
	})

	// The stale file already exists at path, so waiting on its presence
	// alone would race with the server replacing it. Poll the client
	// instead until the recovered socket actually answers.
	client := NewUnixClient(deployID)
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, err := client.GetStatus(); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("GetStatus after stale socket recovery: %v", lastErr)
}

func TestUnixSocket_RefusesWhenOwnerAlive(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	srv, _ := testServer(t)
	deployID := "test-deploy"

	if err := WritePID(deployID); err != nil {
		t.Fatalf("WritePID: %v", err)
	}
	t.Cleanup(func() { RemovePID(deployID) })

	path := SocketPath(deployID)
	if err := os.WriteFile(path, []byte{}, 0600); err != nil {
		t.Fatalf("write existing socket file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	err := srv.ListenAndServeUnix(deployID)
	if err == nil {
		t.Fatal("expected error when socket owner appears alive, got nil")
	}
}

func TestUnixSocket_CleanShutdownRemovesFile(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	srv, _ := testServer(t)
	deployID := "test-deploy"

	go func() { _ = srv.ListenAndServeUnix(deployID) }()

	path := SocketPath(deployID)
	waitForSocket(t, path)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.ShutdownUnix(ctx); err != nil {
		t.Fatalf("ShutdownUnix: %v", err)
	}

	// Give the Serve goroutine a moment to unwind and remove the file.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket file %s still exists after shutdown", path)
}

func TestUnixSocket_NoAPIKeyRequired(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	srv, _ := testServer(t)
	// APIKey is set on this server (testAPIKey), but the socket path must
	// not require it — filesystem permissions are the auth boundary.
	deployID := "test-deploy"

	go func() { _ = srv.ListenAndServeUnix(deployID) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.ShutdownUnix(ctx)
	})

	waitForSocket(t, SocketPath(deployID))

	client := NewUnixClient(deployID)
	if _, err := client.GetJobs(); err != nil {
		t.Fatalf("GetJobs over unix socket without API key: %v", err)
	}
}
