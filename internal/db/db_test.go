package db

import (
	"database/sql"
	"errors"
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
	if got.Runtime != "claude-code" {
		t.Errorf("got runtime %q, want claude-code default", got.Runtime)
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
		Runtime:      sql.NullString{String: "codex", Valid: true},
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
	if !got.Runtime.Valid || got.Runtime.String != "codex" {
		t.Errorf("got runtime %v, want codex", got.Runtime)
	}
}

// TestGetJobForDeployment_Scoping guards the one scoped read every job/log
// lookup (legacy daemon routes, coordinator.Job) must go through: a job that
// belongs to another deployment is indistinguishable from an unknown ID.
func TestGetJobForDeployment_Scoping(t *testing.T) {
	s := testStore(t)

	for _, id := range []string{"deploy-a", "deploy-b"} {
		d := &Deployment{ID: id, RepoDir: "/tmp", Owner: "o", Repo: "r", Mode: "issues"}
		if err := s.CreateDeployment(d); err != nil {
			t.Fatalf("CreateDeployment(%s): %v", id, err)
		}
	}

	jobA := &Job{DeploymentID: "deploy-a", Agent: "autopilot", Name: "issue-1", Owner: "o", Repo: "r", Status: StatusQueued}
	if err := s.CreateJob(jobA); err != nil {
		t.Fatalf("CreateJob(a): %v", err)
	}

	got, err := s.GetJobForDeployment("deploy-a", jobA.ID)
	if err != nil {
		t.Fatalf("GetJobForDeployment(own): %v", err)
	}
	if got.ID != jobA.ID {
		t.Errorf("got job %d, want %d", got.ID, jobA.ID)
	}

	if _, err := s.GetJobForDeployment("deploy-b", jobA.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetJobForDeployment(foreign deployment) error = %v, want sql.ErrNoRows", err)
	}

	if _, err := s.GetJobForDeployment("deploy-a", jobA.ID+1000); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetJobForDeployment(unknown id) error = %v, want sql.ErrNoRows", err)
	}
}

