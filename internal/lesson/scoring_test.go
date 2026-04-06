package lesson

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
)

func TestDecayScore_NoFeedback(t *testing.T) {
	l := &db.Lesson{
		TimesHelpful:   0,
		TimesUnhelpful: 0,
		CreatedAt:      time.Now(),
	}
	score := DecayScore(l, time.Now())
	if score != 0.0 {
		t.Errorf("expected 0.0 for no feedback, got %f", score)
	}
}

func TestDecayScore_RecentHelpful(t *testing.T) {
	now := time.Now().UTC()
	l := &db.Lesson{
		TimesHelpful:   5,
		TimesUnhelpful: 0,
		LastHelpfulAt:  sql.NullTime{Time: now.Add(-1 * time.Hour), Valid: true},
		CreatedAt:      now.Add(-7 * 24 * time.Hour),
	}
	score := DecayScore(l, now)
	if score <= 0 {
		t.Errorf("expected positive score for recent helpful lesson, got %f", score)
	}
	// Should be close to 5.0 since feedback was very recent (1 hour ago).
	if score < 4.5 {
		t.Errorf("expected score close to 5.0, got %f", score)
	}
}

func TestDecayScore_OldHelpful(t *testing.T) {
	now := time.Now().UTC()
	l := &db.Lesson{
		TimesHelpful:   5,
		TimesUnhelpful: 0,
		LastHelpfulAt:  sql.NullTime{Time: now.Add(-90 * 24 * time.Hour), Valid: true},
		CreatedAt:      now.Add(-100 * 24 * time.Hour),
	}
	score := DecayScore(l, now)
	if score <= 0 {
		t.Errorf("expected positive score for old helpful lesson, got %f", score)
	}
	// After 90 days (~3 half-lives), should be significantly reduced.
	if score > 1.0 {
		t.Errorf("expected decayed score < 1.0 after 90 days, got %f", score)
	}
}

func TestDecayScore_RecentUnhelpful(t *testing.T) {
	now := time.Now().UTC()
	l := &db.Lesson{
		TimesHelpful:    3,
		TimesUnhelpful:  3,
		LastHelpfulAt:   sql.NullTime{Time: now.Add(-60 * 24 * time.Hour), Valid: true},
		LastUnhelpfulAt: sql.NullTime{Time: now.Add(-1 * time.Hour), Valid: true},
		CreatedAt:       now.Add(-90 * 24 * time.Hour),
	}
	score := DecayScore(l, now)
	// Recent unhelpful should outweigh old helpful.
	if score >= 0 {
		t.Errorf("expected negative score when recent unhelpful outweighs old helpful, got %f", score)
	}
}

func TestDecayScore_Ordering(t *testing.T) {
	now := time.Now().UTC()

	// Lesson A: helpful recently.
	a := &db.Lesson{
		TimesHelpful:   3,
		TimesUnhelpful: 1,
		LastHelpfulAt:  sql.NullTime{Time: now.Add(-2 * 24 * time.Hour), Valid: true},
		CreatedAt:      now.Add(-30 * 24 * time.Hour),
	}

	// Lesson B: more total helpful but old.
	b := &db.Lesson{
		TimesHelpful:   10,
		TimesUnhelpful: 2,
		LastHelpfulAt:  sql.NullTime{Time: now.Add(-60 * 24 * time.Hour), Valid: true},
		CreatedAt:      now.Add(-90 * 24 * time.Hour),
	}

	scoreA := DecayScore(a, now)
	scoreB := DecayScore(b, now)

	// A should rank higher due to recency despite fewer total counts.
	if scoreA <= scoreB {
		t.Errorf("expected recent lesson A (%f) to score higher than old lesson B (%f)", scoreA, scoreB)
	}
}

