package coordinator

import (
	"errors"
	"testing"

	"github.com/aptx-health/agent-minder/internal/db"
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
