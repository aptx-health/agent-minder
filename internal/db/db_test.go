package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
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

func TestJobCRUD(t *testing.T) {
	s := testStore(t)

	d := &Deployment{
		ID: "deploy-jobs", RepoDir: "/tmp", Owner: "o", Repo: "r",
		Mode: "issues", MaxAgents: 3, MaxTurns: 50, MaxBudgetUSD: 5,
		AnalyzerModel: "sonnet", SkipLabel: "no-agent", TotalBudgetUSD: 25,
		BaseBranch: "main", ReviewEnabled: true,
	}
	_ = s.CreateDeployment(d)

	j1 := &Job{
		DeploymentID: "deploy-jobs",
		Agent:        "autopilot",
		Name:         "issue-42",
		IssueNumber:  42,
		IssueTitle:   sql.NullString{String: "Fix auth", Valid: true},
		Owner:        "o", Repo: "r", Status: StatusQueued,
	}
	if err := s.CreateJob(j1); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if j1.ID == 0 {
		t.Error("expected job ID to be set")
	}

	// Update status.
	if err := s.UpdateJobRunning(j1.ID); err != nil {
		t.Fatalf("UpdateJobRunning: %v", err)
	}

	got, err := s.GetJob(j1.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != StatusRunning {
		t.Errorf("got status %q, want %q", got.Status, StatusRunning)
	}
	if !got.StartedAt.Valid {
		t.Error("expected started_at to be set")
	}
	if got.Agent != "autopilot" {
		t.Errorf("got agent %q, want %q", got.Agent, "autopilot")
	}
	if got.Name != "issue-42" {
		t.Errorf("got name %q, want %q", got.Name, "issue-42")
	}
}

func TestQueuedUnblockedJobs(t *testing.T) {
	s := testStore(t)

	d := &Deployment{
		ID: "deploy-deps", RepoDir: "/tmp", Owner: "o", Repo: "r",
		Mode: "issues", MaxAgents: 3, MaxTurns: 50, MaxBudgetUSD: 5,
		AnalyzerModel: "sonnet", SkipLabel: "no-agent", TotalBudgetUSD: 25,
		BaseBranch: "main", ReviewEnabled: true,
	}
	_ = s.CreateDeployment(d)

	// Job 42: no deps → unblocked.
	_ = s.CreateJob(&Job{
		DeploymentID: "deploy-deps", Agent: "autopilot", Name: "issue-42",
		IssueNumber: 42, Owner: "o", Repo: "r", Status: StatusQueued,
	})

	// Job 43: depends on 42 → blocked.
	_ = s.CreateJob(&Job{
		DeploymentID: "deploy-deps", Agent: "autopilot", Name: "issue-43",
		IssueNumber: 43, Owner: "o", Repo: "r", Status: StatusQueued,
		Dependencies: sql.NullString{String: "[42]", Valid: true},
	})

	// Job 44: depends on 99 (not tracked) → unblocked.
	_ = s.CreateJob(&Job{
		DeploymentID: "deploy-deps", Agent: "autopilot", Name: "issue-44",
		IssueNumber: 44, Owner: "o", Repo: "r", Status: StatusQueued,
		Dependencies: sql.NullString{String: "[99]", Valid: true},
	})

	unblocked, err := s.QueuedUnblockedJobs("deploy-deps")
	if err != nil {
		t.Fatalf("QueuedUnblockedJobs: %v", err)
	}
	if len(unblocked) != 2 {
		t.Fatalf("got %d unblocked jobs, want 2", len(unblocked))
	}

	issues := map[int]bool{}
	for _, j := range unblocked {
		issues[j.IssueNumber] = true
	}
	if !issues[42] || !issues[44] {
		t.Errorf("expected issues 42 and 44 to be unblocked, got %v", issues)
	}

	// Complete job 42 → job 43 should become unblocked.
	jobs, _ := s.GetJobs("deploy-deps")
	for _, j := range jobs {
		if j.IssueNumber == 42 {
			_ = s.CompleteJob(j.ID, StatusDone)
		}
	}

	unblocked2, _ := s.QueuedUnblockedJobs("deploy-deps")
	if len(unblocked2) != 2 {
		t.Fatalf("after completing 42: got %d unblocked, want 2 (43 and 44)", len(unblocked2))
	}
}

func TestJobStageTracking(t *testing.T) {
	s := testStore(t)

	d := &Deployment{
		ID: "deploy-stages", RepoDir: "/tmp", Owner: "o", Repo: "r",
		Mode: "issues", MaxAgents: 3, MaxTurns: 50, MaxBudgetUSD: 5,
		AnalyzerModel: "sonnet", SkipLabel: "no-agent", TotalBudgetUSD: 25,
		BaseBranch: "main", ReviewEnabled: true,
	}
	_ = s.CreateDeployment(d)

	j := &Job{
		DeploymentID: "deploy-stages", Agent: "autopilot", Name: "issue-10",
		IssueNumber: 10, Owner: "o", Repo: "r", Status: StatusQueued,
	}
	_ = s.CreateJob(j)

	// Set stage.
	stagesJSON := `[{"name":"code","status":"running"},{"name":"review","status":"pending"}]`
	if err := s.UpdateJobStage(j.ID, "code", stagesJSON); err != nil {
		t.Fatalf("UpdateJobStage: %v", err)
	}

	got, _ := s.GetJob(j.ID)
	if !got.CurrentStage.Valid || got.CurrentStage.String != "code" {
		t.Errorf("got current_stage %v, want 'code'", got.CurrentStage)
	}
	if !got.StagesJSON.Valid || got.StagesJSON.String != stagesJSON {
		t.Errorf("stages_json mismatch")
	}

	// Set result.
	resultJSON := `{"risk":"low-risk","summary":"Clean"}`
	if err := s.UpdateJobResult(j.ID, resultJSON); err != nil {
		t.Fatalf("UpdateJobResult: %v", err)
	}

	got2, _ := s.GetJob(j.ID)
	if !got2.ResultJSON.Valid || got2.ResultJSON.String != resultJSON {
		t.Errorf("result_json mismatch")
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

func TestV1Migration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "migrate.db")

	// Create a v1 database manually.
	conn, err := sqlx.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open v1 db: %v", err)
	}

	v1Schema := `
CREATE TABLE schema_version (version INTEGER NOT NULL);
INSERT INTO schema_version VALUES (1);
CREATE TABLE deployments (
	id TEXT PRIMARY KEY, repo_dir TEXT NOT NULL, owner TEXT NOT NULL, repo TEXT NOT NULL,
	mode TEXT NOT NULL DEFAULT 'issues', watch_filter TEXT,
	max_agents INTEGER DEFAULT 3, max_turns INTEGER DEFAULT 50, max_budget_usd REAL DEFAULT 5.0,
	analyzer_model TEXT DEFAULT 'sonnet', skip_label TEXT DEFAULT 'no-agent',
	auto_merge INTEGER DEFAULT 0, review_enabled INTEGER DEFAULT 1,
	review_max_turns INTEGER, review_max_budget REAL,
	total_budget_usd REAL DEFAULT 25.0, carried_cost_usd REAL DEFAULT 0.0,
	base_branch TEXT DEFAULT 'main', started_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE tasks (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	deployment_id TEXT NOT NULL REFERENCES deployments(id),
	issue_number INTEGER NOT NULL, issue_title TEXT, issue_body TEXT,
	owner TEXT NOT NULL, repo TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'queued', dependencies TEXT,
	worktree_path TEXT, branch TEXT, pr_number INTEGER,
	cost_usd REAL DEFAULT 0.0, failure_reason TEXT, failure_detail TEXT,
	review_risk TEXT, review_comment_id INTEGER,
	max_turns_override INTEGER, max_budget_override REAL,
	agent_log TEXT, started_at DATETIME, completed_at DATETIME,
	UNIQUE(deployment_id, issue_number)
);
CREATE TABLE dep_graphs (
	deployment_id TEXT PRIMARY KEY REFERENCES deployments(id),
	graph_json TEXT NOT NULL, option_name TEXT, reasoning TEXT, confidence TEXT,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE lessons (
	id INTEGER PRIMARY KEY AUTOINCREMENT, repo_scope TEXT, content TEXT NOT NULL,
	source TEXT NOT NULL DEFAULT 'manual', active INTEGER DEFAULT 1, pinned INTEGER DEFAULT 0,
	times_injected INTEGER DEFAULT 0, times_helpful INTEGER DEFAULT 0, times_unhelpful INTEGER DEFAULT 0,
	superseded_by INTEGER REFERENCES lessons(id), last_injected_at DATETIME,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE task_lessons (
	task_id INTEGER NOT NULL REFERENCES tasks(id),
	lesson_id INTEGER NOT NULL REFERENCES lessons(id),
	PRIMARY KEY (task_id, lesson_id)
);
CREATE TABLE repo_onboarding (
	repo_dir TEXT PRIMARY KEY, owner TEXT NOT NULL, repo TEXT NOT NULL,
	yaml_content TEXT NOT NULL, validation_status TEXT DEFAULT 'untested',
	validation_failures TEXT, scanned_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO deployments (id, repo_dir, owner, repo) VALUES ('d1', '/tmp', 'o', 'r');
INSERT INTO tasks (deployment_id, issue_number, owner, repo, status) VALUES ('d1', 42, 'o', 'r', 'queued');
INSERT INTO tasks (deployment_id, issue_number, owner, repo, status) VALUES ('d1', 43, 'o', 'r', 'running');
`
	if _, err := conn.Exec(v1Schema); err != nil {
		t.Fatalf("create v1 schema: %v", err)
	}
	_ = conn.Close()

	// Reopen with our Open() — should trigger migration.
	conn2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (migration): %v", err)
	}
	defer func() { _ = conn2.Close() }()

	store := NewStore(conn2)

	// Verify version is now 2.
	var version int
	_ = conn2.Get(&version, "SELECT version FROM schema_version")
	if version != schemaVersion {
		t.Errorf("got schema version %d, want %d", version, schemaVersion)
	}

	// Verify jobs table has the data.
	jobs, err := store.GetJobs("d1")
	if err != nil {
		t.Fatalf("GetJobs after migration: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(jobs))
	}

	// Verify backfilled fields.
	for _, j := range jobs {
		if j.Agent != "autopilot" {
			t.Errorf("issue %d: got agent %q, want 'autopilot'", j.IssueNumber, j.Agent)
		}
		if j.Name == "" {
			t.Errorf("issue %d: name not backfilled", j.IssueNumber)
		}
		expectedName := "issue-" + fmt.Sprintf("%d", j.IssueNumber)
		if j.Name != expectedName {
			t.Errorf("issue %d: got name %q, want %q", j.IssueNumber, j.Name, expectedName)
		}
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