// TestBulkCreateJobs_ColumnParity guards against BulkCreateJobs and CreateJob
// drifting apart on which columns they persist (issue #602): both must write
// max_turns/max_budget_usd and the source_type/source_name/source_ref
// provenance columns identically.
func TestBulkCreateJobs_ColumnParity(t *testing.T) {
	s := testStore(t)

	d := &Deployment{
		ID: "deploy-bulk", RepoDir: "/tmp", Owner: "o", Repo: "r",
		Mode: "issues", MaxAgents: 3, MaxTurns: 50, MaxBudgetUSD: 5,
		AnalyzerModel: "sonnet", SkipLabel: "no-agent", TotalBudgetUSD: 25,
		BaseBranch: "main", ReviewEnabled: true,
	}
	if err := s.CreateDeployment(d); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}

	j := &Job{
		DeploymentID: "deploy-bulk",
		Agent:        "autopilot",
		Name:         "issue-77",
		IssueNumber:  77,
		Owner:        "o", Repo: "r", Status: StatusQueued,
		MaxTurns:    sql.NullInt64{Int64: 9, Valid: true},
		MaxBudgetOv: sql.NullFloat64{Float64: 1.5, Valid: true},
		SourceType:  sql.NullString{String: "explicit", Valid: true},
		SourceName:  sql.NullString{String: "issue-77", Valid: true},
		SourceRef:   sql.NullString{String: "cli-deploy", Valid: true},
	}
	if err := s.BulkCreateJobs([]*Job{j}); err != nil {
		t.Fatalf("BulkCreateJobs: %v", err)
	}

	jobs, err := s.GetJobs("deploy-bulk")
	if err != nil {
		t.Fatalf("GetJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	got := jobs[0]
	if !got.MaxTurns.Valid || got.MaxTurns.Int64 != 9 {
		t.Errorf("max_turns = %v, want 9", got.MaxTurns)
	}
	if !got.MaxBudgetOv.Valid || got.MaxBudgetOv.Float64 != 1.5 {
		t.Errorf("max_budget_usd = %v, want 1.5", got.MaxBudgetOv)
	}
	if !got.SourceType.Valid || got.SourceType.String != "explicit" {
		t.Errorf("source_type = %v, want explicit", got.SourceType)
	}
	if !got.SourceName.Valid || got.SourceName.String != "issue-77" {
		t.Errorf("source_name = %v, want issue-77", got.SourceName)
	}
	if !got.SourceRef.Valid || got.SourceRef.String != "cli-deploy" {
		t.Errorf("source_ref = %v, want cli-deploy", got.SourceRef)
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

	// Job 45: terminal blocked failure → not requeued.
	blockedFailure := &Job{
		DeploymentID: "deploy-deps", Agent: "autopilot", Name: "issue-45",
		IssueNumber: 45, Owner: "o", Repo: "r", Status: StatusQueued,
	}
	_ = s.CreateJob(blockedFailure)
	_ = s.UpdateJobBlockedFailure(blockedFailure.ID, "setup_hook", "setup failed")

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

	// Get active lessons for repo scope — only repo-scoped, not global.
	lessons, err := s.GetActiveLessons("aptx-health/agent-minder")
	if err != nil {
		t.Fatalf("GetActiveLessons: %v", err)
	}
	if len(lessons) != 1 {
		t.Errorf("got %d lessons, want 1 (repo-scoped only, no global)", len(lessons))
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

func TestOpenCreatesMissingParentDir(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nested", "does-not-exist-yet", "v2.db")

	conn, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("expected database file to be created: %v", err)
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
	deploy, err := store.GetDeployment("d1")
	if err != nil {
		t.Fatalf("GetDeployment after migration: %v", err)
	}
	if deploy.Runtime != "claude-code" {
		t.Errorf("migrated runtime = %q, want claude-code", deploy.Runtime)
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
		if j.Runtime.Valid {
			t.Errorf("issue %d: runtime = %v, want null fallback", j.IssueNumber, j.Runtime)
		}
	}
}

func TestV8toV9ScheduleMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "migrate89.db")

	// Build a v8 database with the old name-only PK on job_schedules and a row
	// carrying last-run history.
	conn, err := sqlx.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open v8 db: %v", err)
	}
	v8Schedules := `
CREATE TABLE schema_version (version INTEGER NOT NULL);
INSERT INTO schema_version VALUES (8);
CREATE TABLE deployments (
	id TEXT PRIMARY KEY,
	repo_dir TEXT NOT NULL,
	owner TEXT NOT NULL,
	repo TEXT NOT NULL
);
CREATE TABLE jobs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	deployment_id TEXT NOT NULL,
	agent TEXT NOT NULL DEFAULT 'autopilot',
	name TEXT NOT NULL,
	owner TEXT NOT NULL DEFAULT '',
	repo TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'queued'
);
CREATE TABLE job_schedules (
	name TEXT PRIMARY KEY,
	deployment_id TEXT NOT NULL,
	cron_expr TEXT,
	trigger_expr TEXT,
	agent TEXT NOT NULL,
	runtime TEXT,
	model TEXT,
	description TEXT,
	budget REAL,
	max_turns INTEGER,
	enabled INTEGER DEFAULT 1,
	last_run_at DATETIME,
	next_run_at DATETIME,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO job_schedules (name, deployment_id, cron_expr, agent, enabled, last_run_at)
VALUES ('nightly', 'd1', '0 2 * * *', 'maintainer', 1, '2026-07-31 02:00:00');
`
	if _, err := conn.Exec(v8Schedules); err != nil {
		t.Fatalf("create v8 schedules: %v", err)
	}
	_ = conn.Close()

	conn2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (migration): %v", err)
	}
	defer func() { _ = conn2.Close() }()

	var version int
	_ = conn2.Get(&version, "SELECT version FROM schema_version")
	if version != schemaVersion {
		t.Errorf("got schema version %d, want %d", version, schemaVersion)
	}

	// The composite PK must be in place: same name across two deployments coexists.
	if _, err := conn2.Exec(`INSERT INTO job_schedules (name, deployment_id, cron_expr, agent, enabled)
		VALUES ('nightly', 'd2', '0 2 * * *', 'maintainer', 1)`); err != nil {
		t.Fatalf("insert same-name schedule for second deployment: %v", err)
	}

	// Last-run history from the v8 row must survive the migration.
	store := NewStore(conn2)
	sched, err := store.GetSchedule("d1", "nightly")
	if err != nil {
		t.Fatalf("GetSchedule after migration: %v", err)
	}
	if !sched.LastRunAt.Valid {
		t.Fatal("last_run_at not preserved through v8→v9 migration")
	}
}

