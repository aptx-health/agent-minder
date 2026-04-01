package lesson

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/aptx-health/agent-minder/internal/db"
)

func testStore(t *testing.T) *db.Store {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return db.NewStore(conn)
}

func TestSelectLessons(t *testing.T) {
	store := testStore(t)

	// Create a global lesson.
	_ = store.CreateLesson(&db.Lesson{Content: "Always run tests", Source: "manual", Active: true})

	// Create a repo-scoped lesson.
	_ = store.CreateLesson(&db.Lesson{
		RepoScope: sql.NullString{String: "acme/app", Valid: true},
		Content:   "Use v3 API",
		Source:    "review",
		Active:    true,
	})

	// Create a pinned lesson.
	_ = store.CreateLesson(&db.Lesson{Content: "Never skip linting", Source: "manual", Active: true, Pinned: true})

	// Create a lesson for a different repo.
	_ = store.CreateLesson(&db.Lesson{
		RepoScope: sql.NullString{String: "other/repo", Valid: true},
		Content:   "Other repo lesson",
		Source:    "manual",
		Active:    true,
	})

	lessons, err := SelectLessons(store, "acme", "app")
	if err != nil {
		t.Fatalf("SelectLessons: %v", err)
	}

	// Should get pinned + global + repo-scoped (3), not the other/repo lesson.
	if len(lessons) != 3 {
		t.Errorf("got %d lessons, want 3", len(lessons))
		for _, l := range lessons {
			t.Logf("  lesson: %s (pinned=%v scope=%v)", l.Content, l.Pinned, l.RepoScope)
		}
	}

	// Pinned should come first.
	if len(lessons) > 0 && !lessons[0].Pinned {
		t.Error("expected first lesson to be pinned")
	}
}

func TestFormatForPrompt(t *testing.T) {
	lessons := []*db.Lesson{
		{Content: "Always run tests", Source: "manual"},
		{Content: "Use v3 API", Source: "review"},
	}

	result := FormatForPrompt(lessons)
	if result == "" {
		t.Error("expected non-empty prompt")
	}
	if !contains(result, "Lessons from Previous Work") {
		t.Error("expected header in prompt")
	}
	if !contains(result, "Always run tests") {
		t.Error("expected first lesson in prompt")
	}
	if !contains(result, "(from review)") {
		t.Error("expected review source annotation")
	}
}

func TestFormatForPromptEmpty(t *testing.T) {
	result := FormatForPrompt(nil)
	if result != "" {
		t.Errorf("expected empty string for nil lessons, got %q", result)
	}
}

func TestIsDuplicate(t *testing.T) {
	existing := []*db.Lesson{
		{Content: "Always run go vet before committing code changes"},
	}

	// Similar enough to be a duplicate.
	if !isDuplicate("Run go vet before committing", existing) {
		t.Error("expected duplicate detection")
	}

	// Different enough to not be a duplicate.
	if isDuplicate("Use the v3 API client library", existing) {
		t.Error("expected no duplicate for unrelated lesson")
	}
}

func TestExtractPatterns(t *testing.T) {
	review := `Summary of review:
- Fix: Missing error handling in upload handler
- Issue: No test coverage for edge case
- The code looks mostly correct
- Always validate user input before processing`

	patterns := extractPatterns(review)
	if len(patterns) != 3 {
		t.Errorf("got %d patterns, want 3", len(patterns))
		for _, p := range patterns {
			t.Logf("  pattern: %s", p)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
