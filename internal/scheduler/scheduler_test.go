package scheduler

import (
	"database/sql"
	"path/filepath"
	"strings"
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
    runtime: codex
    model: gpt-5
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
		if sched.Name == "weekly-deps" && (!sched.Runtime.Valid || sched.Runtime.String != "codex") {
			t.Errorf("schedule %q: runtime = %v, want codex", sched.Name, sched.Runtime)
		}
		if sched.Name == "weekly-deps" && (!sched.Model.Valid || sched.Model.String != "gpt-5") {
			t.Errorf("schedule %q: model = %v, want gpt-5", sched.Name, sched.Model)
		}
		if !sched.NextRunAt.Valid {
			t.Errorf("schedule %q: next_run_at not set", sched.Name)
		}
		if !sched.NextRunAt.Time.After(time.Now().UTC()) {
			t.Errorf("schedule %q: next_run_at should be in the future", sched.Name)
		}
	}
}

func TestSyncSchedulesConvertScheduleToTrigger(t *testing.T) {
	store := testStore(t)
	_ = testDeployment(t, store)

	// First sync: the job is cron-scheduled, so a cron row is created enabled.
	cfg, err := ParseConfig([]byte(`
jobs:
  triage:
    schedule: "0 9 * * 1"
    agent: autopilot
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	s := New(store, "test-sched", "acme", "widgets", cfg)
	if err := s.SyncSchedules(); err != nil {
		t.Fatalf("SyncSchedules: %v", err)
	}
	sched, err := store.GetSchedule("test-sched", "triage")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if !sched.Enabled {
		t.Fatalf("triage should be enabled after initial sync")
	}

	// Convert the same name in place from schedule: to trigger:.
	cfg2, err := ParseConfig([]byte(`
jobs:
  triage:
    trigger: "label:bug"
    agent: autopilot
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	s2 := New(store, "test-sched", "acme", "widgets", cfg2)
	if err := s2.SyncSchedules(); err != nil {
		t.Fatalf("SyncSchedules (converted): %v", err)
	}

	sched, err = store.GetSchedule("test-sched", "triage")
	if err != nil {
		t.Fatalf("GetSchedule after convert: %v", err)
	}
	if sched.Enabled {
		t.Errorf("triage cron row should be disabled after conversion to trigger")
	}
}

func TestSyncSchedulesAgentScriptConversionReplacesExecutionConfig(t *testing.T) {
	store := testStore(t)
	_ = testDeployment(t, store)

	initial, err := ParseConfig([]byte(`
jobs:
  maintenance:
    schedule: "0 9 * * 1"
    agent: autopilot
    runtime: codex
    model: gpt-5
    budget: 2.5
    max_turns: 25
`))
	if err != nil {
		t.Fatalf("ParseConfig initial: %v", err)
	}
	if err := New(store, "test-sched", "acme", "widgets", initial).SyncSchedules(); err != nil {
		t.Fatalf("SyncSchedules initial: %v", err)
	}

	asScript, err := ParseConfig([]byte(`
jobs:
  maintenance:
    kind: script
    schedule: "0 9 * * 1"
    command: "echo ok"
    timeout: 10s
    env:
      FOO: bar
    workdir: tools
`))
	if err != nil {
		t.Fatalf("ParseConfig script: %v", err)
	}
	if err := New(store, "test-sched", "acme", "widgets", asScript).SyncSchedules(); err != nil {
		t.Fatalf("SyncSchedules script: %v", err)
	}
	sched, err := store.GetSchedule("test-sched", "maintenance")
	if err != nil {
		t.Fatalf("GetSchedule script: %v", err)
	}
	if sched.Kind != db.JobKindScript {
		t.Fatalf("kind = %q, want script", sched.Kind)
	}
	if sched.Runtime.Valid || sched.Model.Valid || sched.Budget.Valid || sched.MaxTurns.Valid {
		t.Fatalf("agent fields survived script conversion: runtime=%v model=%v budget=%v max_turns=%v",
			sched.Runtime, sched.Model, sched.Budget, sched.MaxTurns)
	}
	if !sched.ScriptCommand.Valid || sched.ScriptCommand.String != "echo ok" {
		t.Fatalf("script_command = %v, want echo ok", sched.ScriptCommand)
	}
	if !sched.ScriptEnv.Valid || !strings.Contains(sched.ScriptEnv.String, `"FOO":"bar"`) {
		t.Fatalf("script_env = %v, want FOO", sched.ScriptEnv)
	}

	backToAgent, err := ParseConfig([]byte(`
jobs:
  maintenance:
    schedule: "0 9 * * 1"
    agent: reviewer
`))
	if err != nil {
		t.Fatalf("ParseConfig agent: %v", err)
	}
	if err := New(store, "test-sched", "acme", "widgets", backToAgent).SyncSchedules(); err != nil {
		t.Fatalf("SyncSchedules agent: %v", err)
	}
	sched, err = store.GetSchedule("test-sched", "maintenance")
	if err != nil {
		t.Fatalf("GetSchedule agent: %v", err)
	}
	if sched.Kind != db.JobKindAgent {
		t.Fatalf("kind = %q, want agent", sched.Kind)
	}
	if sched.Agent != "reviewer" {
		t.Fatalf("agent = %q, want reviewer", sched.Agent)
	}
	if sched.ScriptCommand.Valid || sched.ScriptTimeout.Valid || sched.ScriptEnv.Valid || sched.ScriptWorkDir.Valid {
		t.Fatalf("script fields survived agent conversion: command=%v timeout=%v env=%v workdir=%v",
			sched.ScriptCommand, sched.ScriptTimeout, sched.ScriptEnv, sched.ScriptWorkDir)
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
    runtime: codex
    model: gpt-5
    budget: 2.5
`))

	s := New(store, "test-sched", "acme", "widgets", cfg)
	_ = s.SyncSchedules()

	// Set next_run_at to the past so it's due.
	_ = store.UpdateScheduleRun("test-sched", "test-job", time.Time{}, time.Now().UTC().Add(-time.Minute))

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
	if !jobs[0].Runtime.Valid || jobs[0].Runtime.String != "codex" {
		t.Errorf("runtime = %v, want codex", jobs[0].Runtime)
	}
	if !jobs[0].Model.Valid || jobs[0].Model.String != "gpt-5" {
		t.Errorf("model = %v, want gpt-5", jobs[0].Model)
	}
	if !jobs[0].SourceType.Valid || jobs[0].SourceType.String != "cron" {
		t.Errorf("source_type = %v, want cron", jobs[0].SourceType)
	}
	if !jobs[0].SourceName.Valid || jobs[0].SourceName.String != "test-job" {
		t.Errorf("source_name = %v, want test-job", jobs[0].SourceName)
	}
	if !jobs[0].SourceRef.Valid || jobs[0].SourceRef.String == "" {
		t.Errorf("source_ref = %v, want non-empty fire time", jobs[0].SourceRef)
	}

	// Verify schedule was updated.
	sched, _ := store.GetSchedule("test-sched", "test-job")
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

func TestFireScriptScheduleCreatesScriptJob(t *testing.T) {
	store := testStore(t)
	_ = testDeployment(t, store)

	cfg, err := ParseConfig([]byte(`
jobs:
  lint:
    kind: script
    schedule: "* * * * *"
    command: "echo lint"
    timeout: 30s
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	s := New(store, "test-sched", "acme", "widgets", cfg)
	if err := s.SyncSchedules(); err != nil {
		t.Fatalf("SyncSchedules: %v", err)
	}
	_ = store.UpdateScheduleRun("test-sched", "lint", time.Time{}, time.Now().UTC().Add(-time.Minute))

	s.tick()

	jobs, err := store.GetJobs("test-sched")
	if err != nil {
		t.Fatalf("GetJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	job := jobs[0]
	if job.EffectiveKind() != db.JobKindScript {
		t.Fatalf("kind = %q, want script", job.EffectiveKind())
	}
	if job.Agent != db.JobKindScript {
		t.Fatalf("agent display = %q, want script", job.Agent)
	}
	if !job.ScriptCommand.Valid || job.ScriptCommand.String != "echo lint" {
		t.Fatalf("script_command = %v, want echo lint", job.ScriptCommand)
	}
	if !job.ScriptTimeout.Valid || job.ScriptTimeout.String != "30s" {
		t.Fatalf("script_timeout = %v, want 30s", job.ScriptTimeout)
	}
	if job.Runtime.Valid || job.Model.Valid || job.MaxBudgetOv.Valid || job.MaxTurns.Valid {
		t.Fatalf("script job carried agent fields: runtime=%v model=%v budget=%v turns=%v",
			job.Runtime, job.Model, job.MaxBudgetOv, job.MaxTurns)
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
    runtime: codex
    model: gpt-5
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
	if !jobs[0].Runtime.Valid || jobs[0].Runtime.String != "codex" {
		t.Errorf("runtime = %v, want codex", jobs[0].Runtime)
	}
	if !jobs[0].Model.Valid || jobs[0].Model.String != "gpt-5" {
		t.Errorf("model = %v, want gpt-5", jobs[0].Model)
	}
	if !jobs[0].SourceType.Valid || jobs[0].SourceType.String != "cron" {
		t.Errorf("source_type = %v, want cron", jobs[0].SourceType)
	}
	if !jobs[0].SourceName.Valid || jobs[0].SourceName.String != "manual-test" {
		t.Errorf("source_name = %v, want manual-test", jobs[0].SourceName)
	}

	// Non-existent schedule.
	_, err = s.RunOnce("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent schedule")
	}
}

func TestSyncSchedulesOneShot(t *testing.T) {
	store := testStore(t)
	_ = testDeployment(t, store)

	cfg, err := ParseConfig([]byte(`
jobs:
  outage-retry:
    in: "2h"
    agent: dependency-updater
`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	s := New(store, "test-sched", "acme", "widgets", cfg)
	if err := s.SyncSchedules(); err != nil {
		t.Fatalf("SyncSchedules: %v", err)
	}

	sched, err := store.GetSchedule("test-sched", "outage-retry")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if !sched.Enabled {
		t.Error("one-shot should be enabled and pending")
	}
	if !sched.IsOneShot() {
		t.Error("expected IsOneShot")
	}
	if sched.Fired() {
		t.Error("should not have fired yet")
	}
	if sched.CronExpr.Valid {
		t.Error("cron_expr should not be set for a one-shot")
	}
	if !sched.AtTime.Time.After(time.Now().UTC()) {
		t.Error("at_time should be in the future")
	}
}

func TestFireOneShot(t *testing.T) {
	store := testStore(t)
	_ = testDeployment(t, store)

	cfg, _ := ParseConfig([]byte(`
jobs:
  outage-retry:
    in: "20ms"
    agent: dependency-updater
    budget: 1.5
`))

	s := New(store, "test-sched", "acme", "widgets", cfg)
	if err := s.SyncSchedules(); err != nil {
		t.Fatalf("SyncSchedules: %v", err)
	}

	time.Sleep(40 * time.Millisecond)

	s.tick()

	jobs, _ := store.GetJobs("test-sched")
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	if !jobs[0].SourceType.Valid || jobs[0].SourceType.String != "at" {
		t.Errorf("source_type = %v, want at", jobs[0].SourceType)
	}
	if !jobs[0].SourceName.Valid || jobs[0].SourceName.String != "outage-retry" {
		t.Errorf("source_name = %v, want outage-retry", jobs[0].SourceName)
	}
	if !jobs[0].MaxBudgetOv.Valid || jobs[0].MaxBudgetOv.Float64 != 1.5 {
		t.Errorf("max_budget_usd = %v, want 1.5", jobs[0].MaxBudgetOv)
	}

	sched, _ := store.GetSchedule("test-sched", "outage-retry")
	if sched.Enabled {
		t.Error("one-shot should be disabled after firing")
	}
	if !sched.Fired() {
		t.Error("one-shot should be marked fired")
	}

	// Tick again: already disabled, must not fire (and would not be returned
	// by GetEnabledSchedules anyway).
	s.tick()
	jobs2, _ := store.GetJobs("test-sched")
	if len(jobs2) != 1 {
		t.Errorf("got %d jobs after second tick, want 1 (no refire)", len(jobs2))
	}
}

func TestSyncSchedulesOneShotNoRefireOnReload(t *testing.T) {
	store := testStore(t)
	_ = testDeployment(t, store)

	cfg, _ := ParseConfig([]byte(`
jobs:
  outage-retry:
    in: "20ms"
    agent: dependency-updater
`))
	s := New(store, "test-sched", "acme", "widgets", cfg)
	if err := s.SyncSchedules(); err != nil {
		t.Fatalf("SyncSchedules: %v", err)
	}

	time.Sleep(40 * time.Millisecond)
	s.tick()

	fired, err := store.GetSchedule("test-sched", "outage-retry")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if !fired.Fired() {
		t.Fatal("expected schedule to have fired before reload")
	}
	firedAt := fired.LastRunAt.Time

	// Simulate a daemon restart: jobs.yaml is reparsed, re-resolving `in: 20ms`
	// to a fresh future time relative to now.
	cfg2, _ := ParseConfig([]byte(`
jobs:
  outage-retry:
    in: "20ms"
    agent: dependency-updater
`))
	s2 := New(store, "test-sched", "acme", "widgets", cfg2)
	if err := s2.SyncSchedules(); err != nil {
		t.Fatalf("SyncSchedules (reload): %v", err)
	}

	reloaded, err := store.GetSchedule("test-sched", "outage-retry")
	if err != nil {
		t.Fatalf("GetSchedule after reload: %v", err)
	}
	if reloaded.Enabled {
		t.Error("re-resolved one-shot must stay disabled after already firing")
	}
	if !reloaded.LastRunAt.Time.Equal(firedAt) {
		t.Errorf("last_run_at changed on reload: got %v, want %v", reloaded.LastRunAt.Time, firedAt)
	}

	// Ticking after reload must not create a second job.
	time.Sleep(40 * time.Millisecond)
	s2.tick()
	jobs, _ := store.GetJobs("test-sched")
	if len(jobs) != 1 {
		t.Errorf("got %d jobs after restart+reload, want 1 (no refire)", len(jobs))
	}
}

func TestSyncSchedulesConvertCronToOneShotAndBack(t *testing.T) {
	store := testStore(t)
	_ = testDeployment(t, store)

	// Start as cron.
	cfg, _ := ParseConfig([]byte(`
jobs:
  retry:
    schedule: "0 9 * * 1"
    agent: autopilot
`))
	s := New(store, "test-sched", "acme", "widgets", cfg)
	if err := s.SyncSchedules(); err != nil {
		t.Fatalf("SyncSchedules: %v", err)
	}
	assertExactlyOneEnabled(t, store, "retry")

	// Convert to one-shot.
	cfg2, _ := ParseConfig([]byte(`
jobs:
  retry:
    in: "2h"
    agent: autopilot
`))
	s2 := New(store, "test-sched", "acme", "widgets", cfg2)
	if err := s2.SyncSchedules(); err != nil {
		t.Fatalf("SyncSchedules (to one-shot): %v", err)
	}
	sched := assertExactlyOneEnabled(t, store, "retry")
	if !sched.IsOneShot() {
		t.Error("expected converted row to be one-shot")
	}
	if sched.CronExpr.Valid {
		t.Error("cron_expr should be cleared after conversion to one-shot")
	}

	// Convert back to cron.
	cfg3, _ := ParseConfig([]byte(`
jobs:
  retry:
    schedule: "0 9 * * 1"
    agent: autopilot
`))
	s3 := New(store, "test-sched", "acme", "widgets", cfg3)
	if err := s3.SyncSchedules(); err != nil {
		t.Fatalf("SyncSchedules (back to cron): %v", err)
	}
	sched = assertExactlyOneEnabled(t, store, "retry")
	if sched.IsOneShot() {
		t.Error("expected converted row to be cron, not one-shot")
	}
	if !sched.CronExpr.Valid {
		t.Error("cron_expr should be set after conversion back to cron")
	}
}

// assertExactlyOneEnabled fetches all persisted rows for the given schedule
// name across the deployment and fails unless exactly one is enabled.
func assertExactlyOneEnabled(t *testing.T, store *db.Store, name string) *db.JobSchedule {
	t.Helper()
	schedules, err := store.GetSchedules("test-sched")
	if err != nil {
		t.Fatalf("GetSchedules: %v", err)
	}
	var enabled []*db.JobSchedule
	for _, sched := range schedules {
		if sched.Name == name && sched.Enabled {
			enabled = append(enabled, sched)
		}
	}
	if len(enabled) != 1 {
		t.Fatalf("got %d enabled rows for %q, want 1", len(enabled), name)
	}
	return enabled[0]
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

	// Create a queued job whose name does NOT follow the schedule-name prefix
	// convention at all, proving dedup no longer relies on name matching —
	// only on source_type='cron'/source_name=<schedule> provenance.
	_ = store.CreateJob(&db.Job{
		DeploymentID: "test-sched",
		Agent:        "autopilot",
		Name:         "totally-unrelated-job-name",
		Owner:        "acme", Repo: "widgets",
		Status:     db.StatusQueued,
		SourceType: sql.NullString{String: "cron", Valid: true},
		SourceName: sql.NullString{String: "active-test", Valid: true},
	})

	if !s.jobAlreadyActive("active-test") {
		t.Error("should be active with queued job matching provenance, regardless of job name")
	}
}
