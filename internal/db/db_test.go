package db

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	conn, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return NewStore(conn)
}

func TestOpenAndMigrate(t *testing.T) {
	store := openTestDB(t)

	// Verify schema version.
	var version int
	if err := store.DB().Get(&version, "SELECT version FROM schema_version LIMIT 1"); err != nil {
		t.Fatalf("get version: %v", err)
	}
	if version != currentVersion {
		t.Errorf("version = %d, want %d", version, currentVersion)
	}
}

func TestOpenIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Open twice — second open should be a no-op migration.
	conn1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = conn1.Close()

	conn2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	_ = conn2.Close()
}

func TestProjectCRUD(t *testing.T) {
	store := openTestDB(t)

	p := &Project{
		Name:                "testproj",
		GoalType:            "feature",
		GoalDescription:     "Build a thing",
		RefreshIntervalSec:  300,
		MessageTTLSec:       172800,
		AutoEnrollWorktrees: true,
		MinderIdentity:      "testproj/minder",
		LLMProvider:         "anthropic",
		LLMModel:            "claude-haiku-4-5",
		LLMSummarizerModel:  "claude-haiku-4-5",
		LLMAnalyzerModel:    "claude-sonnet-4-6",
		IdlePauseSec:        14400,
	}

	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("expected non-zero ID")
	}

	// Get by name.
	got, err := store.GetProject("testproj")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.GoalType != "feature" {
		t.Errorf("GoalType = %q, want %q", got.GoalType, "feature")
	}
	if got.RefreshInterval().Seconds() != 300 {
		t.Errorf("RefreshInterval = %v, want 5m", got.RefreshInterval())
	}
	if got.LLMSummarizerModel != "claude-haiku-4-5" {
		t.Errorf("LLMSummarizerModel = %q, want %q", got.LLMSummarizerModel, "claude-haiku-4-5")
	}
	if got.LLMAnalyzerModel != "claude-sonnet-4-6" {
		t.Errorf("LLMAnalyzerModel = %q, want %q", got.LLMAnalyzerModel, "claude-sonnet-4-6")
	}
	if got.IdlePauseSec != 14400 {
		t.Errorf("IdlePauseSec = %d, want 14400", got.IdlePauseSec)
	}
	if got.IdlePauseDuration().Hours() != 4 {
		t.Errorf("IdlePauseDuration = %v, want 4h", got.IdlePauseDuration())
	}

	// Update.
	got.GoalType = "bugfix"
	got.LLMAnalyzerModel = "claude-opus-4-6"
	if err := store.UpdateProject(got); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	got2, _ := store.GetProjectByID(got.ID)
	if got2.GoalType != "bugfix" {
		t.Errorf("after update GoalType = %q, want %q", got2.GoalType, "bugfix")
	}
	if got2.LLMAnalyzerModel != "claude-opus-4-6" {
		t.Errorf("after update LLMAnalyzerModel = %q, want %q", got2.LLMAnalyzerModel, "claude-opus-4-6")
	}

	// List.
	projects, err := store.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Errorf("ListProjects len = %d, want 1", len(projects))
	}

	// Delete.
	if err := store.DeleteProject(p.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	projects, _ = store.ListProjects()
	if len(projects) != 0 {
		t.Errorf("after delete ListProjects len = %d, want 0", len(projects))
	}
}

func TestRepoAndWorktrees(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "rtest", MinderIdentity: "rtest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	r := &Repo{ProjectID: p.ID, Path: "/tmp/myapp", ShortName: "app"}
	if err := store.AddRepo(r); err != nil {
		t.Fatalf("AddRepo: %v", err)
	}

	repos, _ := store.GetRepos(p.ID)
	if len(repos) != 1 || repos[0].ShortName != "app" {
		t.Fatalf("GetRepos unexpected: %+v", repos)
	}

	// Worktrees.
	wts := []Worktree{
		{Path: "/tmp/myapp", Branch: "main"},
		{Path: "/tmp/myapp-feat", Branch: "feature/auth"},
	}
	if err := store.ReplaceWorktrees(r.ID, wts); err != nil {
		t.Fatalf("ReplaceWorktrees: %v", err)
	}

	got, _ := store.GetWorktrees(r.ID)
	if len(got) != 2 {
		t.Fatalf("GetWorktrees len = %d, want 2", len(got))
	}

	// Replace again with fewer.
	if err := store.ReplaceWorktrees(r.ID, []Worktree{{Path: "/tmp/myapp", Branch: "main"}}); err != nil {
		t.Fatalf("ReplaceWorktrees: %v", err)
	}
	got, _ = store.GetWorktrees(r.ID)
	if len(got) != 1 {
		t.Errorf("after replace GetWorktrees len = %d, want 1", len(got))
	}

	// Cascade delete.
	if err := store.DeleteProject(p.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	repos, _ = store.GetRepos(p.ID)
	if len(repos) != 0 {
		t.Error("repos should be deleted with project")
	}
}

func TestGetWorktreesForProject(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "wtp", MinderIdentity: "wtp/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	r1 := &Repo{ProjectID: p.ID, Path: "/tmp/app", ShortName: "app"}
	if err := store.AddRepo(r1); err != nil {
		t.Fatalf("AddRepo r1: %v", err)
	}
	r2 := &Repo{ProjectID: p.ID, Path: "/tmp/lib", ShortName: "lib"}
	if err := store.AddRepo(r2); err != nil {
		t.Fatalf("AddRepo r2: %v", err)
	}

	if err := store.ReplaceWorktrees(r1.ID, []Worktree{
		{Path: "/tmp/app", Branch: "main"},
		{Path: "/tmp/app-feat", Branch: "feature/auth"},
	}); err != nil {
		t.Fatalf("ReplaceWorktrees r1: %v", err)
	}
	if err := store.ReplaceWorktrees(r2.ID, []Worktree{
		{Path: "/tmp/lib", Branch: "main"},
	}); err != nil {
		t.Fatalf("ReplaceWorktrees r2: %v", err)
	}

	got, err := store.GetWorktreesForProject(p.ID)
	if err != nil {
		t.Fatalf("GetWorktreesForProject: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}

	// Verify ordering: grouped by repo (alphabetical), newest first within repo (highest ID).
	// ReplaceWorktrees inserts in order, so feature/auth (id=2) > main (id=1).
	if got[0].RepoShortName != "app" || got[0].Branch != "feature/auth" {
		t.Errorf("got[0] = %s/%s, want app/feature/auth", got[0].RepoShortName, got[0].Branch)
	}
	if got[1].RepoShortName != "app" || got[1].Branch != "main" {
		t.Errorf("got[1] = %s/%s, want app/main", got[1].RepoShortName, got[1].Branch)
	}
	if got[2].RepoShortName != "lib" || got[2].Branch != "main" {
		t.Errorf("got[2] = %s/%s, want lib/main", got[2].RepoShortName, got[2].Branch)
	}

	// Empty project should return empty slice.
	p2 := &Project{Name: "empty", MinderIdentity: "e/m", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p2); err != nil {
		t.Fatalf("CreateProject p2: %v", err)
	}
	got2, err := store.GetWorktreesForProject(p2.ID)
	if err != nil {
		t.Fatalf("empty project: %v", err)
	}
	if len(got2) != 0 {
		t.Errorf("empty project len = %d, want 0", len(got2))
	}
}

func TestTopics(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "ttest", MinderIdentity: "ttest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	for _, name := range []string{"ttest/app", "ttest/infra", "ttest/coord"} {
		if err := store.AddTopic(&Topic{ProjectID: p.ID, Name: name}); err != nil {
			t.Fatalf("AddTopic %s: %v", name, err)
		}
	}

	topics, _ := store.GetTopics(p.ID)
	if len(topics) != 3 {
		t.Errorf("GetTopics len = %d, want 3", len(topics))
	}
}

func TestConcerns(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "ctest", MinderIdentity: "ctest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	c := &Concern{ProjectID: p.ID, Severity: "warning", Message: "Schema drift detected"}
	if err := store.AddConcern(c); err != nil {
		t.Fatalf("AddConcern: %v", err)
	}

	active, _ := store.ActiveConcerns(p.ID)
	if len(active) != 1 {
		t.Fatalf("ActiveConcerns len = %d, want 1", len(active))
	}

	if err := store.ResolveConcern(c.ID); err != nil {
		t.Fatalf("ResolveConcern: %v", err)
	}
	active, _ = store.ActiveConcerns(p.ID)
	if len(active) != 0 {
		t.Errorf("after resolve ActiveConcerns len = %d, want 0", len(active))
	}
}

func TestPolls(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "ptest", MinderIdentity: "ptest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	if err := store.RecordPoll(&Poll{ProjectID: p.ID, NewCommits: 3, NewMessages: 1, LLMResponseRaw: "All good", Tier1Response: "Summary", Tier2Response: "Analysis"}); err != nil {
		t.Fatalf("RecordPoll 1: %v", err)
	}
	if err := store.RecordPoll(&Poll{ProjectID: p.ID, NewCommits: 0, NewMessages: 0, LLMResponseRaw: "Nothing new", Tier1Response: "No activity"}); err != nil {
		t.Fatalf("RecordPoll 2: %v", err)
	}

	polls, _ := store.RecentPolls(p.ID, 5)
	if len(polls) != 2 {
		t.Fatalf("RecentPolls len = %d, want 2", len(polls))
	}
	// Most recent first.
	if polls[0].LLMResponse() != "No activity" {
		t.Errorf("first poll LLMResponse = %q, want 'No activity'", polls[0].LLMResponse())
	}

	last, _ := store.LastPoll(p.ID)
	if last == nil {
		t.Fatal("LastPoll returned nil")
	}
	if last.NewCommits != 0 {
		t.Errorf("LastPoll.NewCommits = %d, want 0", last.NewCommits)
	}
}

func TestPollLLMResponseAccessor(t *testing.T) {
	// Tier2 takes priority.
	p := &Poll{Tier1Response: "summary", Tier2Response: "analysis"}
	if got := p.LLMResponse(); got != "analysis" {
		t.Errorf("LLMResponse() = %q, want 'analysis'", got)
	}

	// Falls back to tier1.
	p = &Poll{Tier1Response: "summary"}
	if got := p.LLMResponse(); got != "summary" {
		t.Errorf("LLMResponse() = %q, want 'summary'", got)
	}

	// Falls back to raw.
	p = &Poll{LLMResponseRaw: "raw"}
	if got := p.LLMResponse(); got != "raw" {
		t.Errorf("LLMResponse() = %q, want 'raw'", got)
	}
}

func TestMigrationV1ToV2(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Create a v1 database manually.
	conn, err := openRawV1(dbPath)
	if err != nil {
		t.Fatalf("create v1 db: %v", err)
	}

	// Insert v1 data.
	_, err = conn.Exec(`INSERT INTO projects (name, goal_type, goal_description, minder_identity, llm_provider, llm_model)
		VALUES ('migtest', 'feature', 'test goal', 'migtest/minder', 'anthropic', 'claude-haiku-4-5')`)
	if err != nil {
		t.Fatalf("insert v1 project: %v", err)
	}
	_, err = conn.Exec(`INSERT INTO polls (project_id, new_commits, llm_response) VALUES (1, 5, 'old response')`)
	if err != nil {
		t.Fatalf("insert v1 poll: %v", err)
	}
	_ = conn.Close()

	// Re-open with current migration code — should migrate to v2.
	conn2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open after v1: %v", err)
	}
	defer func() { _ = conn2.Close() }()
	store := NewStore(conn2)

	// Check schema version.
	var version int
	if err := store.DB().Get(&version, "SELECT version FROM schema_version LIMIT 1"); err != nil {
		t.Fatalf("get version: %v", err)
	}
	if version != currentVersion {
		t.Errorf("version = %d, want %d", version, currentVersion)
	}

	// Check project got new columns.
	proj, err := store.GetProject("migtest")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if proj.LLMSummarizerModel != "claude-haiku-4-5" {
		t.Errorf("LLMSummarizerModel = %q, want 'claude-haiku-4-5'", proj.LLMSummarizerModel)
	}
	if proj.LLMAnalyzerModel != "claude-sonnet-4-6" {
		t.Errorf("LLMAnalyzerModel = %q, want 'claude-sonnet-4-6' (default)", proj.LLMAnalyzerModel)
	}

	// Check poll got tier1_response migrated.
	last, err := store.LastPoll(proj.ID)
	if err != nil {
		t.Fatalf("LastPoll: %v", err)
	}
	if last.Tier1Response != "old response" {
		t.Errorf("Tier1Response = %q, want 'old response'", last.Tier1Response)
	}
}

