package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setupTestRepo creates a temporary git repo with some commits for testing.
func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	commands := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %s\n%s", args, err, out)
		}
	}

	// Create a file and commit.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "README.md"},
		{"git", "commit", "-m", "Initial commit"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %s\n%s", args, err, out)
		}
	}

	return dir
}

func TestIsRepo(t *testing.T) {
	dir := setupTestRepo(t)
	if !IsRepo(dir) {
		t.Error("expected IsRepo to return true for a git repo")
	}

	tmpDir := t.TempDir()
	if IsRepo(tmpDir) {
		t.Error("expected IsRepo to return false for a non-repo")
	}
}

func TestTopLevel(t *testing.T) {
	dir := setupTestRepo(t)
	top, err := TopLevel(dir)
	if err != nil {
		t.Fatalf("TopLevel: %v", err)
	}
	// Resolve symlinks for macOS /private/var/folders...
	expected, _ := filepath.EvalSymlinks(dir)
	got, _ := filepath.EvalSymlinks(top)
	if got != expected {
		t.Errorf("TopLevel = %q, want %q", got, expected)
	}
}

func TestRepoName(t *testing.T) {
	dir := setupTestRepo(t)
	name, err := RepoName(dir)
	if err != nil {
		t.Fatalf("RepoName: %v", err)
	}
	expected := filepath.Base(dir)
	if name != expected {
		t.Errorf("RepoName = %q, want %q", name, expected)
	}
}

func TestCurrentBranch(t *testing.T) {
	dir := setupTestRepo(t)
	branch, err := CurrentBranch(dir)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	// Could be "main" or "master" depending on git config.
	if branch == "" {
		t.Error("CurrentBranch returned empty string")
	}
}

func TestLog(t *testing.T) {
	dir := setupTestRepo(t)
	entries, err := Log(dir, 10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Log returned %d entries, want 1", len(entries))
	}
	if entries[0].Subject != "Initial commit" {
		t.Errorf("Subject = %q, want %q", entries[0].Subject, "Initial commit")
	}
}

func TestBranches(t *testing.T) {
	dir := setupTestRepo(t)
	branches, err := Branches(dir)
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if len(branches) == 0 {
		t.Fatal("Branches returned no entries")
	}
	foundCurrent := false
	for _, b := range branches {
		if b.IsCurrent {
			foundCurrent = true
		}
	}
	if !foundCurrent {
		t.Error("no branch marked as current")
	}
}

func TestWorktrees(t *testing.T) {
	dir := setupTestRepo(t)
	worktrees, err := Worktrees(dir)
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	if len(worktrees) != 1 {
		t.Fatalf("Worktrees returned %d entries, want 1", len(worktrees))
	}
	if !worktrees[0].IsMain {
		t.Error("first worktree should be marked as main")
	}
}

func TestWorktreeAddRemove(t *testing.T) {
	dir := setupTestRepo(t)

	// Add a worktree.
	wtPath := filepath.Join(t.TempDir(), "wt-test")
	if err := WorktreeAdd(dir, wtPath, "test-branch"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}

	// Verify worktree exists.
	worktrees, err := Worktrees(dir)
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	if len(worktrees) != 2 {
		t.Fatalf("expected 2 worktrees, got %d", len(worktrees))
	}

	// Find the new worktree.
	found := false
	for _, wt := range worktrees {
		if wt.Branch == "test-branch" {
			found = true
			break
		}
	}
	if !found {
		t.Error("new worktree with branch 'test-branch' not found")
	}

	// Remove worktree.
	if err := WorktreeRemove(dir, wtPath); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	// Verify worktree is gone.
	worktrees, err = Worktrees(dir)
	if err != nil {
		t.Fatalf("Worktrees after remove: %v", err)
	}
	if len(worktrees) != 1 {
		t.Fatalf("expected 1 worktree after remove, got %d", len(worktrees))
	}

	// Delete branch.
	if err := DeleteBranch(dir, "test-branch"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
}

func TestBranchExists(t *testing.T) {
	dir := setupTestRepo(t)

	// Current branch should exist.
	branch, _ := CurrentBranch(dir)
	if !BranchExists(dir, branch) {
		t.Errorf("BranchExists(%q) = false, want true", branch)
	}

	// Non-existent branch should not exist.
	if BranchExists(dir, "nonexistent-branch-xyz") {
		t.Error("BranchExists(nonexistent) = true, want false")
	}
}

func TestDefaultBranch(t *testing.T) {
	dir := setupTestRepo(t)

	// Without origin, should fall back to "main".
	branch, err := DefaultBranch(dir)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if branch != "main" {
		t.Errorf("DefaultBranch = %q, want 'main'", branch)
	}
}
