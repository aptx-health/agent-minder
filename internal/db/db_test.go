package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	conn, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return NewStore(conn)
}

func TestSchemaCreation(t *testing.T) {
	s := testStore(t)

	// Verify schema version.
	var version int
	err := s.DB().Get(&version, "SELECT version FROM schema_version")
	if err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if version != schemaVersion {
		t.Errorf("got version %d, want %d", version, schemaVersion)
	}
}

func TestDeploymentCRUD(t *testing.T) {
	s := testStore(t)

	d := &Deployment{
		ID:             "test-deploy-1",
		RepoDir:        "/tmp/repo",
		Owner:          "aptx-health",
		Repo:           "agent-minder",
		Mode:           "issues",
		MaxAgents:      3,
		MaxTurns:       50,
		MaxBudgetUSD:   5.0,
		AnalyzerModel:  "sonnet",
		SkipLabel:      "no-agent",
		TotalBudgetUSD: 25.0,
		BaseBranch:     "main",
		ReviewEnabled:  true,
	}

	if err := s.CreateDeployment(d); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}

	got, err := s.GetDeployment("test-deploy-1")
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if got.Owner != "aptx-health" || got.Repo != "agent-minder" {
		t.Errorf("got %s/%s, want aptx-health/agent-minder", got.Owner, got.Repo)
	}

	ds, err := s.ListDeployments()
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	if len(ds) != 1 {
		t.Errorf("got %d deployments, want 1", len(ds))
	}
}

