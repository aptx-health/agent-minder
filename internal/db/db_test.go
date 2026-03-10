package db

import (
	"path/filepath"
	"testing"
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

	// Update.
	got.GoalType = "bugfix"
	if err := store.UpdateProject(got); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	got2, _ := store.GetProjectByID(got.ID)
	if got2.GoalType != "bugfix" {
		t.Errorf("after update GoalType = %q, want %q", got2.GoalType, "bugfix")
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

	p := &Project{Name: "rtest", MinderIdentity: "rtest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5"}
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

	p := &Project{Name: "ttest", MinderIdentity: "ttest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5"}
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

	p := &Project{Name: "ctest", MinderIdentity: "ctest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5"}
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

	p := &Project{Name: "ptest", MinderIdentity: "ptest/minder", LLMProvider: "anthropic", LLMModel: "claude-haiku-4-5"}
	store.CreateProject(p)

	store.RecordPoll(&Poll{ProjectID: p.ID, NewCommits: 3, NewMessages: 1, LLMResponse: "All good"})
	store.RecordPoll(&Poll{ProjectID: p.ID, NewCommits: 0, NewMessages: 0, LLMResponse: "Nothing new"})

	polls, _ := store.RecentPolls(p.ID, 5)
	if len(polls) != 2 {
		t.Fatalf("RecentPolls len = %d, want 2", len(polls))
	}
	// Most recent first.
	if polls[0].LLMResponse != "Nothing new" {
		t.Errorf("first poll = %q, want 'Nothing new'", polls[0].LLMResponse)
	}

	last, _ := store.LastPoll(p.ID)
	if last == nil {
		t.Fatal("LastPoll returned nil")
	}
	if last.NewCommits != 0 {
		t.Errorf("LastPoll.NewCommits = %d, want 0", last.NewCommits)
	}
}
