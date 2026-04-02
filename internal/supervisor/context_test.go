package supervisor

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/aptx-health/agent-minder/internal/db"
)

func testStoreForContext(t *testing.T) *db.Store {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return db.NewStore(conn)
}

func testContextSetup(t *testing.T) (*db.Store, *db.Deployment, *db.Job) {
	t.Helper()
	store := testStoreForContext(t)
	deploy := &db.Deployment{
		ID: "ctx-test", RepoDir: t.TempDir(), Owner: "acme", Repo: "widgets",
		Mode: "issues", MaxAgents: 3, MaxTurns: 50, MaxBudgetUSD: 5,
		AnalyzerModel: "sonnet", SkipLabel: "no-agent", TotalBudgetUSD: 25,
		BaseBranch: "main", ReviewEnabled: true,
	}
	_ = store.CreateDeployment(deploy)

	job := &db.Job{
		DeploymentID: deploy.ID, Agent: "autopilot", Name: "issue-42",
		IssueNumber: 42,
		IssueTitle:  sql.NullString{String: "Fix the auth bug", Valid: true},
		IssueBody:   sql.NullString{String: "Auth tokens expire too quickly.", Valid: true},
		Owner:       "acme", Repo: "widgets", Status: db.StatusRunning,
	}
	_ = store.CreateJob(job)
	return store, deploy, job
}

func TestAssembleContext_ReactiveDefaults(t *testing.T) {
	store, deploy, job := testContextSetup(t)

	sc := &SlotContext{
		Store:        store,
		Deploy:       deploy,
		Job:          job,
		RepoDir:      deploy.RepoDir,
		Owner:        "acme",
		Repo:         "widgets",
		WorktreePath: "/tmp/worktree",
		Branch:       "agent/issue-42",
		BaseBranch:   "main",
		TestCommand:  "go test ./...",
	}

	// Default reactive providers (minus issue which needs GitHub, and lessons which are separate).
	providers := []string{"repo_info", "sibling_jobs", "dep_graph"}
	result := AssembleContext(context.Background(), sc, providers)

	// Should contain repo info.
	if !contains(result, "## Repository Info") {
		t.Error("missing repo info section")
	}
	if !contains(result, "acme/widgets") {
		t.Error("missing owner/repo")
	}
	if !contains(result, "go test ./...") {
		t.Error("missing test command")
	}

	// Should contain commands section (because issue number > 0).
	if !contains(result, "## Commands for this task") {
		t.Error("missing commands section")
	}
	if !contains(result, "Fixes #42") {
		t.Error("missing Fixes #42 in commands")
	}
}

func TestAssembleContext_ProactiveNoCommands(t *testing.T) {
	store, deploy, _ := testContextSetup(t)

	// Proactive job — no issue number.
	proJob := &db.Job{
		DeploymentID: deploy.ID, Agent: "dep-updater", Name: "weekly-deps-20260402",
		IssueNumber: 0,
		IssueTitle:  sql.NullString{String: "Check deps", Valid: true},
		Owner:       "acme", Repo: "widgets", Status: db.StatusRunning,
	}
	_ = store.CreateJob(proJob)

	sc := &SlotContext{
		Store:      store,
		Deploy:     deploy,
		Job:        proJob,
		RepoDir:    deploy.RepoDir,
		Owner:      "acme",
		Repo:       "widgets",
		BaseBranch: "main",
	}

	providers := []string{"repo_info"}
	result := AssembleContext(context.Background(), sc, providers)

	// Should have repo info.
	if !contains(result, "## Repository Info") {
		t.Error("missing repo info")
	}

	// Should NOT have commands section (no issue number).
	if contains(result, "## Commands for this task") {
		t.Error("should not have commands for proactive job")
	}
	if contains(result, "Fixes #") {
		t.Error("should not reference issue fixes")
	}
}

func TestAssembleContext_SiblingJobs(t *testing.T) {
	store, deploy, job := testContextSetup(t)

	// Add a sibling job.
	sibling := &db.Job{
		DeploymentID: deploy.ID, Agent: "autopilot", Name: "issue-43",
		IssueNumber: 43,
		IssueTitle:  sql.NullString{String: "Add logging", Valid: true},
		Owner:       "acme", Repo: "widgets", Status: db.StatusQueued,
	}
	_ = store.CreateJob(sibling)

	sc := &SlotContext{Store: store, Deploy: deploy, Job: job, Owner: "acme", Repo: "widgets"}

	result := renderSiblingJobs(sc)
	if !contains(result, "## Related Jobs") {
		t.Error("missing related jobs section")
	}
	if !contains(result, "#43") {
		t.Error("missing sibling issue number")
	}
	if !contains(result, "Add logging") {
		t.Error("missing sibling title")
	}
	// Should not include self.
	if contains(result, "#42") {
		t.Error("should not include self in sibling list")
	}
}

func TestAssembleContext_DepGraph(t *testing.T) {
	store, deploy, job := testContextSetup(t)
	_ = store.SaveDepGraph(deploy.ID, `{"42":[],"43":[42]}`, "conservative")

	sc := &SlotContext{Store: store, Deploy: deploy, Job: job}

	result := renderDepGraph(sc)
	if !contains(result, "## Dependency Graph") {
		t.Error("missing dep graph section")
	}
	if !contains(result, `"42":[]`) {
		t.Error("missing graph content")
	}
}

func TestAssembleContext_EmptyProviders(t *testing.T) {
	store, deploy, job := testContextSetup(t)
	sc := &SlotContext{Store: store, Deploy: deploy, Job: job, Owner: "acme", Repo: "widgets"}

	// Empty provider list — should still get commands (because issue number > 0).
	result := AssembleContext(context.Background(), sc, []string{})
	if !contains(result, "## Commands for this task") {
		t.Error("should still have commands for reactive job")
	}
}

func TestAssembleContext_UnknownProvider(t *testing.T) {
	store, deploy, job := testContextSetup(t)
	sc := &SlotContext{Store: store, Deploy: deploy, Job: job, Owner: "acme", Repo: "widgets"}

	// Unknown provider should be silently skipped.
	result := AssembleContext(context.Background(), sc, []string{"unknown_thing", "repo_info"})
	if !contains(result, "## Repository Info") {
		t.Error("known provider should still render")
	}
}

func TestRenderReviewContext(t *testing.T) {
	store, deploy, job := testContextSetup(t)
	job.PRNumber = sql.NullInt64{Int64: 99, Valid: true}
	job.Branch = sql.NullString{String: "agent/issue-42", Valid: true}

	sc := &SlotContext{
		Store:       store,
		Deploy:      deploy,
		Job:         job,
		Owner:       "acme",
		Repo:        "widgets",
		BaseBranch:  "main",
		TestCommand: "go test ./...",
	}

	result := renderReviewContext(context.Background(), sc)
	if !contains(result, "## Review Context") {
		t.Error("missing review context header")
	}
	if !contains(result, "**PR:** #99") {
		t.Error("missing PR number")
	}
	if !contains(result, "**Issue:** #42") {
		t.Error("missing issue reference")
	}
	if !contains(result, "gh pr diff 99") {
		t.Error("missing PR diff command")
	}
	if !contains(result, "go test ./...") {
		t.Error("missing test command")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