func TestTaskCRUD(t *testing.T) {
	s := testStore(t)

	d := &Deployment{
		ID: "deploy-tasks", RepoDir: "/tmp", Owner: "o", Repo: "r",
		Mode: "issues", MaxAgents: 3, MaxTurns: 50, MaxBudgetUSD: 5,
		AnalyzerModel: "sonnet", SkipLabel: "no-agent", TotalBudgetUSD: 25,
		BaseBranch: "main", ReviewEnabled: true,
	}
	_ = s.CreateDeployment(d)

	t1 := &Task{
		DeploymentID: "deploy-tasks", IssueNumber: 42,
		IssueTitle: sql.NullString{String: "Fix auth", Valid: true},
		Owner:      "o", Repo: "r", Status: StatusQueued,
	}
	if err := s.CreateTask(t1); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if t1.ID == 0 {
		t.Error("expected task ID to be set")
	}

	// Update status.
	if err := s.UpdateTaskRunning(t1.ID); err != nil {
		t.Fatalf("UpdateTaskRunning: %v", err)
	}

	got, err := s.GetTask(t1.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != StatusRunning {
		t.Errorf("got status %q, want %q", got.Status, StatusRunning)
	}
	if !got.StartedAt.Valid {
		t.Error("expected started_at to be set")
	}
}

func TestQueuedUnblockedTasks(t *testing.T) {
	s := testStore(t)

	d := &Deployment{
		ID: "deploy-deps", RepoDir: "/tmp", Owner: "o", Repo: "r",
		Mode: "issues", MaxAgents: 3, MaxTurns: 50, MaxBudgetUSD: 5,
		AnalyzerModel: "sonnet", SkipLabel: "no-agent", TotalBudgetUSD: 25,
		BaseBranch: "main", ReviewEnabled: true,
	}
	_ = s.CreateDeployment(d)

	// Task 42: no deps → unblocked.
	_ = s.CreateTask(&Task{
		DeploymentID: "deploy-deps", IssueNumber: 42,
		Owner: "o", Repo: "r", Status: StatusQueued,
	})

	// Task 43: depends on 42 → blocked.
	_ = s.CreateTask(&Task{
		DeploymentID: "deploy-deps", IssueNumber: 43,
		Owner: "o", Repo: "r", Status: StatusQueued,
		Dependencies: sql.NullString{String: "[42]", Valid: true},
	})

	// Task 44: depends on 99 (not tracked) → unblocked.
	_ = s.CreateTask(&Task{
		DeploymentID: "deploy-deps", IssueNumber: 44,
		Owner: "o", Repo: "r", Status: StatusQueued,
		Dependencies: sql.NullString{String: "[99]", Valid: true},
	})

	unblocked, err := s.QueuedUnblockedTasks("deploy-deps")
	if err != nil {
		t.Fatalf("QueuedUnblockedTasks: %v", err)
	}
	if len(unblocked) != 2 {
		t.Fatalf("got %d unblocked tasks, want 2", len(unblocked))
	}

	issues := map[int]bool{}
	for _, task := range unblocked {
		issues[task.IssueNumber] = true
	}
	if !issues[42] || !issues[44] {
		t.Errorf("expected issues 42 and 44 to be unblocked, got %v", issues)
	}

	// Complete task 42 → task 43 should become unblocked.
	tasks, _ := s.GetTasks("deploy-deps")
	for _, task := range tasks {
		if task.IssueNumber == 42 {
			_ = s.CompleteTask(task.ID, StatusDone)
		}
	}

	unblocked2, _ := s.QueuedUnblockedTasks("deploy-deps")
	if len(unblocked2) != 2 {
		t.Fatalf("after completing 42: got %d unblocked, want 2 (43 and 44)", len(unblocked2))
	}
}

func TestLessonCRUD(t *testing.T) {
	s := testStore(t)

	l := &Lesson{
		Content: "Always run go vet before committing",
		Source:  "manual",
		Active:  true,
	}
	if err := s.CreateLesson(l); err != nil {
		t.Fatalf("CreateLesson: %v", err)
	}
	if l.ID == 0 {
		t.Error("expected lesson ID to be set")
	}

	// Create a repo-scoped lesson.
	l2 := &Lesson{
		RepoScope: sql.NullString{String: "aptx-health/agent-minder", Valid: true},
		Content:   "Use v3 API not v2",
		Source:    "review",
		Active:    true,
		Pinned:    true,
	}
	_ = s.CreateLesson(l2)

	// Get active lessons for repo scope.
	lessons, err := s.GetActiveLessons("aptx-health/agent-minder")
	if err != nil {
		t.Fatalf("GetActiveLessons: %v", err)
	}
	if len(lessons) != 2 {
		t.Errorf("got %d lessons, want 2 (global + repo-scoped)", len(lessons))
	}

	// Pinned should come first.
	if len(lessons) >= 1 && !lessons[0].Pinned {
		t.Error("expected pinned lesson to come first")
	}

	// Get active lessons without repo scope → only global.
	global, err := s.GetActiveLessons("")
	if err != nil {
		t.Fatalf("GetActiveLessons (global): %v", err)
	}
	if len(global) < 1 {
		t.Errorf("expected at least 1 active lesson with empty scope, got %d", len(global))
	}

	// Deactivate.
	_ = s.UpdateLessonActive(l.ID, false)
	active, _ := s.GetActiveLessons("")
	for _, al := range active {
		if al.ID == l.ID {
			t.Error("deactivated lesson should not appear in active list")
		}
	}

	// Effectiveness tracking.
	_ = s.IncrementLessonInjected([]int64{l2.ID})
	got, _ := s.GetLesson(l2.ID)
	if got.TimesInjected != 1 {
		t.Errorf("got times_injected %d, want 1", got.TimesInjected)
	}
}

func TestDefaultDBPath(t *testing.T) {
	path := DefaultDBPath()
	if path == "" {
		t.Error("DefaultDBPath returned empty string")
	}
	// Should end with v2.db.
	if filepath.Base(path) != "v2.db" {
		t.Errorf("expected v2.db, got %s", filepath.Base(path))
	}
}

func TestStaleAndIneffectiveLessons(t *testing.T) {
	s := testStore(t)

	// Create a lesson that's never been injected.
	l := &Lesson{Content: "stale lesson", Source: "manual", Active: true}
	_ = s.CreateLesson(l)

	stale, err := s.StaleLessons(0) // 0 duration means everything is stale.
	if err != nil {
		t.Fatalf("StaleLessons: %v", err)
	}
	if len(stale) != 1 {
		t.Errorf("got %d stale lessons, want 1", len(stale))
	}

	// Create an ineffective lesson.
	l2 := &Lesson{Content: "bad advice", Source: "review", Active: true}
	_ = s.CreateLesson(l2)
	// Manually set injection stats.
	_, _ = s.db.Exec("UPDATE lessons SET times_injected = 10, times_helpful = 2, times_unhelpful = 8 WHERE id = ?", l2.ID)

	ineffective, err := s.IneffectiveLessons(5)
	if err != nil {
		t.Fatalf("IneffectiveLessons: %v", err)
	}
	if len(ineffective) != 1 {
		t.Errorf("got %d ineffective lessons, want 1", len(ineffective))
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
