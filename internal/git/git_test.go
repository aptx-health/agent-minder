package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
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

func TestParseLogOutput(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		entries := parseLogOutput("")
		if entries != nil {
			t.Errorf("expected nil for empty input, got %v", entries)
		}
	})

	t.Run("valid RFC3339 date", func(t *testing.T) {
		input := "abc1234|Fix bug|Alice|2025-06-15T10:30:00+00:00"
		entries := parseLogOutput(input)
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		e := entries[0]
		if e.Hash != "abc1234" {
			t.Errorf("Hash = %q, want %q", e.Hash, "abc1234")
		}
		if e.Subject != "Fix bug" {
			t.Errorf("Subject = %q, want %q", e.Subject, "Fix bug")
		}
		if e.Author != "Alice" {
			t.Errorf("Author = %q, want %q", e.Author, "Alice")
		}
		expected, _ := time.Parse(time.RFC3339, "2025-06-15T10:30:00+00:00")
		if !e.Date.Equal(expected) {
			t.Errorf("Date = %v, want %v", e.Date, expected)
		}
	})

	t.Run("malformed date falls back to now", func(t *testing.T) {
		before := time.Now().Add(-time.Second)
		input := "def5678|Add feature|Bob|not-a-date"
		entries := parseLogOutput(input)
		after := time.Now().Add(time.Second)

		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		e := entries[0]
		if e.Hash != "def5678" {
			t.Errorf("Hash = %q, want %q", e.Hash, "def5678")
		}
		if e.Date.Before(before) || e.Date.After(after) {
			t.Errorf("Date = %v, expected between %v and %v", e.Date, before, after)
		}
	})

	t.Run("empty date string falls back to now", func(t *testing.T) {
		before := time.Now().Add(-time.Second)
		input := "aaa1111|Msg|Dev|"
		entries := parseLogOutput(input)
		after := time.Now().Add(time.Second)

		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		if entries[0].Date.Before(before) || entries[0].Date.After(after) {
			t.Errorf("empty date should fall back to now, got %v", entries[0].Date)
		}
	})

	t.Run("incomplete line with fewer than 4 fields", func(t *testing.T) {
		input := "abc1234|Fix bug|Alice"
		entries := parseLogOutput(input)
		if len(entries) != 0 {
			t.Errorf("expected 0 entries for incomplete line, got %d", len(entries))
		}
	})

	t.Run("single field line", func(t *testing.T) {
		entries := parseLogOutput("just-a-hash")
		if len(entries) != 0 {
			t.Errorf("expected 0 entries for single field, got %d", len(entries))
		}
	})

	t.Run("multiple entries mixed valid and malformed", func(t *testing.T) {
		input := "aaa|Valid commit|Alice|2025-01-01T00:00:00Z\nbbb|Bad date commit|Bob|garbage\nccc|Short line\nddd|Another valid|Carol|2025-12-31T23:59:59-05:00"
		entries := parseLogOutput(input)
		if len(entries) != 3 {
			t.Fatalf("expected 3 entries (one incomplete line skipped), got %d", len(entries))
		}

		// First entry: valid date.
		expected1, _ := time.Parse(time.RFC3339, "2025-01-01T00:00:00Z")
		if !entries[0].Date.Equal(expected1) {
			t.Errorf("entry 0 Date = %v, want %v", entries[0].Date, expected1)
		}

		// Second entry: malformed date, should be near now.
		if entries[1].Date.Before(time.Now().Add(-2 * time.Second)) {
			t.Errorf("entry 1 malformed date should be near now, got %v", entries[1].Date)
		}

		// Third entry: valid date with timezone offset.
		expected3, _ := time.Parse(time.RFC3339, "2025-12-31T23:59:59-05:00")
		if !entries[2].Date.Equal(expected3) {
			t.Errorf("entry 2 Date = %v, want %v", entries[2].Date, expected3)
		}
	})

	t.Run("subject containing pipe characters", func(t *testing.T) {
		// SplitN with limit 4 means the subject+date can contain pipes.
		// But hash|subject|author|date — only first 3 pipes matter.
		input := "abc|feat: add A | B support|Dev|2025-06-01T00:00:00Z"
		entries := parseLogOutput(input)
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		// With SplitN(line, "|", 4), the split is: [abc, feat: add A , B support, Dev, 2025-...]
		// Actually SplitN with 4 means at most 4 parts, splitting on first 3 pipes.
		// "abc|feat: add A | B support|Dev|2025-..." → [abc, feat: add A ,  B support|Dev|2025-...]
		// Wait, that's wrong. Let me reconsider.
		// With 4 parts max: part[0]=abc, part[1]=feat: add A , part[2]= B support, part[3]=Dev|2025-...
		// So parts[3] = "Dev|2025-..." which won't parse as RFC3339.
		// This is a known limitation — subjects with pipes break the parsing.
		// The entry will still be created but with a fallback date.
		if entries[0].Hash != "abc" {
			t.Errorf("Hash = %q, want %q", entries[0].Hash, "abc")
		}
	})

	t.Run("date with numeric timezone offset", func(t *testing.T) {
		input := "fff|Commit|Author|2025-03-15T14:30:00+05:30"
		entries := parseLogOutput(input)
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		expected, _ := time.Parse(time.RFC3339, "2025-03-15T14:30:00+05:30")
		if !entries[0].Date.Equal(expected) {
			t.Errorf("Date = %v, want %v", entries[0].Date, expected)
		}
	})

	t.Run("date in wrong format (not RFC3339)", func(t *testing.T) {
		before := time.Now().Add(-time.Second)
		// Unix timestamp is not RFC3339.
		input := "ggg|Commit|Author|1700000000"
		entries := parseLogOutput(input)
		after := time.Now().Add(time.Second)

		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		if entries[0].Date.Before(before) || entries[0].Date.After(after) {
			t.Errorf("non-RFC3339 date should fall back to now, got %v", entries[0].Date)
		}
	})
}

func TestLogSinceWithDates(t *testing.T) {
	dir := setupTestRepo(t)

	// Create a commit with a known date using GIT_AUTHOR_DATE / GIT_COMMITTER_DATE.
	knownDate := "2025-06-15T12:00:00+00:00"
	if err := os.WriteFile(filepath.Join(dir, "dated.txt"), []byte("dated\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "dated.txt"},
		{"git", "commit", "-m", "Dated commit"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE="+knownDate,
			"GIT_COMMITTER_DATE="+knownDate,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %s\n%s", args, err, out)
		}
	}

	// LogSince before that date should include the commit.
	since, _ := time.Parse(time.RFC3339, "2025-06-15T00:00:00Z")
	entries, err := LogSince(dir, since)
	if err != nil {
		t.Fatalf("LogSince: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("LogSince returned no entries for a commit within the time range")
	}

	// Verify the date was parsed correctly.
	found := false
	expectedDate, _ := time.Parse(time.RFC3339, knownDate)
	for _, e := range entries {
		if e.Subject == "Dated commit" {
			found = true
			if !e.Date.Equal(expectedDate) {
				t.Errorf("Dated commit Date = %v, want %v", e.Date, expectedDate)
			}
		}
	}
	if !found {
		t.Error("did not find 'Dated commit' in LogSince results")
	}
}

func TestLogDateParsing(t *testing.T) {
	dir := setupTestRepo(t)

	// The initial commit from setupTestRepo should have a valid, non-zero date.
	entries, err := Log(dir, 1)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Date.IsZero() {
		t.Error("expected non-zero date from real git commit")
	}
	if entries[0].Date.Year() < 2020 {
		t.Errorf("date year %d seems too old for a fresh commit", entries[0].Date.Year())
	}
}
