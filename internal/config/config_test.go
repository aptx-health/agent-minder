package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewProjectDefaults(t *testing.T) {
	p := NewProject("myproject")

	if p.Name != "myproject" {
		t.Errorf("Name = %q, want %q", p.Name, "myproject")
	}
	if p.RefreshInterval != 5*time.Minute {
		t.Errorf("RefreshInterval = %v, want %v", p.RefreshInterval, 5*time.Minute)
	}
	if p.MessageTTL != 48*time.Hour {
		t.Errorf("MessageTTL = %v, want %v", p.MessageTTL, 48*time.Hour)
	}
	if !p.AutoEnrollWorktrees {
		t.Error("AutoEnrollWorktrees should default to true")
	}
	if p.MinderIdentity != "myproject/minder" {
		t.Errorf("MinderIdentity = %q, want %q", p.MinderIdentity, "myproject/minder")
	}
}

func TestSaveAndLoad(t *testing.T) {
	// Use a temp dir as home so we don't pollute real config.
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	p := NewProject("testproj")
	p.Repos = []Repo{
		{
			Path:      "/tmp/repo-app",
			ShortName: "app",
			Worktrees: []Worktree{
				{Path: "/tmp/repo-app", Branch: "main"},
			},
		},
	}
	p.Topics = []string{"testproj/app", "testproj/coord"}

	if err := Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file exists.
	configPath := filepath.Join(tmpDir, ".agent-minder", "testproj", "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	// Load it back.
	loaded, err := Load("testproj")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.Name != p.Name {
		t.Errorf("Name = %q, want %q", loaded.Name, p.Name)
	}
	if loaded.RefreshInterval != p.RefreshInterval {
		t.Errorf("RefreshInterval = %v, want %v", loaded.RefreshInterval, p.RefreshInterval)
	}
	if loaded.MessageTTL != p.MessageTTL {
		t.Errorf("MessageTTL = %v, want %v", loaded.MessageTTL, p.MessageTTL)
	}
	if loaded.AutoEnrollWorktrees != p.AutoEnrollWorktrees {
		t.Errorf("AutoEnrollWorktrees = %v, want %v", loaded.AutoEnrollWorktrees, p.AutoEnrollWorktrees)
	}
	if len(loaded.Repos) != 1 {
		t.Fatalf("Repos len = %d, want 1", len(loaded.Repos))
	}
	if loaded.Repos[0].ShortName != "app" {
		t.Errorf("Repo ShortName = %q, want %q", loaded.Repos[0].ShortName, "app")
	}
	if len(loaded.Repos[0].Worktrees) != 1 {
		t.Fatalf("Worktrees len = %d, want 1", len(loaded.Repos[0].Worktrees))
	}
	if len(loaded.Topics) != 2 {
		t.Errorf("Topics len = %d, want 2", len(loaded.Topics))
	}
	if loaded.MinderIdentity != "testproj/minder" {
		t.Errorf("MinderIdentity = %q, want %q", loaded.MinderIdentity, "testproj/minder")
	}
}
