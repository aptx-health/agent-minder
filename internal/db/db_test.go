package db

import (
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

	// Update.
	items[0].State = "closed"
	items[0].LastStatus = "Closd"
	items[0].LastCheckedAt = "2026-03-12T00:00:00Z"
	if err := store.UpdateTrackedItem(&items[0]); err != nil {
		t.Fatalf("UpdateTrackedItem: %v", err)
	}
	items, _ = store.GetTrackedItems(p.ID)
	if items[0].LastStatus != "Closd" {
		t.Errorf("after update LastStatus = %q, want Closd", items[0].LastStatus)
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

	// Add 10 items.
	for i := 1; i <= 10; i++ {
		item := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: i, LastStatus: "Open"}
		if err := store.AddTrackedItem(item); err != nil {
			t.Fatalf("AddTrackedItem %d: %v", i, err)
		}
	}

	// 11th should fail.
	item := &TrackedItem{ProjectID: p.ID, Source: "github", Owner: "org", Repo: "repo", Number: 11, LastStatus: "Open"}
	if err := store.AddTrackedItem(item); err == nil {
		t.Error("expected 11th item to be rejected")
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