func TestV9toV10ProvenanceMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "migrate910.db")

	// Build a v9 database with a jobs row that predates the provenance columns.
	conn, err := sqlx.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open v9 db: %v", err)
	}
	v9 := `
CREATE TABLE schema_version (version INTEGER NOT NULL);
INSERT INTO schema_version VALUES (9);
CREATE TABLE deployments (
	id TEXT PRIMARY KEY,
	repo_dir TEXT NOT NULL,
	owner TEXT NOT NULL,
	repo TEXT NOT NULL
);
CREATE TABLE jobs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	deployment_id TEXT NOT NULL,
	agent TEXT NOT NULL DEFAULT 'autopilot',
	name TEXT NOT NULL,
	owner TEXT NOT NULL DEFAULT '',
	repo TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'queued'
);
INSERT INTO jobs (deployment_id, agent, name, status)
VALUES ('d1', 'autopilot', 'autopilot-issue-1', 'queued');
`
	if _, err := conn.Exec(v9); err != nil {
		t.Fatalf("create v9 jobs: %v", err)
	}
	_ = conn.Close()

	conn2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (migration): %v", err)
	}
	defer func() { _ = conn2.Close() }()

	var version int
	_ = conn2.Get(&version, "SELECT version FROM schema_version")
	if version != schemaVersion {
		t.Errorf("got schema version %d, want %d", version, schemaVersion)
	}

	// Pre-existing row survives with NULL provenance columns.
	var srcType, srcName, srcRef sql.NullString
	if err := conn2.QueryRow(
		`SELECT source_type, source_name, source_ref FROM jobs WHERE name = 'autopilot-issue-1'`,
	).Scan(&srcType, &srcName, &srcRef); err != nil {
		t.Fatalf("query provenance columns: %v", err)
	}
	if srcType.Valid || srcName.Valid || srcRef.Valid {
		t.Errorf("expected NULL provenance for pre-existing row, got %v/%v/%v", srcType, srcName, srcRef)
	}

	// New columns are writable.
	if _, err := conn2.Exec(
		`UPDATE jobs SET source_type = 'trigger', source_name = 'nightly' WHERE name = 'autopilot-issue-1'`,
	); err != nil {
		t.Fatalf("write provenance: %v", err)
	}
}

