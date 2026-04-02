package scheduler

import (
	"path/filepath"
	"testing"
	"time"

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

func testDeployment(t *testing.T, store *db.Store) *db.Deployment {
	t.Helper()
	d := &db.Deployment{
		ID: "test-sched", RepoDir: "/tmp", Owner: "acme", Repo: "widgets",
		Mode: "issues", MaxAgents: 3, MaxTurns: 50, MaxBudgetUSD: 5,
		AnalyzerModel: "sonnet", SkipLabel: "no-agent", TotalBudgetUSD: 25,
		BaseBranch: "main", ReviewEnabled: true,
	}
	if err := store.CreateDeployment(d); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	return d
}

func TestSyncSchedules(t *testing.T) {
	store := testStore(t)
	_ = testDeployment(t, store)

	cfg, err := ParseConfig([]byte(`
jobs:
  weekly-deps:
    schedule: "0 9 * * 1"
    agent: dependency-updater
    description: "Check deps"
    budget: 3.0
  nightly-scan:
    schedule: "0 6 * * *"
    agent: security-scanner
  bug-triage:
    trigger: "label:bug"
    agent: autopilot
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	s := New(store, "test-sched", "acme", "widgets", cfg)
	if err := s.SyncSchedules(); err != nil {
		t.Fatalf("SyncSchedules: %v", err)
	}

	// Should have 2 scheduled entries (trigger skipped).
	schedules, err := store.GetEnabledSchedules("test-sched")
	if err != nil {
		t.Fatalf("GetEnabledSchedules: %v", err)
	}
	if len(schedules) != 2 {
		t.Fatalf("got %d schedules, want 2", len(schedules))
	}

	// Verify fields.
	for _, sched := range schedules {
		if sched.Agent == "" {
			t.Errorf("schedule %q: agent is empty", sched.Name)
		}
		if !sched.NextRunAt.Valid {
			t.Errorf("schedule %q: next_run_at not set", sched.Name)
		}
		if !sched.NextRunAt.Time.After(time.Now().UTC()) {
			t.Errorf("schedule %q: next_run_at should be in the future", sched.Name)
		}
	}
}

func TestFireSchedule(t *testing.T) {
	store := testStore(t)
	_ = testDeployment(t, store)

	cfg, _ := ParseConfig([]byte(`
jobs:
  test-job:
    schedule: "* * * * *"
    agent: autopilot
    budget: 2.5
`))

	s := New(store, "test-sched", "acme", "widgets", cfg)
	_ = s.SyncSchedules()

	// Set next_run_at to the past so it's due.
	_ = store.UpdateScheduleRun("test-job", time.Time{}, time.Now().UTC().Add(-time.Minute))

	// Tick should fire the schedule.
	s.tick()

	// Verify a job was created.
	jobs, _ := store.GetJobs("test-sched")
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	if jobs[0].Agent != "autopilot" {
		t.Errorf("agent = %q, want autopilot", jobs[0].Agent)
	}
	if jobs[0].Status != db.StatusQueued {
		t.Errorf("status = %q, want queued", jobs[0].Status)
	}

	// Verify schedule was updated.
	sched, _ := store.GetSchedule("test-job")
	if !sched.LastRunAt.Valid {
		t.Error("last_run_at not set")
	}
	if !sched.NextRunAt.Valid || !sched.NextRunAt.Time.After(time.Now().UTC()) {
		t.Error("next_run_at should be in the future")
	}

	// Tick again — should NOT fire (job still active).
	s.tick()
	jobs2, _ := store.GetJobs("test-sched")
	if len(jobs2) != 1 {
		t.Errorf("got %d jobs after second tick, want 1 (dedup)", len(jobs2))
	}
}

func TestRunOnce(t *testing.T) {
	store := testStore(t)
	_ = testDeployment(t, store)

	cfg, _ := ParseConfig([]byte(`
jobs:
  manual-test:
    schedule: "0 0 1 1 *"
    agent: autopilot
`))

	s := New(store, "test-sched", "acme", "widgets", cfg)
	_ = s.SyncSchedules()

	id, err := s.RunOnce("manual-test")
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero job ID")
	}

	jobs, _ := store.GetJobs("test-sched")
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}

	// Non-existent schedule.
	_, err = s.RunOnce("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent schedule")
	}
}

func TestJobAlreadyActive(t *testing.T) {
	store := testStore(t)
	_ = testDeployment(t, store)

	cfg, _ := ParseConfig([]byte(`
jobs:
  active-test:
    schedule: "* * * * *"
    agent: autopilot
`))

	s := New(store, "test-sched", "acme", "widgets", cfg)

	// No jobs — not active.
	if s.jobAlreadyActive("active-test") {
		t.Error("should not be active with no jobs")
	}

	// Create a queued job matching the pattern.
	_ = store.CreateJob(&db.Job{
		DeploymentID: "test-sched",
		Agent:        "autopilot",
		Name:         "active-test-20260402-0900",
		Owner:        "acme", Repo: "widgets",
		Status: db.StatusQueued,
	})

	if !s.jobAlreadyActive("active-test") {
		t.Error("should be active with queued job")
	}
}