func TestSelectLessons_DecayOrdering(t *testing.T) {
	store := scoringTestStore(t)
	now := time.Now().UTC()

	// Create two repo-scoped lessons with different recency.
	recentLesson := &db.Lesson{
		RepoScope: sql.NullString{String: "acme/app", Valid: true},
		Content:   "Recent helpful lesson",
		Source:    "review",
		Active:    true,
	}
	_ = store.CreateLesson(recentLesson)
	// Simulate 5 recent helpful feedbacks.
	for range 5 {
		_ = store.UpdateLessonFeedback(recentLesson.ID, true)
	}

	oldLesson := &db.Lesson{
		RepoScope: sql.NullString{String: "acme/app", Valid: true},
		Content:   "Old helpful lesson with many counts",
		Source:    "review",
		Active:    true,
	}
	_ = store.CreateLesson(oldLesson)
	// Simulate old feedback: 5 helpful but from 90 days ago.
	oldTime := now.Add(-90 * 24 * time.Hour)
	_, _ = store.DB().Exec(
		"UPDATE lessons SET times_helpful = 5, last_helpful_at = ? WHERE id = ?",
		oldTime, oldLesson.ID)

	lessons, err := SelectLessons(store, "acme", "app")
	if err != nil {
		t.Fatalf("SelectLessons: %v", err)
	}

	if len(lessons) != 2 {
		t.Fatalf("got %d lessons, want 2", len(lessons))
	}

	// Recent lesson should come first despite fewer total counts.
	if lessons[0].ID != recentLesson.ID {
		t.Errorf("expected recent lesson (ID %d) first, got ID %d", recentLesson.ID, lessons[0].ID)
	}
}

func TestFormatForPrompt_IncludesIDs(t *testing.T) {
	lessons := []*db.Lesson{
		{ID: 42, Content: "Always run tests", Source: "manual"},
		{ID: 99, Content: "Use v3 API", Source: "review"},
	}

	result := FormatForPrompt(lessons)
	if !contains(result, "[Lesson #42]") {
		t.Error("expected lesson ID in prompt")
	}
	if !contains(result, "[Lesson #99]") {
		t.Error("expected lesson ID in prompt")
	}
}

func TestFormatForReviewContext(t *testing.T) {
	lessons := []*db.Lesson{
		{ID: 10, Content: "Run tests first", RepoScope: sql.NullString{String: "acme/app", Valid: true}},
		{ID: 20, Content: "Global rule"},
	}

	result := FormatForReviewContext(lessons)
	if !contains(result, "Injected Lessons") {
		t.Error("expected header")
	}
	if !contains(result, "Lesson #10") {
		t.Error("expected lesson ID 10")
	}
	if !contains(result, "Lesson #20") {
		t.Error("expected lesson ID 20")
	}
	if !contains(result, "lesson_feedback") {
		t.Error("expected instruction to use lesson_feedback field")
	}
}

func TestFormatForReviewContext_Empty(t *testing.T) {
	result := FormatForReviewContext(nil)
	if result != "" {
		t.Errorf("expected empty string for nil lessons, got %q", result)
	}
}

func TestUpdateLessonFeedback(t *testing.T) {
	store := scoringTestStore(t)

	l := &db.Lesson{Content: "Test lesson", Source: "manual", Active: true}
	_ = store.CreateLesson(l)

	// Record helpful feedback.
	if err := store.UpdateLessonFeedback(l.ID, true); err != nil {
		t.Fatalf("UpdateLessonFeedback(helpful): %v", err)
	}

	updated, _ := store.GetLesson(l.ID)
	if updated.TimesHelpful != 1 {
		t.Errorf("expected TimesHelpful=1, got %d", updated.TimesHelpful)
	}
	if !updated.LastHelpfulAt.Valid {
		t.Error("expected LastHelpfulAt to be set")
	}

	// Record unhelpful feedback.
	if err := store.UpdateLessonFeedback(l.ID, false); err != nil {
		t.Fatalf("UpdateLessonFeedback(unhelpful): %v", err)
	}

	updated, _ = store.GetLesson(l.ID)
	if updated.TimesUnhelpful != 1 {
		t.Errorf("expected TimesUnhelpful=1, got %d", updated.TimesUnhelpful)
	}
	if !updated.LastUnhelpfulAt.Valid {
		t.Error("expected LastUnhelpfulAt to be set")
	}
}

