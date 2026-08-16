package coordinator

import (
	"errors"
	"testing"

	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/jmoiron/sqlx"
)

// TestProviderScopedReads verifies the StateProvider store reads are scoped to
// the Coordinator's deployment — in particular that Job refuses rows belonging
// to another deployment sharing the host DB.
func TestProviderScopedReads(t *testing.T) {
	store, deploy := setup(t, "")
	coord, err := New(Options{Store: store, Deploy: deploy})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := coord.DeploymentID(); got != deploy.ID {
		t.Fatalf("DeploymentID() = %q, want %q", got, deploy.ID)
	}

	own := &db.Job{DeploymentID: deploy.ID, Agent: "autopilot", Name: "autopilot-issue-1",
		IssueNumber: 1, Owner: deploy.Owner, Repo: deploy.Repo, Status: db.StatusQueued}
	if err := store.CreateJob(own); err != nil {
		t.Fatalf("create own job: %v", err)
	}

	other := &db.Deployment{ID: "other-deploy", RepoDir: deploy.RepoDir, Owner: "acme",
		Repo: "widgets", Mode: "issues", MaxAgents: 1, Runtime: "claude-code", BaseBranch: "main"}
	if err := store.CreateDeployment(other); err != nil {
		t.Fatalf("create other deployment: %v", err)
	}
	foreign := &db.Job{DeploymentID: other.ID, Agent: "autopilot", Name: "autopilot-issue-2",
		IssueNumber: 2, Owner: other.Owner, Repo: other.Repo, Status: db.StatusQueued}
	if err := store.CreateJob(foreign); err != nil {
		t.Fatalf("create foreign job: %v", err)
	}
	ownRun := &db.AgentRun{JobID: own.ID, Stage: "implement", Attempt: 1, Agent: "autopilot"}
	if err := store.StartAgentRun(ownRun); err != nil {
		t.Fatalf("create own run: %v", err)
	}
	foreignRun := &db.AgentRun{JobID: foreign.ID, Stage: "implement", Attempt: 1, Agent: "autopilot"}
	if err := store.StartAgentRun(foreignRun); err != nil {
		t.Fatalf("create foreign run: %v", err)
	}

	job, err := coord.Job(own.ID)
	if err != nil {
		t.Fatalf("Job(own) error: %v", err)
	}
	if job.Name != "autopilot-issue-1" {
		t.Fatalf("Job(own).Name = %q, want autopilot-issue-1", job.Name)
	}
	if _, err := coord.Job(foreign.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Job(foreign) error = %v, want ErrNotFound", err)
	}
	if _, err := coord.Job(foreign.ID + 1000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Job(unknown) error = %v, want ErrNotFound", err)
	}

	jobs, err := coord.Jobs()
	if err != nil {
		t.Fatalf("Jobs() error: %v", err)
	}
	if len(jobs) != 1 || jobs[0].DeploymentID != deploy.ID {
		t.Fatalf("Jobs() = %d jobs (first deployment %q), want exactly the own deployment's 1",
			len(jobs), jobs[0].DeploymentID)
	}

	got, err := coord.Deployment()
	if err != nil {
		t.Fatalf("Deployment() error: %v", err)
	}
	if got.ID != deploy.ID {
		t.Fatalf("Deployment().ID = %q, want %q", got.ID, deploy.ID)
	}
	if _, err := coord.TotalSpend(); err != nil {
		t.Fatalf("TotalSpend() error: %v", err)
	}

	jobSnapshot, err := coord.ReadJobsSnapshot()
	if err != nil {
		t.Fatalf("ReadJobsSnapshot: %v", err)
	}
	if len(jobSnapshot.Jobs) != 1 || jobSnapshot.Jobs[0].ID != own.ID {
		t.Fatalf("job snapshot leaked another deployment: %#v", jobSnapshot.Jobs)
	}
	if _, err := coord.ReadJobSnapshot(foreign.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadJobSnapshot(foreign) = %v, want ErrNotFound", err)
	}
	runs, err := coord.ReadAgentRunsSnapshot(own.ID)
	if err != nil {
		t.Fatalf("ReadAgentRunsSnapshot(own): %v", err)
	}
	if len(runs.Runs) != 1 || runs.Runs[0].ID != ownRun.ID {
		t.Fatalf("run snapshot leaked another deployment: %#v", runs.Runs)
	}
	if _, err := coord.ReadAgentRunsSnapshot(foreign.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadAgentRunsSnapshot(foreign) = %v, want ErrNotFound", err)
	}
}

