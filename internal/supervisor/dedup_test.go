package supervisor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
)

func testStoreForDedup(t *testing.T) *db.Store {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return db.NewStore(conn)
}

func testDeployForDedup(t *testing.T, store *db.Store) *db.Deployment {
	t.Helper()
	d := &db.Deployment{
		ID: "dedup-test", RepoDir: "/tmp", Owner: "acme", Repo: "widgets",
		Mode: "issues", MaxAgents: 3, MaxTurns: 50, MaxBudgetUSD: 5,
		AnalyzerModel: "sonnet", SkipLabel: "no-agent", TotalBudgetUSD: 25,
		BaseBranch: "main", ReviewEnabled: true,
	}
	_ = store.CreateDeployment(d)
	return d
}

func TestDedupRecentRun(t *testing.T) {
	store := testStoreForDedup(t)
	deploy := testDeployForDedup(t, store)

	// Create current job.
	current := &db.Job{
		DeploymentID: deploy.ID, Agent: "dep-updater", Name: "test-now",
		Owner: "acme", Repo: "widgets", Status: db.StatusQueued,
	}
	_ = store.CreateJob(current)

	sc := &SlotContext{Store: store, Deploy: deploy, Job: current}

	// No previous runs — should not skip.
	result := EvaluateDedup(context.Background(), sc, []string{"recent_run:24"})
	if result.Skip {
		t.Errorf("should not skip with no previous runs, got: %s", result.Reason)
	}

	// Add a recently completed job with the same agent.
	old := &db.Job{
		DeploymentID: deploy.ID, Agent: "dep-updater", Name: "test-old",
		Owner: "acme", Repo: "widgets", Status: db.StatusDone,
	}
	_ = store.CreateJob(old)
	// Set completed_at directly (CreateJob doesn't insert it).
	_, _ = store.DB().Exec("UPDATE jobs SET completed_at = ? WHERE id = ?",
		time.Now().UTC().Add(-2*time.Hour), old.ID)

	// Within 24h window — should skip.
	result = EvaluateDedup(context.Background(), sc, []string{"recent_run:24"})
	if !result.Skip {
		t.Error("should skip — same agent ran 2h ago within 24h window")
	}

	// Within 1h window — should not skip (ran 2h ago).
	result = EvaluateDedup(context.Background(), sc, []string{"recent_run:1"})
	if result.Skip {
		t.Error("should not skip — 2h ago is outside 1h window")
	}
}

func TestDedupRecentRunActiveJob(t *testing.T) {
	store := testStoreForDedup(t)
	deploy := testDeployForDedup(t, store)

	current := &db.Job{
		DeploymentID: deploy.ID, Agent: "dep-updater", Name: "test-now",
		Owner: "acme", Repo: "widgets", Status: db.StatusQueued,
	}
	_ = store.CreateJob(current)

	// Add a currently running job with the same agent.
	running := &db.Job{
		DeploymentID: deploy.ID, Agent: "dep-updater", Name: "test-running",
		Owner: "acme", Repo: "widgets", Status: db.StatusRunning,
	}
	_ = store.CreateJob(running)
	_, _ = store.DB().Exec("UPDATE jobs SET started_at = ? WHERE id = ?",
		time.Now().UTC().Add(-10*time.Minute), running.ID)

	sc := &SlotContext{Store: store, Deploy: deploy, Job: current}

	result := EvaluateDedup(context.Background(), sc, []string{"recent_run:24"})
	if !result.Skip {
		t.Error("should skip — same agent is currently running")
	}
}

func TestDedupStackable(t *testing.T) {
	store := testStoreForDedup(t)
	deploy := testDeployForDedup(t, store)

	current := &db.Job{
		DeploymentID: deploy.ID, Agent: "dep-updater", Name: "test",
		Owner: "acme", Repo: "widgets", Status: db.StatusQueued,
	}
	_ = store.CreateJob(current)

	sc := &SlotContext{Store: store, Deploy: deploy, Job: current}

	// Multiple strategies — all must pass (none skip).
	result := EvaluateDedup(context.Background(), sc, []string{"recent_run:1", "branch_exists"})
	if result.Skip {
		t.Error("should not skip when all strategies pass")
	}
}

func TestDedupUnknownStrategy(t *testing.T) {
	store := testStoreForDedup(t)
	deploy := testDeployForDedup(t, store)

	current := &db.Job{
		DeploymentID: deploy.ID, Agent: "test", Name: "test",
		Owner: "acme", Repo: "widgets", Status: db.StatusQueued,
	}
	_ = store.CreateJob(current)

	sc := &SlotContext{Store: store, Deploy: deploy, Job: current}

	// Unknown strategy should not skip.
	result := EvaluateDedup(context.Background(), sc, []string{"unknown_thing"})
	if result.Skip {
		t.Error("unknown strategy should not cause skip")
	}
}

func TestDedupBranchExists(t *testing.T) {
	// Without a real git repo, branch_exists will return not-skip (can't check).
	store := testStoreForDedup(t)
	deploy := testDeployForDedup(t, store)

	current := &db.Job{
		DeploymentID: deploy.ID, Agent: "test", Name: "test",
		Owner: "acme", Repo: "widgets", Status: db.StatusQueued,
	}
	_ = store.CreateJob(current)

	sc := &SlotContext{Store: store, Deploy: deploy, Job: current, Branch: "agent/test"}

	// No git repo at /tmp — should not skip (fails gracefully).
	result := dedupBranchExists(sc)
	if result.Skip {
		t.Error("should not skip when git check fails")
	}
}