func TestGetJobLessons(t *testing.T) {
	store := scoringTestStore(t)

	// Create deployment + job.
	deploy := &db.Deployment{
		ID: "test-deploy", RepoDir: "/tmp", Owner: "acme", Repo: "app",
		Mode: "issues", MaxAgents: 1, MaxTurns: 10, MaxBudgetUSD: 5,
		AnalyzerModel: "sonnet", BaseBranch: "main", TotalBudgetUSD: 25,
	}
	_ = store.CreateDeployment(deploy)
	job := &db.Job{
		DeploymentID: "test-deploy", Agent: "autopilot", Name: "issue-1",
		Owner: "acme", Repo: "app", Status: "running",
	}
	_ = store.CreateJob(job)

	// Create lessons and link them.
	l1 := &db.Lesson{Content: "Lesson 1", Source: "manual", Active: true}
	l2 := &db.Lesson{Content: "Lesson 2", Source: "review", Active: true}
	_ = store.CreateLesson(l1)
	_ = store.CreateLesson(l2)
	_ = store.RecordJobLessons(job.ID, []int64{l1.ID, l2.ID})

	lessons, err := store.GetJobLessons(job.ID)
	if err != nil {
		t.Fatalf("GetJobLessons: %v", err)
	}
	if len(lessons) != 2 {
		t.Errorf("got %d lessons, want 2", len(lessons))
	}
}

func TestDeleteLesson_CascadesJobLessons(t *testing.T) {
	store := scoringTestStore(t)

	// Create deployment + job.
	deploy := &db.Deployment{
		ID: "test-deploy", RepoDir: "/tmp", Owner: "acme", Repo: "app",
		Mode: "issues", MaxAgents: 1, MaxTurns: 10, MaxBudgetUSD: 5,
		AnalyzerModel: "sonnet", BaseBranch: "main", TotalBudgetUSD: 25,
	}
	_ = store.CreateDeployment(deploy)
	job := &db.Job{
		DeploymentID: "test-deploy", Agent: "autopilot", Name: "issue-1",
		Owner: "acme", Repo: "app", Status: "running",
	}
	_ = store.CreateJob(job)

	// Create a lesson and link it to the job.
	l := &db.Lesson{Content: "Lesson to delete", Source: "manual", Active: true}
	_ = store.CreateLesson(l)
	_ = store.RecordJobLessons(job.ID, []int64{l.ID})

	// Verify the link exists.
	linked, _ := store.GetJobLessons(job.ID)
	if len(linked) != 1 {
		t.Fatalf("expected 1 linked lesson, got %d", len(linked))
	}

	// Delete the lesson — should succeed despite FK reference.
	if err := store.DeleteLesson(l.ID); err != nil {
		t.Fatalf("DeleteLesson failed: %v", err)
	}

	// Verify lesson is gone.
	_, err := store.GetLesson(l.ID)
	if err == nil {
		t.Error("expected error getting deleted lesson")
	}

	// Verify job_lessons reference is also cleaned up.
	linked, _ = store.GetJobLessons(job.ID)
	if len(linked) != 0 {
		t.Errorf("expected 0 linked lessons after delete, got %d", len(linked))
	}
}

func TestMigrationV4toV5(t *testing.T) {
	// Verify that a fresh database has the new columns.
	store := scoringTestStore(t)

	l := &db.Lesson{Content: "Test", Source: "manual", Active: true}
	_ = store.CreateLesson(l)

	// Verify we can update the new columns.
	if err := store.UpdateLessonFeedback(l.ID, true); err != nil {
		t.Fatalf("can't use last_helpful_at column: %v", err)
	}
	if err := store.UpdateLessonFeedback(l.ID, false); err != nil {
		t.Fatalf("can't use last_unhelpful_at column: %v", err)
	}

	updated, _ := store.GetLesson(l.ID)
	if !updated.LastHelpfulAt.Valid || !updated.LastUnhelpfulAt.Valid {
		t.Error("expected both timestamp columns to be set")
	}
}

func scoringTestStore(t *testing.T) *db.Store {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return db.NewStore(conn)
}
