package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRuntimeHierarchy(t *testing.T) {
	resetRegistry(t)
	Register(NameCodex, func() AgentRuntime { return &stubRuntime{name: NameCodex} })

	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()

	if err := os.MkdirAll(filepath.Join(home, ".agent-minder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".agent-minder", ConfigFileName), []byte("runtime: codex\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve("", repo)
	if err != nil {
		t.Fatalf("Resolve user default: %v", err)
	}
	if got.Name != NameCodex || got.Source != "user" {
		t.Fatalf("Resolve user default = %+v, want codex/user", got)
	}

	if err := os.MkdirAll(filepath.Join(repo, ".agent-minder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".agent-minder", ConfigFileName), []byte("runtime: claude-code\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err = Resolve("", repo)
	if err != nil {
		t.Fatalf("Resolve repo default: %v", err)
	}
	if got.Name != NameClaudeCode || got.Source != "repo" {
		t.Fatalf("Resolve repo default = %+v, want claude-code/repo", got)
	}

	got, err = Resolve(NameCodex, repo)
	if err != nil {
		t.Fatalf("Resolve explicit: %v", err)
	}
	if got.Name != NameCodex || got.Source != "flag" {
		t.Fatalf("Resolve explicit = %+v, want codex/flag", got)
	}
}

func TestResolveRuntimeDefault(t *testing.T) {
	resetRegistry(t)
	t.Setenv("HOME", t.TempDir())

	got, err := Resolve("", t.TempDir())
	if err != nil {
		t.Fatalf("Resolve default: %v", err)
	}
	if got.Name != DefaultName || got.Source != "default" {
		t.Fatalf("Resolve default = %+v, want %s/default", got, DefaultName)
	}
}

func TestResolveRuntimeRejectsUnknownConfig(t *testing.T) {
	resetRegistry(t)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".agent-minder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".agent-minder", ConfigFileName), []byte("runtime: nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Resolve("", repo); err == nil {
		t.Fatal("Resolve accepted unknown runtime from repo config")
	}
}
