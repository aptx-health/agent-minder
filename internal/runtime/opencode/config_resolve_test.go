package opencode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

// TestResolvePicksOpenCode exercises the runtime-resolution hierarchy end-to-end
// against the REAL opencode registration (this package's init registers it), so
// `runtime.Resolve` can select opencode from an explicit flag, a repo config, and
// a user config. Complements the stub-based hierarchy test in the runtime package.
func TestResolvePicksOpenCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()

	// Explicit flag wins.
	got, err := runtime.Resolve(Name, repo)
	if err != nil {
		t.Fatalf("Resolve flag: %v", err)
	}
	if got.Name != Name || got.Source != "flag" {
		t.Fatalf("Resolve flag = %+v, want opencode/flag", got)
	}

	// User config default.
	writeConfig(t, filepath.Join(home, ".agent-minder"), "runtime: opencode\n")
	got, err = runtime.Resolve("", repo)
	if err != nil {
		t.Fatalf("Resolve user: %v", err)
	}
	if got.Name != Name || got.Source != "user" {
		t.Fatalf("Resolve user = %+v, want opencode/user", got)
	}

	// Repo config overrides user config.
	writeConfig(t, filepath.Join(repo, ".agent-minder"), "runtime: opencode\n")
	got, err = runtime.Resolve("", repo)
	if err != nil {
		t.Fatalf("Resolve repo: %v", err)
	}
	if got.Name != Name || got.Source != "repo" {
		t.Fatalf("Resolve repo = %+v, want opencode/repo", got)
	}
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, runtime.ConfigFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
