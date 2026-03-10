package discovery

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func setupTestRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %s\n%s", args, err, out)
		}
	}

	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# "+name+"\nA test repo.\n"), 0644)
	for _, args := range [][]string{
		{"git", "add", "."},
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

func TestScanRepo(t *testing.T) {
	dir := setupTestRepo(t, "myapp")

	info, err := ScanRepo(dir)
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}

	if info.Name != "myapp" {
		t.Errorf("Name = %q, want %q", info.Name, "myapp")
	}
	if info.Readme == "" {
		t.Error("Readme should not be empty")
	}
	if len(info.RecentLogs) != 1 {
		t.Errorf("RecentLogs len = %d, want 1", len(info.RecentLogs))
	}
	if len(info.Worktrees) < 1 {
		t.Error("should have at least 1 worktree (main)")
	}
}

func TestScanRepoNotARepo(t *testing.T) {
	dir := t.TempDir()
	_, err := ScanRepo(dir)
	if err == nil {
		t.Fatal("expected error for non-repo directory")
	}
	if _, ok := err.(*NotARepoError); !ok {
		t.Errorf("expected NotARepoError, got %T", err)
	}
}

func TestDeriveProjectName(t *testing.T) {
	tests := []struct {
		names []string
		want  string
	}{
		{[]string{"ripit-app", "ripit-infra"}, "ripit"},
		{[]string{"frontend", "backend"}, "frontend"},
		{[]string{"myapp"}, "myapp"},
		{nil, "project"},
	}

	for _, tt := range tests {
		var repos []*RepoInfo
		for _, n := range tt.names {
			repos = append(repos, &RepoInfo{Name: n, ShortName: n})
		}
		got := DeriveProjectName(repos)
		if got != tt.want {
			t.Errorf("DeriveProjectName(%v) = %q, want %q", tt.names, got, tt.want)
		}
	}
}

func TestSuggestTopics(t *testing.T) {
	repos := []*RepoInfo{
		{ShortName: "app"},
		{ShortName: "infra"},
	}
	topics := SuggestTopics("ripit", repos)
	expected := []string{"ripit/app", "ripit/infra", "ripit/coord"}

	if len(topics) != len(expected) {
		t.Fatalf("topics len = %d, want %d", len(topics), len(expected))
	}
	for i, topic := range topics {
		if topic != expected[i] {
			t.Errorf("topics[%d] = %q, want %q", i, topic, expected[i])
		}
	}
}