// openRawV1 creates a database with only the v1 schema (no v2 columns).
func openRawV1(path string) (*sqlx.DB, error) {
	db, err := sqlx.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(schemaV1_only); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec("INSERT INTO schema_version (version) VALUES (1)"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func TestTrackedItemsCRUD(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "titest", MinderIdentity: "titest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	item := &TrackedItem{
		ProjectID:  p.ID,
		Source:     "github",
		Owner:      "octocat",
		Repo:       "hello-world",
		Number:     42,
		ItemType:   "issue",
		Title:      "Fix the thing",
		State:      "open",
		LastStatus: "Open",
	}
	if err := store.AddTrackedItem(item); err != nil {
		t.Fatalf("AddTrackedItem: %v", err)
	}
	if item.ID == 0 {
		t.Fatal("expected non-zero ID")
	}

	// Get items.
	items, err := store.GetTrackedItems(p.ID)
	if err != nil {
		t.Fatalf("GetTrackedItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("GetTrackedItems len = %d, want 1", len(items))
	}
	if items[0].DisplayRef() != "octocat/hello-world#42" {
		t.Errorf("DisplayRef = %q", items[0].DisplayRef())
	}

	// Update with new v4 fields.
	items[0].State = "closed"
	items[0].LastStatus = "Closd"
	items[0].LastCheckedAt = "2026-03-12T00:00:00Z"
	items[0].ContentHash = "abc123"
	items[0].ObjectiveSummary = "Fix a critical bug"
	items[0].ProgressSummary = "PR merged, awaiting deploy"
	if err := store.UpdateTrackedItem(&items[0]); err != nil {
		t.Fatalf("UpdateTrackedItem: %v", err)
	}
	items, _ = store.GetTrackedItems(p.ID)
	if items[0].LastStatus != "Closd" {
		t.Errorf("after update LastStatus = %q, want Closd", items[0].LastStatus)
	}
	if items[0].ContentHash != "abc123" {
		t.Errorf("after update ContentHash = %q, want abc123", items[0].ContentHash)
	}
	if items[0].ObjectiveSummary != "Fix a critical bug" {
		t.Errorf("after update ObjectiveSummary = %q", items[0].ObjectiveSummary)
	}
	if items[0].ProgressSummary != "PR merged, awaiting deploy" {
		t.Errorf("after update ProgressSummary = %q", items[0].ProgressSummary)
	}

	// Duplicate insert should fail.
	dup := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "octocat", Repo: "hello-world", Number: 42}
	if err := store.AddTrackedItem(dup); err == nil {
		t.Error("expected duplicate insert to fail")
	}

	// Remove.
	if err := store.RemoveTrackedItem(p.ID, "octocat", "hello-world", 42); err != nil {
		t.Fatalf("RemoveTrackedItem: %v", err)
	}
	items, _ = store.GetTrackedItems(p.ID)
	if len(items) != 0 {
		t.Errorf("after remove len = %d, want 0", len(items))
	}

	// Remove non-existent should fail.
	if err := store.RemoveTrackedItem(p.ID, "x", "y", 1); err == nil {
		t.Error("expected remove of non-existent item to fail")
	}
}

func TestTrackedItemsCap(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "captest", MinderIdentity: "captest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Add 20 items.
	for i := 1; i <= 20; i++ {
		item := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Open"}
		if err := store.AddTrackedItem(item); err != nil {
			t.Fatalf("AddTrackedItem %d: %v", i, err)
		}
	}

	// 21st should fail.
	item := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 21, LastStatus: "Open"}
	if err := store.AddTrackedItem(item); err == nil {
		t.Error("expected 21st item to be rejected")
	}
}

func TestTrackedItemCascadeDelete(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "cascade", MinderIdentity: "cascade/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	if err := store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "o", Repo: "r", Number: 1, LastStatus: "Open"}); err != nil {
		t.Fatalf("AddTrackedItem: %v", err)
	}

	// Delete project should cascade.
	if err := store.DeleteProject(p.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	items, _ := store.GetTrackedItems(p.ID)
	if len(items) != 0 {
		t.Error("tracked items should be deleted with project")
	}
}

func TestBulkAddTrackedItems(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "bulktest", MinderIdentity: "bulktest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Bulk add 5 items.
	items := make([]*TrackedItem, 5)
	for i := range items {
		items[i] = &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i + 1, LastStatus: "Open"}
	}
	added, err := store.BulkAddTrackedItems(items)
	if err != nil {
		t.Fatalf("BulkAddTrackedItems: %v", err)
	}
	if added != 5 {
		t.Errorf("expected 5 added, got %d", added)
	}

	// Bulk add same items again — duplicates should be ignored.
	added, err = store.BulkAddTrackedItems(items)
	if err != nil {
		t.Fatalf("BulkAddTrackedItems dupes: %v", err)
	}
	if added != 0 {
		t.Errorf("expected 0 added for dupes, got %d", added)
	}

	// Verify count.
	got, _ := store.GetTrackedItems(p.ID)
	if len(got) != 5 {
		t.Errorf("expected 5 items, got %d", len(got))
	}
}

