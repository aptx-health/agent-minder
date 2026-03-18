package cmd

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dustinlange/agent-minder/internal/discovery"
	"github.com/dustinlange/agent-minder/internal/onboarding"
)

func TestPluralRepos(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{1, "this repo"},
		{2, "these 2 repos"},
		{5, "these 5 repos"},
	}
	for _, tt := range tests {
		got := pluralRepos(tt.n)
		if got != tt.want {
			t.Errorf("pluralRepos(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestPromptOnboarding_AllOnboarded(t *testing.T) {
	// Create a temp dir with an existing onboarding file.
	dir := t.TempDir()
	obDir := filepath.Join(dir, ".agent-minder")
	if err := os.MkdirAll(obDir, 0755); err != nil {
		t.Fatal(err)
	}
	obPath := filepath.Join(obDir, "onboarding.yaml")
	f := onboarding.New(onboarding.Inventory{Languages: []string{"go"}})
	if err := onboarding.Write(obPath, f); err != nil {
		t.Fatal(err)
	}

	repos := []*discovery.RepoInfo{{Path: dir, ShortName: "test-repo"}}

	// Should return immediately without reading from reader.
	reader := bufio.NewReader(strings.NewReader(""))
	promptOnboarding(reader, repos)
	// No panic or hang means success — the function detected all repos
	// are onboarded and returned early.
}

func TestPromptOnboarding_Declined(t *testing.T) {
	dir := t.TempDir()
	repos := []*discovery.RepoInfo{{Path: dir, ShortName: "test-repo"}}

	// Provide "n" to decline onboarding.
	reader := bufio.NewReader(strings.NewReader("n\n"))
	promptOnboarding(reader, repos)

	// Onboarding file should not exist since we declined.
	if onboarding.Exists(dir) {
		t.Error("onboarding file should not exist after declining")
	}
}

func TestBuildInventoryContext(t *testing.T) {
	ri := &discovery.RepoInfo{
		Path:      "/tmp/test-repo",
		ShortName: "test-repo",
		Inventory: onboarding.Inventory{
			Languages:       []string{"go", "python"},
			PackageManagers: []string{"go modules"},
			BuildFiles:      []string{"Makefile"},
			CI:              []string{"github-actions"},
			Tooling: onboarding.Tooling{
				Secrets:    "doppler",
				Containers: "docker-compose",
			},
			ExistingClaude: onboarding.ExistingClaudeConfig{
				SettingsJSON: true,
				ClaudeMD:     false,
			},
		},
	}

	ctx := buildInventoryContext(ri, "/tmp/test-repo/.agent-minder/onboarding.yaml")

	checks := []string{
		"Repo directory: /tmp/test-repo",
		"Onboarding file path: /tmp/test-repo/.agent-minder/onboarding.yaml",
		"Languages: go, python",
		"Package managers: go modules",
		"Build files: Makefile",
		"CI: github-actions",
		"Secrets: doppler",
		"Containers: docker-compose",
		"settings.json: true",
		"CLAUDE.md: false",
	}
	for _, check := range checks {
		if !strings.Contains(ctx, check) {
			t.Errorf("buildInventoryContext missing %q", check)
		}
	}

	// Should NOT contain fields that are empty.
	if strings.Contains(ctx, "Process manager:") {
		t.Error("should not include empty Process manager field")
	}
	if strings.Contains(ctx, "Env tools:") {
		t.Error("should not include empty Env tools field")
	}
}

func TestRunRepoOnboarding_CleansUpOnFailure(t *testing.T) {
	dir := t.TempDir()
	ri := &discovery.RepoInfo{
		Path:      dir,
		ShortName: "test-repo",
		Inventory: onboarding.Inventory{Languages: []string{"go"}},
	}

	// runRepoOnboarding will write the file, then fail because "claude"
	// binary doesn't exist in the test environment.
	err := runRepoOnboarding(ri)
	if err == nil {
		t.Fatal("expected error from missing claude binary")
	}

	// The onboarding file should have been cleaned up.
	if onboarding.Exists(dir) {
		t.Error("onboarding file should be removed after agent failure")
	}
}
