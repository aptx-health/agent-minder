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
	t.Cleanup(func() { conn.Close() })
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
	conn1.Close()

	conn2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	conn2.Close()
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
	store.CreateProject(p)

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
	store.ReplaceWorktrees(r.ID, []Worktree{{Path: "/tmp/myapp", Branch: "main"}})
	got, _ = store.GetWorktrees(r.ID)
	if len(got) != 1 {
		t.Errorf("after replace GetWorktrees len = %d, want 1", len(got))
	}

	// Cascade delete.
	store.DeleteProject(p.ID)
	repos, _ = store.GetRepos(p.ID)
	if len(repos) != 0 {
		t.Error("repos should be deleted with project")
	}
}

func TestGetWorktreesForProject(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "wtp", MinderIdentity: "wtp/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	store.CreateProject(p)

	r1 := &Repo{ProjectID: p.ID, Path: "/tmp/app", ShortName: "app"}
	store.AddRepo(r1)
	r2 := &Repo{ProjectID: p.ID, Path: "/tmp/lib", ShortName: "lib"}
	store.AddRepo(r2)

	store.ReplaceWorktrees(r1.ID, []Worktree{
		{Path: "/tmp/app", Branch: "main"},
		{Path: "/tmp/app-feat", Branch: "feature/auth"},
	})
	store.ReplaceWorktrees(r2.ID, []Worktree{
		{Path: "/tmp/lib", Branch: "main"},
	})

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
	store.CreateProject(p2)
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
	store.CreateProject(p)

	for _, name := range []string{"ttest/app", "ttest/infra", "ttest/coord"} {
		store.AddTopic(&Topic{ProjectID: p.ID, Name: name})
	}

	topics, _ := store.GetTopics(p.ID)
	if len(topics) != 3 {
		t.Errorf("GetTopics len = %d, want 3", len(topics))
	}
}

func TestConcerns(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "ctest", MinderIdentity: "ctest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	store.CreateProject(p)

	c := &Concern{ProjectID: p.ID, Severity: "warning", Message: "Schema drift detected"}
	store.AddConcern(c)

	active, _ := store.ActiveConcerns(p.ID)
	if len(active) != 1 {
		t.Fatalf("ActiveConcerns len = %d, want 1", len(active))
	}

	store.ResolveConcern(c.ID)
	active, _ = store.ActiveConcerns(p.ID)
	if len(active) != 0 {
		t.Errorf("after resolve ActiveConcerns len = %d, want 0", len(active))
	}
}

func TestPolls(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "ptest", MinderIdentity: "ptest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	store.CreateProject(p)

	store.RecordPoll(&Poll{ProjectID: p.ID, NewCommits: 3, NewMessages: 1, LLMResponseRaw: "All good", Tier1Response: "Summary", Tier2Response: "Analysis"})
	store.RecordPoll(&Poll{ProjectID: p.ID, NewCommits: 0, NewMessages: 0, LLMResponseRaw: "Nothing new", Tier1Response: "No activity"})

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
	conn.Close()

	// Re-open with current migration code — should migrate to v2.
	conn2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open after v1: %v", err)
	}
	defer conn2.Close()
	store := NewStore(conn2)

	// Check schema version.
	var version int
	store.DB().Get(&version, "SELECT version FROM schema_version LIMIT 1")
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
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schemaV1_only); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("INSERT INTO schema_version (version) VALUES (1)"); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func TestTrackedItemsCRUD(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "titest", MinderIdentity: "titest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	store.CreateProject(p)

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
	store.CreateProject(p)

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
	store.CreateProject(p)

	store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "o", Repo: "r", Number: 1, LastStatus: "Open"})

	// Delete project should cascade.
	store.DeleteProject(p.ID)
	items, _ := store.GetTrackedItems(p.ID)
	if len(items) != 0 {
		t.Error("tracked items should be deleted with project")
	}
}

func TestBulkAddTrackedItems(t *testing.T) {
	store := openTestDB(t)

	p := &Project{Name: "bulktest", MinderIdentity: "bulktest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5", LLMSummarizerModel: "claude-haiku-4-5", LLMAnalyzerModel: "claude-sonnet-4-6"}
	store.CreateProject(p)

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
	store.CreateProject(p)

	// Add 18 items individually.
	for i := 1; i <= 18; i++ {
		store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Open"})
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
	store.CreateProject(p)

	for i := 1; i <= 5; i++ {
		store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Open"})
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
	conn.Close()

	// Re-open with current migration code — should migrate to v4.
	conn2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open after v3: %v", err)
	}
	defer conn2.Close()
	store := NewStore(conn2)

	// Check schema version.
	var version int
	store.DB().Get(&version, "SELECT version FROM schema_version LIMIT 1")
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
	conn.Close()

	// Re-open with current migration code — should migrate to v5.
	conn2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open after v4: %v", err)
	}
	defer conn2.Close()
	store := NewStore(conn2)

	// Check schema version.
	var version int
	store.DB().Get(&version, "SELECT version FROM schema_version LIMIT 1")
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
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schemaV4_only); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("INSERT INTO schema_version (version) VALUES (4)"); err != nil {
		db.Close()
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
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schemaV3_only); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("INSERT INTO schema_version (version) VALUES (3)"); err != nil {
		db.Close()
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
	store.CreateProject(p)

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
	store.CreateProject(p)

	for i := 1; i <= 5; i++ {
		item := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Closd"}
		store.AddTrackedItem(item)
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
	store.CreateProject(p)

	for i := 1; i <= 10; i++ {
		item := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Open"}
		store.AddTrackedItem(item)
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
	store.CreateProject(p)

	// 8 open + 2 terminal = 10 total. keepTerminal=2 means 0 removable.
	for i := 1; i <= 8; i++ {
		item := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Open"}
		store.AddTrackedItem(item)
	}
	for i := 9; i <= 10; i++ {
		item := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Mrgd"}
		store.AddTrackedItem(item)
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
	store.CreateProject(p)

	// Add 3 open, 2 closed, 1 merged, 1 not-planned.
	for i := 1; i <= 3; i++ {
		store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Open"})
	}
	store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 4, LastStatus: "Closd"})
	store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 5, LastStatus: "Mrgd"})
	store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 6, LastStatus: "Closd"})
	store.AddTrackedItem(&TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 7, LastStatus: "NotPl"})

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