func TestAgentRunCRUD(t *testing.T) {
	s := testStore(t)

	d := &Deployment{
		ID: "run-deploy", RepoDir: "/tmp/repo", Owner: "aptx-health", Repo: "agent-minder",
		Mode: "issues", MaxAgents: 1, MaxTurns: 50, MaxBudgetUSD: 5, BaseBranch: "main",
	}
	if err := s.CreateDeployment(d); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	j := &Job{
		DeploymentID: "run-deploy", Agent: "autopilot", Name: "issue-42",
		IssueNumber: 42, Owner: "aptx-health", Repo: "agent-minder", Status: StatusRunning,
	}
	if err := s.CreateJob(j); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	run := &AgentRun{
		JobID:        j.ID,
		Stage:        "code",
		Attempt:      1,
		Agent:        "autopilot",
		Runtime:      sql.NullString{String: "claude-code", Valid: true},
		Model:        sql.NullString{String: "sonnet", Valid: true},
		MaxTurns:     sql.NullInt64{Int64: 50, Valid: true},
		MaxBudgetUSD: sql.NullFloat64{Float64: 5, Valid: true},
		LogPath:      sql.NullString{String: "/tmp/log", Valid: true},
	}
	if err := s.StartAgentRun(run); err != nil {
		t.Fatalf("StartAgentRun: %v", err)
	}
	if run.ID == 0 {
		t.Fatal("StartAgentRun did not populate ID")
	}

	// Live progress updates step_count + last_activity_at.
	if err := s.TouchAgentRun(run.ID, 7); err != nil {
		t.Fatalf("TouchAgentRun: %v", err)
	}
	got, err := s.GetAgentRun(run.ID)
	if err != nil {
		t.Fatalf("GetAgentRun: %v", err)
	}
	if got.Status != RunStatusRunning {
		t.Errorf("status = %q, want running", got.Status)
	}
	if got.StepCount != 7 {
		t.Errorf("step_count = %d, want 7", got.StepCount)
	}
	if !got.LastActivityAt.Valid {
		t.Error("last_activity_at should be set after Touch")
	}
	if got.CompletedAt.Valid {
		t.Error("completed_at should be NULL before completion")
	}

	// Completion writes terminal truth.
	err = s.CompleteAgentRun(run.ID, AgentRunResult{
		Status:     RunStatusSuccess,
		StopReason: "end_turn",
		SessionID:  "sess-1",
		FinalText:  "opened PR",
		FinalTurns: 12,
		CostUSD:    0.34,
		StepCount:  10,
	})
	if err != nil {
		t.Fatalf("CompleteAgentRun: %v", err)
	}
	got, err = s.GetAgentRun(run.ID)
	if err != nil {
		t.Fatalf("GetAgentRun after complete: %v", err)
	}
	if got.Status != RunStatusSuccess {
		t.Errorf("status = %q, want success", got.Status)
	}
	if got.FinalTurns != 12 || got.StepCount != 10 {
		t.Errorf("final_turns=%d step_count=%d, want 12/10", got.FinalTurns, got.StepCount)
	}
	if got.CostUSD != 0.34 {
		t.Errorf("cost_usd = %v, want 0.34", got.CostUSD)
	}
	if got.SessionID.String != "sess-1" || got.StopReason.String != "end_turn" {
		t.Errorf("session/stop = %q/%q, want sess-1/end_turn", got.SessionID.String, got.StopReason.String)
	}
	if !got.CompletedAt.Valid {
		t.Error("completed_at should be set after completion")
	}

	// A second attempt of the same stage coexists and is listed after the first.
	run2 := &AgentRun{JobID: j.ID, Stage: "code", Attempt: 2, Agent: "autopilot"}
	if err := s.StartAgentRun(run2); err != nil {
		t.Fatalf("StartAgentRun attempt 2: %v", err)
	}
	runs, err := s.GetAgentRuns(j.ID)
	if err != nil {
		t.Fatalf("GetAgentRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("GetAgentRuns returned %d rows, want 2", len(runs))
	}
	if runs[0].Attempt != 1 || runs[1].Attempt != 2 {
		t.Errorf("attempts out of order: %d, %d", runs[0].Attempt, runs[1].Attempt)
	}
}

func TestV10toV11AgentRunsMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "migrate1011.db")

	conn, err := sqlx.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open v10 db: %v", err)
	}
	v10 := `
CREATE TABLE schema_version (version INTEGER NOT NULL);
INSERT INTO schema_version VALUES (10);
CREATE TABLE deployments (
	id TEXT PRIMARY KEY,
	repo_dir TEXT NOT NULL,
	owner TEXT NOT NULL,
	repo TEXT NOT NULL
);
CREATE TABLE jobs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	deployment_id TEXT NOT NULL,
	agent TEXT NOT NULL DEFAULT 'autopilot',
	name TEXT NOT NULL,
	owner TEXT NOT NULL DEFAULT '',
	repo TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'queued'
);
INSERT INTO jobs (deployment_id, agent, name, status)
VALUES ('d1', 'autopilot', 'autopilot-issue-1', 'queued');
`
	if _, err := conn.Exec(v10); err != nil {
		t.Fatalf("create v10 db: %v", err)
	}
	_ = conn.Close()

	c2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (migration): %v", err)
	}
	defer func() { _ = c2.Close() }()

	var version int
	_ = c2.Get(&version, "SELECT version FROM schema_version")
	if version != schemaVersion {
		t.Errorf("got schema version %d, want %d", version, schemaVersion)
	}

	// The agent_runs table exists and pre-existing job rows survive untouched.
	store := NewStore(c2)
	runs, err := store.GetAgentRuns(1)
	if err != nil {
		t.Fatalf("GetAgentRuns after migration: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected no runs on migrated db, got %d", len(runs))
	}
	if _, err := c2.Exec(
		`INSERT INTO agent_runs (job_id, stage, attempt, agent, status)
		 VALUES (1, 'code', 1, 'autopilot', 'running')`); err != nil {
		t.Fatalf("insert into migrated agent_runs: %v", err)
	}
}

