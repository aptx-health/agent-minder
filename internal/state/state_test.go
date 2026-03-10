package state

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleState = `# Minder State: ripit

## Watched Repos
- ~/repos/ripit-app — Next.js app, Prisma schema, active branch: feature/auth
- ~/repos/ripit-infra — k3s infra, ArgoCD, currently on main

## Active Concerns
- ripit-app is modifying User schema — ripit-infra worker queries may need updating
- k3s cluster is bootstrapped, waiting for Docker image from app

## Recent Activity
- [2026-03-01 22:42] ripit-infra: PostgreSQL + Redis deployed to cluster
- [2026-03-02 00:38] ripit-app: Fixed local dev postgres persistence

## Monitoring Plan
- Watch for schema changes in ripit-app (Prisma migrations dir)
- Watch for new messages on ripit/*

## Last Poll
- Time: 2026-03-02 01:15 UTC
- Messages checked: 0 new
- Git activity: none since last poll
`

func TestParse(t *testing.T) {
	s := Parse("ripit", sampleState)

	if s.Project != "ripit" {
		t.Errorf("Project = %q, want %q", s.Project, "ripit")
	}
	if len(s.WatchedRepos) != 2 {
		t.Errorf("WatchedRepos len = %d, want 2", len(s.WatchedRepos))
	}
	if len(s.ActiveConcerns) != 2 {
		t.Errorf("ActiveConcerns len = %d, want 2", len(s.ActiveConcerns))
	}
	if len(s.RecentActivity) != 2 {
		t.Errorf("RecentActivity len = %d, want 2", len(s.RecentActivity))
	}
	if len(s.MonitoringPlan) != 2 {
		t.Errorf("MonitoringPlan len = %d, want 2", len(s.MonitoringPlan))
	}
	if s.LastPollTime != "2026-03-02 01:15 UTC" {
		t.Errorf("LastPollTime = %q, want %q", s.LastPollTime, "2026-03-02 01:15 UTC")
	}
	if len(s.LastPollNotes) != 3 {
		t.Errorf("LastPollNotes len = %d, want 3", len(s.LastPollNotes))
	}
}

func TestParseEmpty(t *testing.T) {
	s := Parse("empty", "")
	if s.Project != "empty" {
		t.Errorf("Project = %q, want %q", s.Project, "empty")
	}
	if len(s.ActiveConcerns) != 0 {
		t.Error("expected empty ActiveConcerns")
	}
}

func TestSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	err := Save("testproj", sampleState)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file exists.
	path := filepath.Join(tmpDir, ".agent-minder", "testproj", "state.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file not created: %v", err)
	}

	s, err := Load("testproj")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.WatchedRepos) != 2 {
		t.Errorf("WatchedRepos len = %d, want 2", len(s.WatchedRepos))
	}
}

func TestLoadNonexistent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	s, err := Load("nope")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Project != "nope" {
		t.Errorf("Project = %q, want %q", s.Project, "nope")
	}
	if len(s.ActiveConcerns) != 0 {
		t.Error("expected empty state for nonexistent project")
	}
}

func TestExists(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	exists, err := Exists("nope")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Error("expected false for nonexistent project")
	}

	Save("testproj", "content")
	exists, err = Exists("testproj")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Error("expected true after Save")
	}
}