func TestBulkAddTrackedItemsCap(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "bulkcap", MinderIdentity: "bulkcap/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Add 18 items individually.
	for i := 1; i <= 18; i++ {
		if err := store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Open"}); err != nil {
			t.Fatalf("AddTrackedItem %d: %v", i, err)
		}
	}

	// Bulk add 5 more — only 2 should fit (cap 20).
	items := make([]*TrackedItem, 5)
	for i := range items {
		items[i] = &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 100 + i, LastStatus: "Open"}
	}
	added, err := store.BulkAddTrackedItems(items)
	if err != nil {
		t.Fatalf("BulkAddTrackedItems: %v", err)
	}
	if added != 2 {
		t.Errorf("expected 2 added (cap enforcement), got %d", added)
	}

	got, _ := store.GetTrackedItems(p.ID)
	if len(got) != 20 {
		t.Errorf("expected 20 items total, got %d", len(got))
	}
}

func TestClearTrackedItems(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "cleartest", MinderIdentity: "cleartest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	for i := 1; i <= 5; i++ {
		if err := store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Open"}); err != nil {
			t.Fatalf("AddTrackedItem %d: %v", i, err)
		}
	}

	if err := store.ClearTrackedItems(p.ID); err != nil {
		t.Fatalf("ClearTrackedItems: %v", err)
	}

	got, _ := store.GetTrackedItems(p.ID)
	if len(got) != 0 {
		t.Errorf("expected 0 items after clear, got %d", len(got))
	}
}

func TestMigrationV3ToV4(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Create a v3 database manually.
	conn, err := openRawV3(dbPath)
	if err != nil {
		t.Fatalf("create v3 db: %v", err)
	}

	// Insert v3 data with a tracked item.
	_, err = conn.Exec(`INSERT INTO projects (name, goal_type, goal_description, minder_identity, llm_provider, llm_model, llm_summarizer_model, llm_analyzer_model)
		VALUES ('migtest4', 'feature', 'test goal', 'migtest4/minder', 'anthropic', 'claude-haiku-4-5', 'claude-haiku-4-5', 'claude-sonnet-4-6')`)
	if err != nil {
		t.Fatalf("insert v3 project: %v", err)
	}
	_, err = conn.Exec(`INSERT INTO tracked_items (project_id, source, owner, repo, number, item_type, title, state, labels, last_status)
		VALUES (1, 'github', 'octocat', 'hello', 42, 'issue', 'Test', 'open', 'bug', 'Open')`)
	if err != nil {
		t.Fatalf("insert v3 tracked item: %v", err)
	}
	_ = conn.Close()

	// Re-open with current migration code — should migrate to v4.
	conn2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open after v3: %v", err)
	}
	defer func() { _ = conn2.Close() }()
	store := NewStore(conn2)

	// Check schema version.
	var version int
	if err := store.DB().Get(&version, "SELECT version FROM schema_version LIMIT 1"); err != nil {
		t.Fatalf("get version: %v", err)
	}
	if version != currentVersion {
		t.Errorf("version = %d, want %d", version, currentVersion)
	}

	// Check tracked item has new columns with defaults.
	items, err := store.GetTrackedItems(1)
	if err != nil {
		t.Fatalf("GetTrackedItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 tracked item, got %d", len(items))
	}
	if items[0].ContentHash != "" {
		t.Errorf("ContentHash = %q, want empty default", items[0].ContentHash)
	}
	if items[0].ObjectiveSummary != "" {
		t.Errorf("ObjectiveSummary = %q, want empty default", items[0].ObjectiveSummary)
	}
	if items[0].ProgressSummary != "" {
		t.Errorf("ProgressSummary = %q, want empty default", items[0].ProgressSummary)
	}

	// Verify new columns can be updated.
	items[0].ContentHash = "deadbeef"
	items[0].ObjectiveSummary = "Fix auth"
	items[0].ProgressSummary = "In review"
	if err := store.UpdateTrackedItem(&items[0]); err != nil {
		t.Fatalf("UpdateTrackedItem after migration: %v", err)
	}
	items, _ = store.GetTrackedItems(1)
	if items[0].ContentHash != "deadbeef" {
		t.Errorf("ContentHash = %q, want deadbeef", items[0].ContentHash)
	}
}

func TestMigrationV4ToV5(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Create a v4 database manually.
	conn, err := openRawV4(dbPath)
	if err != nil {
		t.Fatalf("create v4 db: %v", err)
	}

	// Insert v4 data.
	_, err = conn.Exec(`INSERT INTO projects (name, goal_type, goal_description, minder_identity, llm_provider, llm_model, llm_summarizer_model, llm_analyzer_model)
		VALUES ('migtest5', 'feature', 'test goal', 'migtest5/minder', 'anthropic', 'claude-haiku-4-5', 'claude-haiku-4-5', 'claude-sonnet-4-6')`)
	if err != nil {
		t.Fatalf("insert v4 project: %v", err)
	}
	_ = conn.Close()

	// Re-open with current migration code — should migrate to v5.
	conn2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open after v4: %v", err)
	}
	defer func() { _ = conn2.Close() }()
	store := NewStore(conn2)

	// Check schema version.
	var version int
	if err := store.DB().Get(&version, "SELECT version FROM schema_version LIMIT 1"); err != nil {
		t.Fatalf("get version: %v", err)
	}
	if version != currentVersion {
		t.Errorf("version = %d, want %d", version, currentVersion)
	}

	// Check project got idle_pause_sec with default value.
	proj, err := store.GetProject("migtest5")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if proj.IdlePauseSec != 14400 {
		t.Errorf("IdlePauseSec = %d, want 14400 (default)", proj.IdlePauseSec)
	}

	// Verify it can be updated.
	proj.IdlePauseSec = 7200
	if err := store.UpdateProject(proj); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	proj, _ = store.GetProject("migtest5")
	if proj.IdlePauseSec != 7200 {
		t.Errorf("after update IdlePauseSec = %d, want 7200", proj.IdlePauseSec)
	}
}