func TestV11toV12ActivationPolicyMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "migrate1112.db")

	conn, err := sqlx.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open v11 db: %v", err)
	}
	v11 := `
CREATE TABLE schema_version (version INTEGER NOT NULL);
INSERT INTO schema_version VALUES (11);
CREATE TABLE deployments (
	id TEXT PRIMARY KEY,
	repo_dir TEXT NOT NULL,
	owner TEXT NOT NULL,
	repo TEXT NOT NULL,
	mode TEXT NOT NULL DEFAULT 'issues',
	watch_filter TEXT,
	max_agents INTEGER DEFAULT 3,
	max_turns INTEGER DEFAULT 50,
	max_budget_usd REAL DEFAULT 5.0,
	runtime TEXT DEFAULT 'claude-code',
	analyzer_model TEXT DEFAULT 'sonnet',
	skip_label TEXT DEFAULT 'no-agent',
	auto_merge INTEGER DEFAULT 0,
	review_enabled INTEGER DEFAULT 1,
	review_max_turns INTEGER,
	review_max_budget REAL,
	total_budget_usd REAL DEFAULT 25.0,
	carried_cost_usd REAL DEFAULT 0.0,
	base_branch TEXT DEFAULT 'main',
	started_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO deployments (id, repo_dir, owner, repo, mode) VALUES ('d1', '/tmp', 'o', 'r', 'watch');
`
	if _, err := conn.Exec(v11); err != nil {
		t.Fatalf("create v11 db: %v", err)
	}
	_ = conn.Close()

	c2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (migration): %v", err)
	}
	defer func() { _ = c2.Close() }()

	var version int
	_ = c2.Get(&version, "SELECT version FROM schema_version")
	if version != schemaVersion {
		t.Errorf("got schema version %d, want %d", version, schemaVersion)
	}

	// A pre-existing deployment predating the column defaults to 'automated',
	// preserving its pre-migration behavior of unconditionally loading
	// jobs.yaml.
	store := NewStore(c2)
	deploy, err := store.GetDeployment("d1")
	if err != nil {
		t.Fatalf("GetDeployment after migration: %v", err)
	}
	if deploy.ActivationPolicy != ActivationAutomated {
		t.Errorf("migrated activation_policy = %q, want %q", deploy.ActivationPolicy, ActivationAutomated)
	}
}

func TestRenameRepo(t *testing.T) {
	s := testStore(t)

	d := &Deployment{
		ID: "rename-test", RepoDir: "/tmp/myrepo",
		Owner: "acme", Repo: "old-name",
		Mode: "issues", MaxAgents: 2, MaxTurns: 50, MaxBudgetUSD: 5,
	}
	if err := s.CreateDeployment(d); err != nil {
		t.Fatal(err)
	}

	j := &Job{
		DeploymentID: "rename-test", Agent: "autopilot", Name: "issue-1",
		IssueNumber: 1, Owner: "acme", Repo: "old-name", Status: StatusQueued,
	}
	if err := s.CreateJob(j); err != nil {
		t.Fatal(err)
	}

	// Rename.
	n, err := s.RenameRepo("acme", "old-name", "acme", "new-name")
	if err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}
	if n != 2 { // 1 deployment + 1 job
		t.Errorf("RenameRepo affected %d rows, want 2", n)
	}

	// Verify deployment.
	deploys, _ := s.ListDeployments()
	if deploys[0].Repo != "new-name" {
		t.Errorf("deployment repo = %q, want new-name", deploys[0].Repo)
	}

	// Verify job.
	jobs, _ := s.GetJobsByRepo("acme", "new-name")
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job under new-name, got %d", len(jobs))
	}

	// Old name should find nothing.
	old, _ := s.GetJobsByRepo("acme", "old-name")
	if len(old) != 0 {
		t.Errorf("expected 0 jobs under old-name, got %d", len(old))
	}

	// HasRepo.
	has, _ := s.HasRepo("acme", "new-name")
	if !has {
		t.Error("HasRepo(new-name) = false, want true")
	}

	// FindRepoByDir.
	owner, repo, err := s.FindRepoByDir("/tmp/myrepo")
	if err != nil {
		t.Fatalf("FindRepoByDir: %v", err)
	}
	if owner != "acme" || repo != "new-name" {
		t.Errorf("FindRepoByDir = %s/%s, want acme/new-name", owner, repo)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
