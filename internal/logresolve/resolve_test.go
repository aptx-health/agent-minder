package logresolve

import (
	"database/sql"
	"errors"
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

func mustCreateJob(t *testing.T, store *db.Store, deployID, name string) *db.Job {
	t.Helper()
	deploy := &db.Deployment{
		ID:      deployID,
		RepoDir: "/tmp/repo",
		Owner:   "acme",
		Repo:    "widgets",
		Mode:    "issues",
	}
	// Deployment might already exist from a prior call in the same test.
	if _, err := store.GetDeployment(deployID); err != nil {
		if err := store.CreateDeployment(deploy); err != nil {
			t.Fatalf("CreateDeployment: %v", err)
		}
	}
	j := &db.Job{
		DeploymentID: deployID,
		Agent:        "autopilot",
		Name:         name,
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusQueued,
	}
	if err := store.CreateJob(j); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	return j
}

func mustStartRun(t *testing.T, store *db.Store, jobID int64, stage string, attempt int, logPath string) *db.AgentRun {
	t.Helper()
	r := &db.AgentRun{
		JobID:   jobID,
		Stage:   stage,
		Attempt: attempt,
		Agent:   "autopilot",
		LogPath: sql.NullString{String: logPath, Valid: logPath != ""},
	}
	if err := store.StartAgentRun(r); err != nil {
		t.Fatalf("StartAgentRun: %v", err)
	}
	return r
}

func TestResolve_NoRuns_FallsBackToLegacyAgentLog(t *testing.T) {
	store := testStore(t)
	job := mustCreateJob(t, store, "deploy-1", "issue-1")

	if _, err := store.DB().Exec("UPDATE jobs SET agent_log = ? WHERE id = ?", "/tmp/legacy.log", job.ID); err != nil {
		t.Fatalf("set agent_log: %v", err)
	}
	job, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}

	path, run, err := Resolve(store, job, Selector{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != "/tmp/legacy.log" {
		t.Errorf("path = %q, want /tmp/legacy.log", path)
	}
	if run != nil {
		t.Errorf("run = %+v, want nil (legacy path)", run)
	}
}

func TestResolve_NoRunsNoLegacy_NotFound(t *testing.T) {
	store := testStore(t)
	job := mustCreateJob(t, store, "deploy-1", "issue-1")

	_, _, err := Resolve(store, job, Selector{})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestResolve_DefaultPicksLatestRunAcrossStages(t *testing.T) {
	store := testStore(t)
	job := mustCreateJob(t, store, "deploy-1", "issue-1")

	mustStartRun(t, store, job.ID, "implement", 1, "/tmp/implement-1.log")
	mustStartRun(t, store, job.ID, "review", 1, "/tmp/review-1.log")

	path, run, err := Resolve(store, job, Selector{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != "/tmp/review-1.log" {
		t.Errorf("path = %q, want /tmp/review-1.log", path)
	}
	if run == nil || run.Stage != "review" {
		t.Errorf("run = %+v, want stage=review", run)
	}
}

func TestResolve_RetriedAttempts_DefaultPicksLatestAttempt(t *testing.T) {
	store := testStore(t)
	job := mustCreateJob(t, store, "deploy-1", "issue-1")

	mustStartRun(t, store, job.ID, "implement", 1, "/tmp/implement-1.log")
	mustStartRun(t, store, job.ID, "implement", 2, "/tmp/implement-2.log")
	mustStartRun(t, store, job.ID, "implement", 3, "/tmp/implement-3.log")

	path, run, err := Resolve(store, job, Selector{Stage: "implement"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != "/tmp/implement-3.log" {
		t.Errorf("path = %q, want /tmp/implement-3.log", path)
	}
	if run == nil || run.Attempt != 3 {
		t.Errorf("run = %+v, want attempt=3", run)
	}
}

func TestResolve_ExplicitStageAndAttempt(t *testing.T) {
	store := testStore(t)
	job := mustCreateJob(t, store, "deploy-1", "issue-1")

	mustStartRun(t, store, job.ID, "implement", 1, "/tmp/implement-1.log")
	mustStartRun(t, store, job.ID, "implement", 2, "/tmp/implement-2.log")
	mustStartRun(t, store, job.ID, "review", 1, "/tmp/review-1.log")

	path, run, err := Resolve(store, job, Selector{Stage: "implement", Attempt: 1})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != "/tmp/implement-1.log" {
		t.Errorf("path = %q, want /tmp/implement-1.log", path)
	}
	if run == nil || run.Attempt != 1 || run.Stage != "implement" {
		t.Errorf("run = %+v, want stage=implement attempt=1", run)
	}
}

func TestResolve_ExplicitSelectorNoMatch_NotFound(t *testing.T) {
	store := testStore(t)
	job := mustCreateJob(t, store, "deploy-1", "issue-1")

	mustStartRun(t, store, job.ID, "implement", 1, "/tmp/implement-1.log")

	// No run for stage "review": an explicit but unmatched selector must not
	// silently fall back to an unrelated log.
	_, _, err := Resolve(store, job, Selector{Stage: "review"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}

	// Nor to job.AgentLog even if one is set.
	if _, err := store.DB().Exec("UPDATE jobs SET agent_log = ? WHERE id = ?", "/tmp/legacy.log", job.ID); err != nil {
		t.Fatalf("set agent_log: %v", err)
	}
	job, err = store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	_, _, err = Resolve(store, job, Selector{Stage: "review"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestResolve_MultiStageJob_PrefersRunsOverLegacyAgentLog(t *testing.T) {
	store := testStore(t)
	job := mustCreateJob(t, store, "deploy-1", "issue-1")

	if _, err := store.DB().Exec("UPDATE jobs SET agent_log = ? WHERE id = ?", "/tmp/legacy.log", job.ID); err != nil {
		t.Fatalf("set agent_log: %v", err)
	}
	job, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}

	mustStartRun(t, store, job.ID, "implement", 1, "/tmp/implement-1.log")

	path, run, err := Resolve(store, job, Selector{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != "/tmp/implement-1.log" {
		t.Errorf("path = %q, want /tmp/implement-1.log (run row should win over legacy agent_log)", path)
	}
	if run == nil {
		t.Errorf("run = nil, want the implement run")
	}
}

func TestSelector_IsZero(t *testing.T) {
	cases := []struct {
		sel  Selector
		want bool
	}{
		{Selector{}, true},
		{Selector{Stage: "implement"}, false},
		{Selector{Attempt: 2}, false},
		{Selector{Attempt: -1}, true},
	}
	for _, c := range cases {
		if got := c.sel.IsZero(); got != c.want {
			t.Errorf("Selector(%+v).IsZero() = %v, want %v", c.sel, got, c.want)
		}
	}
}
