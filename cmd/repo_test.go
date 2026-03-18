package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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

func TestRepoEnrollRunsOnRealRepo(t *testing.T) {
	dir := setupGitRepo(t)

	// Add a go.mod to make inventory interesting.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n\ngo 1.21\n"), 0644); err != nil {
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