// openRawV4 creates a database with the v4 schema (no idle_pause_sec).
func openRawV4(path string) (*sqlx.DB, error) {
	db, err := sqlx.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(schemaV4_only); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec("INSERT INTO schema_version (version) VALUES (4)"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// schemaV4_only is the v4 schema without the v5 idle_pause_sec column.
const schemaV4_only = `
CREATE TABLE IF NOT EXISTS schema_version (
	version INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS projects (
	id                    INTEGER PRIMARY KEY,
	name                  TEXT UNIQUE NOT NULL,
	goal_type             TEXT,
	goal_description      TEXT,
	refresh_interval_sec  INTEGER DEFAULT 300,
	message_ttl_sec       INTEGER DEFAULT 172800,
	auto_enroll_worktrees BOOLEAN DEFAULT 1,
	minder_identity       TEXT,
	llm_provider          TEXT DEFAULT 'anthropic',
	llm_model             TEXT DEFAULT 'claude-haiku-4-5',
	llm_summarizer_model  TEXT DEFAULT 'claude-haiku-4-5',
	llm_analyzer_model    TEXT DEFAULT 'claude-sonnet-4-6',
	created_at            TEXT DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS repos (
	id         INTEGER PRIMARY KEY,
	project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	path       TEXT NOT NULL,
	short_name TEXT NOT NULL,
	summary    TEXT,
	UNIQUE(project_id, path)
);
CREATE TABLE IF NOT EXISTS worktrees (
	id      INTEGER PRIMARY KEY,
	repo_id INTEGER REFERENCES repos(id) ON DELETE CASCADE,
	path    TEXT NOT NULL,
	branch  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS topics (
	id         INTEGER PRIMARY KEY,
	project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	UNIQUE(project_id, name)
);
CREATE TABLE IF NOT EXISTS concerns (
	id          INTEGER PRIMARY KEY,
	project_id  INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	severity    TEXT DEFAULT 'warning',
	message     TEXT NOT NULL,
	resolved    BOOLEAN DEFAULT 0,
	created_at  TEXT DEFAULT (datetime('now')),
	resolved_at TEXT
);
CREATE TABLE IF NOT EXISTS polls (
	id              INTEGER PRIMARY KEY,
	project_id      INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	new_commits     INTEGER DEFAULT 0,
	new_messages    INTEGER DEFAULT 0,
	concerns_raised INTEGER DEFAULT 0,
	llm_response    TEXT,
	tier1_response  TEXT DEFAULT '',
	tier2_response  TEXT DEFAULT '',
	bus_message_sent TEXT DEFAULT '',
	polled_at       TEXT DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS tracked_items (
	id              INTEGER PRIMARY KEY,
	project_id      INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	source          TEXT NOT NULL DEFAULT 'github',
	owner           TEXT NOT NULL,
	repo            TEXT NOT NULL,
	number          INTEGER NOT NULL,
	item_type       TEXT NOT NULL DEFAULT 'issue',
	title           TEXT NOT NULL DEFAULT '',
	state           TEXT NOT NULL DEFAULT 'open',
	labels          TEXT NOT NULL DEFAULT '',
	last_status     TEXT NOT NULL DEFAULT 'Open',
	last_checked_at     TEXT DEFAULT '',
	content_hash        TEXT DEFAULT '',
	objective_summary   TEXT DEFAULT '',
	progress_summary    TEXT DEFAULT '',
	created_at          TEXT DEFAULT (datetime('now')),
	UNIQUE(project_id, source, owner, repo, number)
);
`

// openRawV3 creates a database with the v3 schema (no v4 columns on tracked_items).
func openRawV3(path string) (*sqlx.DB, error) {
	db, err := sqlx.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(schemaV3_only); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec("INSERT INTO schema_version (version) VALUES (3)"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// schemaV3_only is the v3 schema without v4 columns on tracked_items.
const schemaV3_only = `
CREATE TABLE IF NOT EXISTS schema_version (
	version INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS projects (
	id                    INTEGER PRIMARY KEY,
	name                  TEXT UNIQUE NOT NULL,
	goal_type             TEXT,
	goal_description      TEXT,
	refresh_interval_sec  INTEGER DEFAULT 300,
	message_ttl_sec       INTEGER DEFAULT 172800,
	auto_enroll_worktrees BOOLEAN DEFAULT 1,
	minder_identity       TEXT,
	llm_provider          TEXT DEFAULT 'anthropic',
	llm_model             TEXT DEFAULT 'claude-haiku-4-5',
	llm_summarizer_model  TEXT DEFAULT 'claude-haiku-4-5',
	llm_analyzer_model    TEXT DEFAULT 'claude-sonnet-4-6',
	created_at            TEXT DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS repos (
	id         INTEGER PRIMARY KEY,
	project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	path       TEXT NOT NULL,
	short_name TEXT NOT NULL,
	summary    TEXT,
	UNIQUE(project_id, path)
);
CREATE TABLE IF NOT EXISTS worktrees (
	id      INTEGER PRIMARY KEY,
	repo_id INTEGER REFERENCES repos(id) ON DELETE CASCADE,
	path    TEXT NOT NULL,
	branch  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS topics (
	id         INTEGER PRIMARY KEY,
	project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	UNIQUE(project_id, name)
);
CREATE TABLE IF NOT EXISTS concerns (
	id          INTEGER PRIMARY KEY,
	project_id  INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	severity    TEXT DEFAULT 'warning',
	message     TEXT NOT NULL,
	resolved    BOOLEAN DEFAULT 0,
	created_at  TEXT DEFAULT (datetime('now')),
	resolved_at TEXT
);
CREATE TABLE IF NOT EXISTS polls (
	id              INTEGER PRIMARY KEY,
	project_id      INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	new_commits     INTEGER DEFAULT 0,
	new_messages    INTEGER DEFAULT 0,
	concerns_raised INTEGER DEFAULT 0,
	llm_response    TEXT,
	tier1_response  TEXT DEFAULT '',
	tier2_response  TEXT DEFAULT '',
	bus_message_sent TEXT DEFAULT '',
	polled_at       TEXT DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS tracked_items (
	id              INTEGER PRIMARY KEY,
	project_id      INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	source          TEXT NOT NULL DEFAULT 'github',
	owner           TEXT NOT NULL,
	repo            TEXT NOT NULL,
	number          INTEGER NOT NULL,
	item_type       TEXT NOT NULL DEFAULT 'issue',
	title           TEXT NOT NULL DEFAULT '',
	state           TEXT NOT NULL DEFAULT 'open',
	labels          TEXT NOT NULL DEFAULT '',
	last_status     TEXT NOT NULL DEFAULT 'Open',
	last_checked_at TEXT DEFAULT '',
	created_at      TEXT DEFAULT (datetime('now')),
	UNIQUE(project_id, source, owner, repo, number)
);
`

// schemaV1_only is the original v1 schema without v2 columns, for migration testing.
const schemaV1_only = `
CREATE TABLE IF NOT EXISTS schema_version (
	version INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS projects (
	id                    INTEGER PRIMARY KEY,
	name                  TEXT UNIQUE NOT NULL,
	goal_type             TEXT,
	goal_description      TEXT,
	refresh_interval_sec  INTEGER DEFAULT 300,
	message_ttl_sec       INTEGER DEFAULT 172800,
	auto_enroll_worktrees BOOLEAN DEFAULT 1,
	minder_identity       TEXT,
	llm_provider          TEXT DEFAULT 'anthropic',
	llm_model             TEXT DEFAULT 'claude-haiku-4-5',
	created_at            TEXT DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS repos (
	id         INTEGER PRIMARY KEY,
	project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	path       TEXT NOT NULL,
	short_name TEXT NOT NULL,
	summary    TEXT,
	UNIQUE(project_id, path)
);
CREATE TABLE IF NOT EXISTS worktrees (
	id      INTEGER PRIMARY KEY,
	repo_id INTEGER REFERENCES repos(id) ON DELETE CASCADE,
	path    TEXT NOT NULL,
	branch  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS topics (
	id         INTEGER PRIMARY KEY,
	project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	UNIQUE(project_id, name)
);
CREATE TABLE IF NOT EXISTS concerns (
	id          INTEGER PRIMARY KEY,
	project_id  INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	severity    TEXT DEFAULT 'warning',
	message     TEXT NOT NULL,
	resolved    BOOLEAN DEFAULT 0,
	created_at  TEXT DEFAULT (datetime('now')),
	resolved_at TEXT
);
CREATE TABLE IF NOT EXISTS polls (
	id              INTEGER PRIMARY KEY,
	project_id      INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	new_commits     INTEGER DEFAULT 0,
	new_messages    INTEGER DEFAULT 0,
	concerns_raised INTEGER DEFAULT 0,
	llm_response    TEXT,
	polled_at       TEXT DEFAULT (datetime('now'))
);
`

func TestPruneTrackedItems(t *testing.T) {
	store := openTestDB(t)
	p := &Project{Name: "prunetest", MinderIdentity: "prunetest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Add 10 items: 5 open, 5 closed with staggered timestamps.
	for i := 1; i <= 5; i++ {
		item := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Open"}
		if err := store.AddTrackedItem(item); err != nil {
			t.Fatalf("AddTrackedItem %d: %v", i, err)
		}
	}
	for i := 6; i <= 10; i++ {
		item := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Closd", LastCheckedAt: fmt.Sprintf("2026-01-%02dT00:00:00Z", i)}
		if err := store.AddTrackedItem(item); err != nil {
			t.Fatalf("AddTrackedItem %d: %v", i, err)
		}
	}

	// At exactly 10, prune should still trigger (>= maxTotal).
	pruned, err := store.PruneTrackedItems(p.ID, 10, 2)
	if err != nil {
		t.Fatalf("PruneTrackedItems: %v", err)
	}
	// Should prune enough to get below 10, keeping 2 terminal items.
	// 5 terminal - 2 keep = 3 removable, excess = 10-10+1 = 1, so prune 1.
	if pruned != 1 {
		t.Errorf("pruned = %d, want 1", pruned)
	}

	items, _ := store.GetTrackedItems(p.ID)
	if len(items) != 9 {
		t.Errorf("after prune len = %d, want 9", len(items))
	}

	// Oldest terminal (#6, checked Jan 6) should be gone.
	for _, item := range items {
		if item.Number == 6 {
			t.Errorf("item #6 should have been pruned")
		}
	}
}

func TestPruneTrackedItems_UnderThreshold(t *testing.T) {
	store := openTestDB(t)
	p := &Project{Name: "pruneskip", MinderIdentity: "pruneskip/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	for i := 1; i <= 5; i++ {
		item := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Closd"}
		if err := store.AddTrackedItem(item); err != nil {
			t.Fatalf("AddTrackedItem: %v", err)
		}
	}

	pruned, err := store.PruneTrackedItems(p.ID, 10, 2)
	if err != nil {
		t.Fatalf("PruneTrackedItems: %v", err)
	}
	if pruned != 0 {
		t.Errorf("pruned = %d, want 0 (under threshold)", pruned)
	}
}

func TestPruneTrackedItems_AllOpen(t *testing.T) {
	store := openTestDB(t)
	p := &Project{Name: "pruneopen", MinderIdentity: "pruneopen/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	for i := 1; i <= 10; i++ {
		item := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Open"}
		if err := store.AddTrackedItem(item); err != nil {
			t.Fatalf("AddTrackedItem: %v", err)
		}
	}

	pruned, err := store.PruneTrackedItems(p.ID, 10, 2)
	if err != nil {
		t.Fatalf("PruneTrackedItems: %v", err)
	}
	if pruned != 0 {
		t.Errorf("pruned = %d, want 0 (no terminal items)", pruned)
	}
}

func TestPruneTrackedItems_RespectsKeepTerminal(t *testing.T) {
	store := openTestDB(t)
	p := &Project{Name: "prunekeep", MinderIdentity: "prunekeep/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// 8 open + 2 terminal = 10 total. keepTerminal=2 means 0 removable.
	for i := 1; i <= 8; i++ {
		item := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Open"}
		if err := store.AddTrackedItem(item); err != nil {
			t.Fatalf("AddTrackedItem: %v", err)
		}
	}
	for i := 9; i <= 10; i++ {
		item := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Mrgd"}
		if err := store.AddTrackedItem(item); err != nil {
			t.Fatalf("AddTrackedItem: %v", err)
		}
	}

	pruned, err := store.PruneTrackedItems(p.ID, 10, 2)
	if err != nil {
		t.Fatalf("PruneTrackedItems: %v", err)
	}
	if pruned != 0 {
		t.Errorf("pruned = %d, want 0 (only 2 terminal, keepTerminal=2)", pruned)
	}
}

func TestRemoveTerminalTrackedItems(t *testing.T) {
	store := openTestDB(t)
	p := &Project{Name: "cleanuptest", MinderIdentity: "cleanuptest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Add 3 open, 2 closed, 1 merged, 1 not-planned.
	for i := 1; i <= 3; i++ {
		if err := store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Open"}); err != nil {
			t.Fatalf("AddTrackedItem %d: %v", i, err)
		}
	}
	if err := store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 4, LastStatus: "Closd"}); err != nil {
		t.Fatalf("AddTrackedItem 4: %v", err)
	}
	if err := store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 5, LastStatus: "Mrgd"}); err != nil {
		t.Fatalf("AddTrackedItem 5: %v", err)
	}
	if err := store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 6, LastStatus: "Closd"}); err != nil {
		t.Fatalf("AddTrackedItem 6: %v", err)
	}
	if err := store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 7, LastStatus: "NotPl"}); err != nil {
		t.Fatalf("AddTrackedItem 7: %v", err)
	}

	// Count terminal items.
	count, err := store.CountTerminalTrackedItems(p.ID)
	if err != nil {
		t.Fatalf("CountTerminalTrackedItems: %v", err)
	}
	if count != 4 {
		t.Errorf("count = %d, want 4", count)
	}

	// Remove terminal items.
	removed, err := store.RemoveTerminalTrackedItems(p.ID)
	if err != nil {
		t.Fatalf("RemoveTerminalTrackedItems: %v", err)
	}
	if removed != 4 {
		t.Errorf("removed = %d, want 4", removed)
	}

	// Verify only open items remain.
	items, _ := store.GetTrackedItems(p.ID)
	if len(items) != 3 {
		t.Fatalf("remaining = %d, want 3", len(items))
	}
	for _, item := range items {
		if item.LastStatus != "Open" {
			t.Errorf("expected Open, got %s for #%d", item.LastStatus, item.Number)
		}
	}

	// Count should now be 0.
	count, _ = store.CountTerminalTrackedItems(p.ID)
	if count != 0 {
		t.Errorf("count after cleanup = %d, want 0", count)
	}
}