func TestProviderSnapshotIdentity(t *testing.T) {
	store, deploy := setup(t, "")
	first, err := New(Options{Store: store, Deploy: deploy})
	if err != nil {
		t.Fatalf("New first: %v", err)
	}
	incarnation := first.WorkerIncarnation()
	if incarnation == "" || first.WorkerIncarnation() != incarnation {
		t.Fatalf("incarnation is empty or unstable: %q then %q", incarnation, first.WorkerIncarnation())
	}
	before, err := first.SnapshotMarker()
	if err != nil {
		t.Fatalf("SnapshotMarker first: %v", err)
	}

	second, err := New(Options{Store: store, Deploy: deploy})
	if err != nil {
		t.Fatalf("New second: %v", err)
	}
	if second.WorkerIncarnation() == incarnation {
		t.Fatalf("incarnation did not change across Coordinator reconstruction: %q", incarnation)
	}
	after, err := second.SnapshotMarker()
	if err != nil {
		t.Fatalf("SnapshotMarker second: %v", err)
	}
	if after.LogEpoch == "" || after.LogEpoch != before.LogEpoch {
		t.Fatalf("log epoch changed across Coordinator restart: %q -> %q", before.LogEpoch, after.LogEpoch)
	}
}

func TestProviderSnapshotStateAndWatermark(t *testing.T) {
	store, deploy := setup(t, "")
	coord, err := New(Options{Store: store, Deploy: deploy})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	job := &db.Job{DeploymentID: deploy.ID, Agent: "autopilot", Name: "issue-1",
		Owner: deploy.Owner, Repo: deploy.Repo, Status: db.StatusQueued}
	event := &db.Event{DeploymentID: deploy.ID, Type: "info", Severity: "info", Summary: "created"}
	if err := store.WithTx(func(tx *sqlx.Tx) error {
		if err := db.CreateJobTx(tx, job); err != nil {
			return err
		}
		event.JobID = job.ID
		return db.AppendEventTx(tx, event)
	}); err != nil {
		t.Fatalf("create state and event: %v", err)
	}

	snapshot, err := coord.ReadJobsSnapshot()
	if err != nil {
		t.Fatalf("ReadJobsSnapshot: %v", err)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].ID != job.ID {
		t.Fatalf("snapshot jobs = %#v", snapshot.Jobs)
	}
	if snapshot.Marker.Watermark != event.ID || snapshot.Marker.LogEpoch == "" {
		t.Fatalf("snapshot marker = %#v, event id = %d", snapshot.Marker, event.ID)
	}
}

// TestProviderLifecycleDelegation verifies the control trio reaches the
// supervisor: budget state round-trips and Stop is safe before Start.
func TestProviderLifecycleDelegation(t *testing.T) {
	store, deploy := setup(t, "")
	coord, err := New(Options{Store: store, Deploy: deploy})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if coord.IsBudgetPaused() {
		t.Fatal("IsBudgetPaused() = true on a fresh coordinator")
	}
	coord.ResumeBudget() // no-op when not paused, must not panic
	coord.Stop()         // before Start: must be safe

	if _, err := coord.SubscribeEvents(0); err != nil {
		t.Fatalf("SubscribeEvents(0) error: %v", err)
	}
	if jobs := coord.RunningJobs(); len(jobs) != 0 {
		t.Fatalf("RunningJobs() = %d, want 0", len(jobs))
	}
}
