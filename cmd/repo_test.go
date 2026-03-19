package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dustinlange/agent-minder/internal/discovery"
	"github.com/dustinlange/agent-minder/internal/onboarding"
)

func TestCheckMark(t *testing.T) {
	if got := checkMark(true); got != "yes" {
		t.Errorf("checkMark(true) = %q, want %q", got, "yes")
	}
	if got := checkMark(false); got != "no" {
		t.Errorf("checkMark(false) = %q, want %q", got, "no")
	}
}

func TestDefaultAgentLogDir(t *testing.T) {
	dir := defaultAgentLogDir()
	if dir == "" {
		t.Skip("could not determine home directory")
	}
	if len(dir) < len(".agent-minder/agents") {
		t.Errorf("defaultAgentLogDir() = %q, too short", dir)
	}
}

func TestRepoEnrollCreatesOnboardingFile(t *testing.T) {
	dir := setupGitRepo(t)

	// Add a go.mod to make inventory interesting.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-create the onboarding file so enroll hits the "already exists" path
	// and doesn't try to launch the interactive agent.
	onboardingDir := filepath.Join(dir, ".agent-minder")
	if err := os.MkdirAll(onboardingDir, 0755); err != nil {
		t.Fatal(err)
	}
	onboardingFile := filepath.Join(onboardingDir, "onboarding.yaml")
	if err := os.WriteFile(onboardingFile, []byte("version: 1\nscanned_at: 2024-01-01T00:00:00Z\ninventory:\n  languages: [go]\nvalidation:\n  status: untested\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := repoEnrollCmd
	cmd.SetArgs([]string{dir})
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.RunE(cmd, []string{dir}); err != nil {
		t.Fatalf("runRepoEnroll: %v", err)
	}
}

func TestRepoEnrollNoClaudeCLI(t *testing.T) {
	dir := setupGitRepo(t)

	// Create a temporary bin dir with only git (not claude) to simulate missing claude CLI.
	fakeBin := t.TempDir()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("git not found")
	}
	if err := os.Symlink(gitPath, filepath.Join(fakeBin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)

	if err := runRepoEnroll(repoEnrollCmd, []string{dir}); err != nil {
		t.Fatalf("runRepoEnroll: %v", err)
	}

	// Should have created the initial onboarding file even without claude.
	onboardingPath := filepath.Join(dir, ".agent-minder", "onboarding.yaml")
	if _, err := os.Stat(onboardingPath); os.IsNotExist(err) {
		t.Error("expected onboarding file to be created")
	}
}

func TestRepoStatusRunsOnRealRepo(t *testing.T) {
	dir := setupGitRepo(t)

	cmd := repoStatusCmd
	cmd.SetArgs([]string{dir})

	if err := cmd.RunE(cmd, []string{dir}); err != nil {
		t.Fatalf("runRepoStatus: %v", err)
	}
}

func TestRepoRefreshNoEnrollmentFile(t *testing.T) {
	dir := setupGitRepo(t)

	cmd := repoRefreshCmd
	cmd.SetArgs([]string{dir})

	// Should not error, just report no enrollment file.
	if err := cmd.RunE(cmd, []string{dir}); err != nil {
		t.Fatalf("runRepoRefresh: %v", err)
	}
}

func TestBuildEnrollmentPrompt(t *testing.T) {
	info := &discovery.RepoInfo{
		Path: "/tmp/test-repo",
		Inventory: onboarding.Inventory{
			Languages:       []string{"go", "typescript"},
			PackageManagers: []string{"go-modules", "npm"},
			BuildFiles:      []string{"Makefile", "package.json"},
			CI:              []string{"github-actions"},
			Tooling: onboarding.Tooling{
				Secrets: "doppler",
			},
			ExistingClaude: onboarding.ExistingClaudeConfig{
				ClaudeMD: true,
			},
		},
	}

	prompt := buildEnrollmentPrompt(info, "/tmp/test-repo/.agent-minder/onboarding.yaml", []string{"Bash(make lint)"})

	// Check key content is present.
	for _, want := range []string{
		"go, typescript",
		"go-modules, npm",
		"doppler",
		"CLAUDE.md: yes",
		"settings.json: no",
		"/tmp/test-repo",
		"Bash(make lint)",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

// setupGitRepo creates a temporary directory with a git repo initialized.
func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}