func TestArchiveTrackedItem(t *testing.T) {
	store := openTestDB(t)
	p := &Project{Name: "archivetest", MinderIdentity: "archivetest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Item with progress summary should be archived.
	item := &TrackedItem{
		ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 1,
		ItemType: "pull_request", Title: "Add auth", LastStatus: "Mrgd",
		ObjectiveSummary: "Implement OAuth2 flow",
		ProgressSummary:  "OAuth2 flow implemented and merged",
	}
	if err := store.AddTrackedItem(item); err != nil {
		t.Fatalf("AddTrackedItem: %v", err)
	}

	err := store.ArchiveTrackedItem(item)
	if err != nil {
		t.Fatalf("ArchiveTrackedItem: %v", err)
	}

	completed, err := store.RecentCompletedItems(p.ID, 3600)
	if err != nil {
		t.Fatalf("RecentCompletedItems: %v", err)
	}
	if len(completed) != 1 {
		t.Fatalf("completed count = %d, want 1", len(completed))
	}
	if completed[0].FinalStatus != "Mrgd" {
		t.Errorf("final_status = %q, want Mrgd", completed[0].FinalStatus)
	}
	if completed[0].Summary != "Implement OAuth2 flow — OAuth2 flow implemented and merged" {
		t.Errorf("summary = %q, want combined objective + progress", completed[0].Summary)
	}
}

func TestArchiveTrackedItem_SkipsNoProgress(t *testing.T) {
	store := openTestDB(t)
	p := &Project{Name: "archiveskip", MinderIdentity: "archiveskip/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Item with no progress summary should NOT be archived.
	item := &TrackedItem{
		ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 2,
		ItemType: "issue", Title: "Accidental add", LastStatus: "Closd",
		ProgressSummary: "",
	}
	if err := store.AddTrackedItem(item); err != nil {
		t.Fatalf("AddTrackedItem: %v", err)
	}

	err := store.ArchiveTrackedItem(item)
	if err != nil {
		t.Fatalf("ArchiveTrackedItem: %v", err)
	}

	completed, _ := store.RecentCompletedItems(p.ID, 3600)
	if len(completed) != 0 {
		t.Errorf("completed count = %d, want 0 (no progress summary)", len(completed))
	}
}

func TestArchiveTerminalTrackedItems(t *testing.T) {
	store := openTestDB(t)
	p := &Project{Name: "archiveterminal", MinderIdentity: "archiveterminal/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// 2 open, 2 terminal (1 with progress, 1 without).
	if err := store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 1, LastStatus: "Open"}); err != nil {
		t.Fatalf("AddTrackedItem 1: %v", err)
	}
	if err := store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 2, LastStatus: "Open"}); err != nil {
		t.Fatalf("AddTrackedItem 2: %v", err)
	}
	item3 := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 3, LastStatus: "Mrgd"}
	if err := store.AddTrackedItem(item3); err != nil {
		t.Fatalf("AddTrackedItem 3: %v", err)
	}
	item3.ProgressSummary = "Feature shipped"
	if err := store.UpdateTrackedItem(item3); err != nil {
		t.Fatalf("UpdateTrackedItem item3: %v", err)
	}
	if err := store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 4, LastStatus: "Closd"}); err != nil {
		t.Fatalf("AddTrackedItem 4: %v", err)
	} // no progress

	removed, err := store.ArchiveTerminalTrackedItems(p.ID)
	if err != nil {
		t.Fatalf("ArchiveTerminalTrackedItems: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}

	// Only open items should remain.
	items, _ := store.GetTrackedItems(p.ID)
	if len(items) != 2 {
		t.Errorf("remaining tracked = %d, want 2", len(items))
	}

	// Only 1 should be archived (the one with progress).
	completed, _ := store.RecentCompletedItems(p.ID, 3600)
	if len(completed) != 1 {
		t.Errorf("completed count = %d, want 1", len(completed))
	}
	if len(completed) > 0 && completed[0].Number != 3 {
		t.Errorf("archived item number = %d, want 3", completed[0].Number)
	}
}

func TestPruneCompletedItems(t *testing.T) {
	store := openTestDB(t)
	p := &Project{Name: "prunecompleted", MinderIdentity: "prunecompleted/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Insert a completed item with an old timestamp.
	if _, err := store.db.Exec(`
		INSERT INTO completed_items (project_id, source, owner, repo, number, item_type, title, final_status, summary, completed_at)
		VALUES (?, 'github', 'org', 'repo', 1, 'issue', 'Old item', 'Closd', 'Done long ago', datetime('now', '-30 days'))
	`, p.ID); err != nil {
		t.Fatalf("insert old completed item: %v", err)
	}

	// Insert a recent one.
	if _, err := store.db.Exec(`
		INSERT INTO completed_items (project_id, source, owner, repo, number, item_type, title, final_status, summary, completed_at)
		VALUES (?, 'github', 'org', 'repo', 2, 'issue', 'Recent item', 'Mrgd', 'Just done', datetime('now'))
	`, p.ID); err != nil {
		t.Fatalf("insert recent completed item: %v", err)
	}

	// Prune items older than 14 days.
	pruned, err := store.PruneCompletedItems(p.ID, 14*24*3600)
	if err != nil {
		t.Fatalf("PruneCompletedItems: %v", err)
	}
	if pruned != 1 {
		t.Errorf("pruned = %d, want 1", pruned)
	}

	// Only the recent item should remain.
	remaining, _ := store.RecentCompletedItems(p.ID, 30*24*3600)
	if len(remaining) != 1 {
		t.Fatalf("remaining = %d, want 1", len(remaining))
	}
	if remaining[0].Number != 2 {
		t.Errorf("remaining item number = %d, want 2", remaining[0].Number)
	}
}

func TestPruneTrackedItems_ArchivesBeforeDelete(t *testing.T) {
	store := openTestDB(t)
	p := &Project{Name: "prunearchive", MinderIdentity: "prunearchive/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// 5 open + 5 terminal (with progress summaries).
	for i := 1; i <= 5; i++ {
		if err := store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Open"}); err != nil {
			t.Fatalf("AddTrackedItem %d: %v", i, err)
		}
	}
	for i := 6; i <= 10; i++ {
		item := &TrackedItem{
			ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i,
			LastStatus: "Closd", LastCheckedAt: fmt.Sprintf("2026-01-%02dT00:00:00Z", i),
		}
		if err := store.AddTrackedItem(item); err != nil {
			t.Fatalf("AddTrackedItem %d: %v", i, err)
		}
		item.ProgressSummary = fmt.Sprintf("Work done on item %d", i)
		if err := store.UpdateTrackedItem(item); err != nil {
			t.Fatalf("UpdateTrackedItem %d: %v", i, err)
		}
	}

	pruned, err := store.PruneTrackedItems(p.ID, 10, 2)
	if err != nil {
		t.Fatalf("PruneTrackedItems: %v", err)
	}
	if pruned != 1 {
		t.Errorf("pruned = %d, want 1", pruned)
	}

	// The pruned item (#6, oldest) should now be in completed_items.
	completed, _ := store.RecentCompletedItems(p.ID, 3600)
	if len(completed) != 1 {
		t.Fatalf("completed count = %d, want 1", len(completed))
	}
	if completed[0].Number != 6 {
		t.Errorf("archived item number = %d, want 6", completed[0].Number)
	}
}

func TestMigrationV9_AutopilotTasks(t *testing.T) {
	store := openTestDB(t)

	// Verify schema version is current.
	var version int
	if err := store.DB().Get(&version, "SELECT version FROM schema_version LIMIT 1"); err != nil {
		t.Fatalf("get version: %v", err)
	}
	if version != currentVersion {
		t.Errorf("version = %d, want %d", version, currentVersion)
	}

	// Create a project with autopilot fields.
	p := &Project{
		Name:                  "autopilot-test",
		GoalType:              "feature",
		GoalDescription:       "test autopilot",
		RefreshIntervalSec:    300,
		MessageTTLSec:         172800,
		LLMProvider:           "anthropic",
		LLMModel:              "claude-haiku-4-5",
		LLMSummarizerModel:    "claude-haiku-4-5",
		LLMAnalyzerModel:      "claude-sonnet-4-6",
		AutopilotMaxAgents:    5,
		AutopilotMaxTurns:     100,
		AutopilotMaxBudgetUSD: 5.50,
		AutopilotSkipLabel:    "skip-me",
	}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Verify autopilot fields persist.
	got, err := store.GetProject("autopilot-test")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.AutopilotMaxAgents != 5 {
		t.Errorf("AutopilotMaxAgents = %d, want 5", got.AutopilotMaxAgents)
	}
	if got.AutopilotMaxTurns != 100 {
		t.Errorf("AutopilotMaxTurns = %d, want 100", got.AutopilotMaxTurns)
	}
	if got.AutopilotMaxBudgetUSD != 5.50 {
		t.Errorf("AutopilotMaxBudgetUSD = %f, want 5.50", got.AutopilotMaxBudgetUSD)
	}
	if got.AutopilotSkipLabel != "skip-me" {
		t.Errorf("AutopilotSkipLabel = %q, want %q", got.AutopilotSkipLabel, "skip-me")
	}
}

func TestAutopilotTasksCRUD(t *testing.T) {
	store := openTestDB(t)

	p := &Project{
		Name:               "ap-crud",
		GoalType:           "feature",
		GoalDescription:    "test",
		RefreshIntervalSec: 300,
		MessageTTLSec:      172800,
		LLMProvider:        "anthropic",
		LLMModel:           "claude-haiku-4-5",
		LLMSummarizerModel: "claude-haiku-4-5",
		LLMAnalyzerModel:   "claude-sonnet-4-6",
	}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Create a task.
	task := &AutopilotTask{
		ProjectID:    p.ID,
		IssueNumber:  42,
		IssueTitle:   "Add auth",
		IssueBody:    "Implement OAuth",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatalf("CreateAutopilotTask: %v", err)
	}
	if task.ID == 0 {
		t.Fatal("expected non-zero ID")
	}

	// Get tasks.
	tasks, err := store.GetAutopilotTasks(p.ID)
	if err != nil {
		t.Fatalf("GetAutopilotTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if tasks[0].IssueNumber != 42 {
		t.Errorf("IssueNumber = %d, want 42", tasks[0].IssueNumber)
	}

	// Update status.
	if err := store.UpdateAutopilotTaskStatus(task.ID, "running"); err != nil {
		t.Fatalf("UpdateAutopilotTaskStatus: %v", err)
	}
	tasks, _ = store.GetAutopilotTasks(p.ID)
	if tasks[0].Status != "running" {
		t.Errorf("Status = %q, want running", tasks[0].Status)
	}

	// Update running info.
	if err := store.UpdateAutopilotTaskRunning(task.ID, "/tmp/wt", "agent/issue-42", "/tmp/log"); err != nil {
		t.Fatalf("UpdateAutopilotTaskRunning: %v", err)
	}
	tasks, _ = store.GetAutopilotTasks(p.ID)
	if tasks[0].WorktreePath != "/tmp/wt" {
		t.Errorf("WorktreePath = %q, want /tmp/wt", tasks[0].WorktreePath)
	}

	// Update PR number.
	if err := store.UpdateAutopilotTaskPR(task.ID, 99); err != nil {
		t.Fatalf("UpdateAutopilotTaskPR: %v", err)
	}
	tasks, _ = store.GetAutopilotTasks(p.ID)
	if tasks[0].PRNumber != 99 {
		t.Errorf("PRNumber = %d, want 99", tasks[0].PRNumber)
	}

	// Mark done.
	if err := store.UpdateAutopilotTaskStatus(task.ID, "done"); err != nil {
		t.Fatalf("UpdateAutopilotTaskStatus done: %v", err)
	}
	tasks, _ = store.GetAutopilotTasks(p.ID)
	if tasks[0].Status != "done" {
		t.Errorf("Status = %q, want done", tasks[0].Status)
	}
	if tasks[0].CompletedAt == "" {
		t.Error("CompletedAt should be set when done")
	}

	// Clear.
	if err := store.ClearAutopilotTasks(p.ID); err != nil {
		t.Fatalf("ClearAutopilotTasks: %v", err)
	}
	tasks, _ = store.GetAutopilotTasks(p.ID)
	if len(tasks) != 0 {
		t.Errorf("got %d tasks after clear, want 0", len(tasks))
	}
}

func TestBulkCreateAutopilotTasks(t *testing.T) {
	store := openTestDB(t)

	p := &Project{
		Name:               "ap-bulk",
		GoalType:           "feature",
		GoalDescription:    "test",
		RefreshIntervalSec: 300,
		MessageTTLSec:      172800,
		LLMProvider:        "anthropic",
		LLMModel:           "claude-haiku-4-5",
		LLMSummarizerModel: "claude-haiku-4-5",
		LLMAnalyzerModel:   "claude-sonnet-4-6",
	}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	tasks := []*AutopilotTask{
		{ProjectID: p.ID, IssueNumber: 1, IssueTitle: "Task 1", Dependencies: "[]", Status: "queued"},
		{ProjectID: p.ID, IssueNumber: 2, IssueTitle: "Task 2", Dependencies: "[]", Status: "queued"},
		{ProjectID: p.ID, IssueNumber: 1, IssueTitle: "Task 1 dup", Dependencies: "[]", Status: "queued"}, // duplicate
	}

	inserted, err := store.BulkCreateAutopilotTasks(tasks)
	if err != nil {
		t.Fatalf("BulkCreateAutopilotTasks: %v", err)
	}
	if inserted != 2 {
		t.Errorf("inserted = %d, want 2 (1 duplicate)", inserted)
	}

	got, _ := store.GetAutopilotTasks(p.ID)
	if len(got) != 2 {
		t.Errorf("got %d tasks, want 2", len(got))
	}
}

func TestQueuedUnblockedTasks(t *testing.T) {
	store := openTestDB(t)

	p := &Project{
		Name:               "ap-deps",
		GoalType:           "feature",
		GoalDescription:    "test",
		RefreshIntervalSec: 300,
		MessageTTLSec:      172800,
		LLMProvider:        "anthropic",
		LLMModel:           "claude-haiku-4-5",
		LLMSummarizerModel: "claude-haiku-4-5",
		LLMAnalyzerModel:   "claude-sonnet-4-6",
	}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Task 1: no deps (should be unblocked).
	t1 := &AutopilotTask{ProjectID: p.ID, IssueNumber: 1, IssueTitle: "Base", Dependencies: "[]", Status: "queued"}
	// Task 2: depends on task 1 (should be blocked).
	t2 := &AutopilotTask{ProjectID: p.ID, IssueNumber: 2, IssueTitle: "Depends on 1", Dependencies: "[1]", Status: "queued"}
	// Task 3: no deps (should be unblocked).
	t3 := &AutopilotTask{ProjectID: p.ID, IssueNumber: 3, IssueTitle: "Independent", Dependencies: "[]", Status: "queued"}

	if err := store.CreateAutopilotTask(t1); err != nil {
		t.Fatalf("CreateAutopilotTask t1: %v", err)
	}
	if err := store.CreateAutopilotTask(t2); err != nil {
		t.Fatalf("CreateAutopilotTask t2: %v", err)
	}
	if err := store.CreateAutopilotTask(t3); err != nil {
		t.Fatalf("CreateAutopilotTask t3: %v", err)
	}

	unblocked, err := store.QueuedUnblockedTasks(p.ID)
	if err != nil {
		t.Fatalf("QueuedUnblockedTasks: %v", err)
	}
	if len(unblocked) != 2 {
		t.Fatalf("got %d unblocked, want 2", len(unblocked))
	}

	// Complete task 1.
	if err := store.UpdateAutopilotTaskStatus(t1.ID, "done"); err != nil {
		t.Fatalf("UpdateAutopilotTaskStatus: %v", err)
	}

	// Now task 2 should also be unblocked.
	unblocked, err = store.QueuedUnblockedTasks(p.ID)
	if err != nil {
		t.Fatalf("QueuedUnblockedTasks after done: %v", err)
	}
	if len(unblocked) != 2 {
		t.Fatalf("got %d unblocked after done, want 2 (tasks 2 and 3)", len(unblocked))
	}
}

func TestParseDependencies(t *testing.T) {
	tests := []struct {
		input string
		want  []int
	}{
		{"[]", nil},
		{"", nil},
		{"[1]", []int{1}},
		{"[1, 2, 3]", []int{1, 2, 3}},
		{"[42]", []int{42}},
		{"invalid", nil},
	}

	for _, tt := range tests {
		got := parseDependencies(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("parseDependencies(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("parseDependencies(%q)[%d] = %d, want %d", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestRepoOnboardingCRUD(t *testing.T) {
	store := openTestDB(t)

	// Create project and repo.
	p := &Project{Name: "onboard-test", MinderIdentity: "onboard-test/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	r := &Repo{ProjectID: p.ID, Path: "/tmp/myrepo", ShortName: "myrepo"}
	if err := store.AddRepo(r); err != nil {
		t.Fatalf("AddRepo: %v", err)
	}

	// Upsert onboarding.
	e := &RepoOnboarding{
		RepoID:           r.ID,
		OnboardingYAML:   "version: 1\ninventory:\n  languages: [go]\n",
		ValidationStatus: "untested",
	}
	if err := store.UpsertRepoOnboarding(e); err != nil {
		t.Fatalf("UpsertRepoOnboarding: %v", err)
	}
	if e.ID == 0 {
		t.Error("expected non-zero ID after upsert")
	}

	// Get by repo ID.
	got, err := store.GetRepoOnboarding(r.ID)
	if err != nil {
		t.Fatalf("GetRepoOnboarding: %v", err)
	}
	if got.ValidationStatus != "untested" {
		t.Errorf("ValidationStatus = %q, want %q", got.ValidationStatus, "untested")
	}
	if got.OnboardingYAML == "" {
		t.Error("OnboardingYAML should not be empty")
	}
	if got.OnboardedAt == "" {
		t.Error("OnboardedAt should be set")
	}

	// Update validation status.
	if err := store.UpdateRepoOnboardingValidation(r.ID, "pass"); err != nil {
		t.Fatalf("UpdateRepoOnboardingValidation: %v", err)
	}
	got, _ = store.GetRepoOnboarding(r.ID)
	if got.ValidationStatus != "pass" {
		t.Errorf("ValidationStatus = %q, want %q", got.ValidationStatus, "pass")
	}
	if got.ValidatedAt == "" {
		t.Error("ValidatedAt should be set after validation update")
	}

	// Upsert again (should update, not duplicate).
	e2 := &RepoOnboarding{
		RepoID:           r.ID,
		OnboardingYAML:   "version: 1\ninventory:\n  languages: [go, python]\n",
		ValidationStatus: "untested",
	}
	if err := store.UpsertRepoOnboarding(e2); err != nil {
		t.Fatalf("UpsertRepoOnboarding second: %v", err)
	}
	got, _ = store.GetRepoOnboarding(r.ID)
	if got.OnboardingYAML != e2.OnboardingYAML {
		t.Errorf("OnboardingYAML not updated after second upsert")
	}

	// Get by project.
	records, err := store.GetRepoOnboardings(p.ID)
	if err != nil {
		t.Fatalf("GetRepoOnboardings: %v", err)
	}
	if len(records) != 1 {
		t.Errorf("got %d onboarding records, want 1", len(records))
	}

	// Delete.
	if err := store.DeleteRepoOnboarding(r.ID); err != nil {
		t.Fatalf("DeleteRepoOnboarding: %v", err)
	}
	records, _ = store.GetRepoOnboardings(p.ID)
	if len(records) != 0 {
		t.Errorf("got %d onboarding records after delete, want 0", len(records))
	}
}

func TestRepoOnboardingCascadeDelete(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "onboard-cascade", MinderIdentity: "onboard-cascade/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	r := &Repo{ProjectID: p.ID, Path: "/tmp/cascade-repo", ShortName: "cascade"}
	if err := store.AddRepo(r); err != nil {
		t.Fatalf("AddRepo: %v", err)
	}

	e := &RepoOnboarding{
		RepoID:           r.ID,
		OnboardingYAML:   "version: 1\n",
		ValidationStatus: "untested",
	}
	if err := store.UpsertRepoOnboarding(e); err != nil {
		t.Fatalf("UpsertRepoOnboarding: %v", err)
	}

	// Delete the repo — onboarding should cascade.
	if err := store.DeleteRepo(r.ID); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}

	records, _ := store.GetRepoOnboardings(p.ID)
	if len(records) != 0 {
		t.Errorf("got %d onboarding records after repo delete, want 0 (cascade)", len(records))
	}
}

func TestPerTierProviderColumns(t *testing.T) {
	store := openTestDB(t)

	// Create project without setting per-tier providers — they should default to "".
	p := &Project{
		Name:               "tier-test",
		MinderIdentity:     "tier-test/minder",
		LLMProvider:        "anthropic",
		LLMModel:           "claude-haiku-4-5",
		LLMSummarizerModel: "claude-haiku-4-5",
		LLMAnalyzerModel:   "claude-sonnet-4-6",
	}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	got, err := store.GetProject("tier-test")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.LLMSummarizerProvider != "" {
		t.Errorf("LLMSummarizerProvider = %q, want empty", got.LLMSummarizerProvider)
	}
	if got.LLMAnalyzerProvider != "" {
		t.Errorf("LLMAnalyzerProvider = %q, want empty", got.LLMAnalyzerProvider)
	}

	// Update with per-tier providers.
	got.LLMSummarizerProvider = "openai"
	got.LLMAnalyzerProvider = "anthropic"
	if err := store.UpdateProject(got); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}

	got2, _ := store.GetProjectByID(got.ID)
	if got2.LLMSummarizerProvider != "openai" {
		t.Errorf("after update LLMSummarizerProvider = %q, want %q", got2.LLMSummarizerProvider, "openai")
	}
	if got2.LLMAnalyzerProvider != "anthropic" {
		t.Errorf("after update LLMAnalyzerProvider = %q, want %q", got2.LLMAnalyzerProvider, "anthropic")
	}
}

// --- Cost Aggregation Tests ---

func createCostTestProject(t *testing.T, store *Store, name string) *Project {
	t.Helper()
	p := &Project{
		Name:               name,
		GoalType:           "feature",
		GoalDescription:    "cost test",
		RefreshIntervalSec: 300,
		MessageTTLSec:      172800,
		LLMProvider:        "anthropic",
		LLMModel:           "claude-haiku-4-5",
		LLMSummarizerModel: "claude-haiku-4-5",
		LLMAnalyzerModel:   "claude-sonnet-4-6",
	}
	if err := store.CreateProject(p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return p
}

func createTaskWithCost(t *testing.T, store *Store, projectID int64, issueNum int, status string, cost float64, completedAt string) {
	t.Helper()
	task := &AutopilotTask{
		ProjectID:    projectID,
		IssueNumber:  issueNum,
		IssueTitle:   fmt.Sprintf("Issue #%d", issueNum),
		IssueBody:    "test",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatalf("CreateAutopilotTask: %v", err)
	}
	// Set cost.
	if err := store.UpdateAutopilotTaskCost(task.ID, cost); err != nil {
		t.Fatalf("UpdateAutopilotTaskCost: %v", err)
	}
	// Set status and completed_at directly via SQL for test control.
	if _, err := store.DB().Exec(`UPDATE autopilot_tasks SET status = ?, completed_at = ? WHERE id = ?`, status, completedAt, task.ID); err != nil {
		t.Fatalf("set status+completed_at: %v", err)
	}
}

func TestUpdateAutopilotTaskCost(t *testing.T) {
	store := openTestDB(t)
	p := createCostTestProject(t, store, "cost-update")

	task := &AutopilotTask{
		ProjectID:    p.ID,
		IssueNumber:  1,
		IssueTitle:   "Test",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatalf("CreateAutopilotTask: %v", err)
	}

	// Initially zero.
	tasks, _ := store.GetAutopilotTasks(p.ID)
	if tasks[0].CostUSD != 0 {
		t.Errorf("CostUSD = %f, want 0", tasks[0].CostUSD)
	}

	// Update cost.
	if err := store.UpdateAutopilotTaskCost(task.ID, 1.25); err != nil {
		t.Fatalf("UpdateAutopilotTaskCost: %v", err)
	}
	tasks, _ = store.GetAutopilotTasks(p.ID)
	if tasks[0].CostUSD != 1.25 {
		t.Errorf("CostUSD = %f, want 1.25", tasks[0].CostUSD)
	}
}

func TestDailyCost(t *testing.T) {
	store := openTestDB(t)
	p := createCostTestProject(t, store, "daily-cost")

	// Two tasks completed today, one yesterday.
	createTaskWithCost(t, store, p.ID, 1, "done", 1.50, "2026-03-18 10:00:00")
	createTaskWithCost(t, store, p.ID, 2, "bailed", 0.75, "2026-03-18 15:30:00")
	createTaskWithCost(t, store, p.ID, 3, "done", 2.00, "2026-03-17 12:00:00")

	// Daily cost for 2026-03-18.
	cs, err := store.DailyCost(p.ID, "2026-03-18")
	if err != nil {
		t.Fatalf("DailyCost: %v", err)
	}
	if cs.TotalCost != 2.25 {
		t.Errorf("TotalCost = %f, want 2.25", cs.TotalCost)
	}
	if cs.TaskCount != 2 {
		t.Errorf("TaskCount = %d, want 2", cs.TaskCount)
	}

	// Daily cost for 2026-03-17.
	cs, err = store.DailyCost(p.ID, "2026-03-17")
	if err != nil {
		t.Fatalf("DailyCost: %v", err)
	}
	if cs.TotalCost != 2.00 {
		t.Errorf("TotalCost = %f, want 2.00", cs.TotalCost)
	}
	if cs.TaskCount != 1 {
		t.Errorf("TaskCount = %d, want 1", cs.TaskCount)
	}
}

func TestDailyCost_ExcludesQueuedAndRunning(t *testing.T) {
	store := openTestDB(t)
	p := createCostTestProject(t, store, "daily-excl")

	// A queued task and a running task should not count.
	createTaskWithCost(t, store, p.ID, 1, "queued", 0.50, "2026-03-18 10:00:00")
	createTaskWithCost(t, store, p.ID, 2, "running", 0.75, "2026-03-18 10:00:00")
	createTaskWithCost(t, store, p.ID, 3, "done", 1.00, "2026-03-18 10:00:00")

	cs, err := store.DailyCost(p.ID, "2026-03-18")
	if err != nil {
		t.Fatalf("DailyCost: %v", err)
	}
	if cs.TotalCost != 1.00 {
		t.Errorf("TotalCost = %f, want 1.00 (only done task)", cs.TotalCost)
	}
	if cs.TaskCount != 1 {
		t.Errorf("TaskCount = %d, want 1", cs.TaskCount)
	}
}

func TestWeeklyCost(t *testing.T) {
	store := openTestDB(t)
	p := createCostTestProject(t, store, "weekly-cost")

	// Tasks across a week.
	createTaskWithCost(t, store, p.ID, 1, "done", 1.00, "2026-03-18 10:00:00") // today (Wed)
	createTaskWithCost(t, store, p.ID, 2, "done", 2.00, "2026-03-15 10:00:00") // Sun (within 7 days)
	createTaskWithCost(t, store, p.ID, 3, "done", 3.00, "2026-03-12 10:00:00") // Thu (exactly 6 days ago from 18th)
	createTaskWithCost(t, store, p.ID, 4, "done", 4.00, "2026-03-11 10:00:00") // Wed (7 days ago = outside)

	// Weekly cost ending on 2026-03-18 (covers 03-12 through 03-18).
	cs, err := store.WeeklyCost(p.ID, "2026-03-18")
	if err != nil {
		t.Fatalf("WeeklyCost: %v", err)
	}
	// Should include tasks 1, 2, 3 (1.00 + 2.00 + 3.00 = 6.00).
	if cs.TotalCost != 6.00 {
		t.Errorf("TotalCost = %f, want 6.00", cs.TotalCost)
	}
	if cs.TaskCount != 3 {
		t.Errorf("TaskCount = %d, want 3", cs.TaskCount)
	}
}

func TestOverallCost(t *testing.T) {
	store := openTestDB(t)
	p := createCostTestProject(t, store, "overall-cost")

	createTaskWithCost(t, store, p.ID, 1, "done", 1.50, "2026-01-15 10:00:00")
	createTaskWithCost(t, store, p.ID, 2, "failed", 0.50, "2026-02-20 10:00:00")
	createTaskWithCost(t, store, p.ID, 3, "done", 2.00, "2026-03-18 10:00:00")
	createTaskWithCost(t, store, p.ID, 4, "queued", 0.00, "")  // queued: excluded
	createTaskWithCost(t, store, p.ID, 5, "running", 0.00, "") // running: excluded

	cs, err := store.OverallCost(p.ID)
	if err != nil {
		t.Fatalf("OverallCost: %v", err)
	}
	if cs.TotalCost != 4.00 {
		t.Errorf("TotalCost = %f, want 4.00", cs.TotalCost)
	}
	if cs.TaskCount != 3 {
		t.Errorf("TaskCount = %d, want 3", cs.TaskCount)
	}
}

func TestOverallCost_EmptyProject(t *testing.T) {
	store := openTestDB(t)
	p := createCostTestProject(t, store, "overall-empty")

	cs, err := store.OverallCost(p.ID)
	if err != nil {
		t.Fatalf("OverallCost: %v", err)
	}
	if cs.TotalCost != 0 {
		t.Errorf("TotalCost = %f, want 0", cs.TotalCost)
	}
	if cs.TaskCount != 0 {
		t.Errorf("TaskCount = %d, want 0", cs.TaskCount)
	}
}

func TestDailyTaskCosts(t *testing.T) {
	store := openTestDB(t)
	p := createCostTestProject(t, store, "daily-details")

	createTaskWithCost(t, store, p.ID, 10, "done", 1.50, "2026-03-18 09:00:00")
	createTaskWithCost(t, store, p.ID, 20, "bailed", 0.25, "2026-03-18 14:00:00")
	createTaskWithCost(t, store, p.ID, 30, "done", 3.00, "2026-03-17 12:00:00") // different day

	details, err := store.DailyTaskCosts(p.ID, "2026-03-18")
	if err != nil {
		t.Fatalf("DailyTaskCosts: %v", err)
	}
	if len(details) != 2 {
		t.Fatalf("got %d details, want 2", len(details))
	}
	// Should be ordered by completed_at DESC.
	if details[0].IssueNumber != 20 {
		t.Errorf("first detail issue = %d, want 20 (most recent)", details[0].IssueNumber)
	}
	if details[1].IssueNumber != 10 {
		t.Errorf("second detail issue = %d, want 10", details[1].IssueNumber)
	}
	if details[0].CostUSD != 0.25 {
		t.Errorf("detail[0].CostUSD = %f, want 0.25", details[0].CostUSD)
	}
}

func TestCostAggregation_IsolatedByProject(t *testing.T) {
	store := openTestDB(t)
	p1 := createCostTestProject(t, store, "cost-iso-1")
	p2 := createCostTestProject(t, store, "cost-iso-2")

	createTaskWithCost(t, store, p1.ID, 1, "done", 5.00, "2026-03-18 10:00:00")
	createTaskWithCost(t, store, p2.ID, 1, "done", 3.00, "2026-03-18 10:00:00")

	cs1, _ := store.OverallCost(p1.ID)
	cs2, _ := store.OverallCost(p2.ID)

	if cs1.TotalCost != 5.00 {
		t.Errorf("p1 TotalCost = %f, want 5.00", cs1.TotalCost)
	}
	if cs2.TotalCost != 3.00 {
		t.Errorf("p2 TotalCost = %f, want 3.00", cs2.TotalCost)
	}
}

func TestCostAggregation_AllTerminalStatuses(t *testing.T) {
	store := openTestDB(t)
	p := createCostTestProject(t, store, "cost-statuses")

	// All terminal statuses should be included.
	createTaskWithCost(t, store, p.ID, 1, "done", 1.00, "2026-03-18 10:00:00")
	createTaskWithCost(t, store, p.ID, 2, "bailed", 0.50, "2026-03-18 10:00:00")
	createTaskWithCost(t, store, p.ID, 3, "failed", 0.75, "2026-03-18 10:00:00")
	createTaskWithCost(t, store, p.ID, 4, "stopped", 0.25, "2026-03-18 10:00:00")

	cs, err := store.OverallCost(p.ID)
	if err != nil {
		t.Fatalf("OverallCost: %v", err)
	}
	if cs.TotalCost != 2.50 {
		t.Errorf("TotalCost = %f, want 2.50", cs.TotalCost)
	}
	if cs.TaskCount != 4 {
		t.Errorf("TaskCount = %d, want 4", cs.TaskCount)
	}
}
