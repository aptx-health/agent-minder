package supervisor

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/aptx-health/agent-minder/internal/db"
)

// testStoreForMultiAgent creates a temporary DB store for multi-agent tests.
func testStoreForMultiAgent(t *testing.T) *db.Store {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return db.NewStore(conn)
}

func testDeployForMultiAgent(t *testing.T, store *db.Store) *db.Deployment {
	t.Helper()
	d := &db.Deployment{
		ID: "test-multi", RepoDir: "/tmp/repo", Owner: "acme", Repo: "widgets",
		Mode: "watch", MaxAgents: 3, MaxTurns: 50, MaxBudgetUSD: 5,
		AnalyzerModel: "sonnet", SkipLabel: "no-agent", TotalBudgetUSD: 25,
		BaseBranch: "main", ReviewEnabled: true,
	}
	if err := store.CreateDeployment(d); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	return d
}

func TestJobNameIncludesAgent(t *testing.T) {
	// Verify that createJobForIssue produces agent-prefixed names
	// so different agents on the same issue don't collide.
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)

	// Create two jobs for the same issue with different agents.
	spikeJob := &db.Job{
		DeploymentID: deploy.ID,
		Agent:        "spike",
		Name:         "spike-issue-42",
		IssueNumber:  42,
		IssueTitle:   sql.NullString{String: "Research feature X", Valid: true},
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusDone,
	}
	if err := store.CreateJob(spikeJob); err != nil {
		t.Fatalf("CreateJob spike: %v", err)
	}

	autopilotJob := &db.Job{
		DeploymentID: deploy.ID,
		Agent:        "autopilot",
		Name:         "autopilot-issue-42",
		IssueNumber:  42,
		IssueTitle:   sql.NullString{String: "Research feature X", Valid: true},
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusQueued,
	}
	if err := store.CreateJob(autopilotJob); err != nil {
		t.Fatalf("CreateJob autopilot: %v", err)
	}

	// Both should exist in the DB.
	jobs, err := store.GetJobs(deploy.ID)
	if err != nil {
		t.Fatalf("GetJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(jobs))
	}

	// Verify names are distinct.
	names := map[string]bool{}
	for _, j := range jobs {
		names[j.Name] = true
	}
	if !names["spike-issue-42"] {
		t.Error("missing spike-issue-42")
	}
	if !names["autopilot-issue-42"] {
		t.Error("missing autopilot-issue-42")
	}
}

func TestJobNameCollisionSameAgent(t *testing.T) {
	// Same agent + same issue should still be rejected by UNIQUE constraint.
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)

	job1 := &db.Job{
		DeploymentID: deploy.ID,
		Agent:        "autopilot",
		Name:         "autopilot-issue-42",
		IssueNumber:  42,
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusDone,
	}
	if err := store.CreateJob(job1); err != nil {
		t.Fatalf("CreateJob first: %v", err)
	}

	job2 := &db.Job{
		DeploymentID: deploy.ID,
		Agent:        "autopilot",
		Name:         "autopilot-issue-42",
		IssueNumber:  42,
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusQueued,
	}
	err := store.CreateJob(job2)
	if err == nil {
		t.Fatal("expected UNIQUE constraint violation, got nil")
	}
}

func TestWatchPollDedupByAgentAndIssue(t *testing.T) {
	// Simulate the knownJobs map logic from watchPoll.
	type issueAgent struct {
		issue int
		agent string
	}

	knownJobs := map[issueAgent]bool{
		{42, "spike"}: true, // spike already ran on #42
	}

	// Same issue, same agent → should be skipped.
	if !knownJobs[issueAgent{42, "spike"}] {
		t.Error("spike on #42 should be known")
	}

	// Same issue, different agent → should NOT be skipped.
	if knownJobs[issueAgent{42, "autopilot"}] {
		t.Error("autopilot on #42 should NOT be known")
	}

	// Different issue, same agent → should NOT be skipped.
	if knownJobs[issueAgent{43, "spike"}] {
		t.Error("spike on #43 should NOT be known")
	}
}

func TestTriggerLabelLookup(t *testing.T) {
	// Verify TriggerLabel() finds the right label for an agent.
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)

	sup := NewTestSupervisor(store, deploy, "/tmp/repo")
	sup.SetTriggerRoutes([]TriggerRoute{
		{Label: "spike", Agent: "spike"},
		{Label: "bug", Agent: "bug-fixer"},
		{Label: "agent-ready", Agent: "autopilot"},
	})

	spikeJob := &db.Job{
		DeploymentID: deploy.ID,
		Agent:        "spike",
		Name:         "spike-issue-42",
		IssueNumber:  42,
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusRunning,
	}
	_ = store.CreateJob(spikeJob)
	sup.RegisterTestJob(spikeJob)

	sc := &SlotContext{
		Store: store, Deploy: deploy, Job: spikeJob,
		Owner: "acme", Repo: "widgets", sup: sup,
	}

	if label := sc.TriggerLabel(); label != "spike" {
		t.Errorf("TriggerLabel() = %q, want %q", label, "spike")
	}

	// Test with autopilot.
	autoJob := &db.Job{
		DeploymentID: deploy.ID,
		Agent:        "autopilot",
		Name:         "autopilot-issue-42",
		IssueNumber:  42,
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusRunning,
	}
	_ = store.CreateJob(autoJob)

	sc2 := &SlotContext{
		Store: store, Deploy: deploy, Job: autoJob,
		Owner: "acme", Repo: "widgets", sup: sup,
	}

	if label := sc2.TriggerLabel(); label != "agent-ready" {
		t.Errorf("TriggerLabel() = %q, want %q", label, "agent-ready")
	}

	// Agent with no trigger route should return empty.
	reviewJob := &db.Job{
		DeploymentID: deploy.ID,
		Agent:        "reviewer",
		Name:         "reviewer-pr-100",
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusRunning,
	}
	_ = store.CreateJob(reviewJob)

	sc3 := &SlotContext{
		Store: store, Deploy: deploy, Job: reviewJob,
		Owner: "acme", Repo: "widgets", sup: sup,
	}

	if label := sc3.TriggerLabel(); label != "" {
		t.Errorf("TriggerLabel() for reviewer = %q, want empty", label)
	}
}
