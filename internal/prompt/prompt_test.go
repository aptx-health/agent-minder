package prompt

import (
	"strings"
	"testing"

	"github.com/dustinlange/agent-minder/internal/config"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	"github.com/dustinlange/agent-minder/internal/msgbus"
)

func TestRenderInit(t *testing.T) {
	data := &InitData{
		Project:         "ripit",
		Identity:        "ripit/minder",
		RefreshInterval: "5m0s",
		Topics:          []string{"ripit/app", "ripit/infra", "ripit/coord"},
		Repos: []RepoContext{
			{
				ShortName: "app",
				Path:      "/tmp/ripit-app",
				Branch:    "main",
				Readme:    "# Ripit App\nA Next.js application.",
				RecentLogs: []gitpkg.LogEntry{
					{Hash: "abc1234", Subject: "Add auth", Author: "Dev"},
				},
				Branches: []gitpkg.BranchInfo{
					{Name: "main", IsCurrent: true},
					{Name: "feature/auth"},
				},
				Worktrees: []config.Worktree{
					{Path: "/tmp/ripit-app", Branch: "main"},
				},
			},
		},
		Messages: []msgbus.Message{
			{ID: 1, Topic: "ripit/infra", Sender: "ripit/Tobias", Message: "k3s ready"},
		},
		Agents: []string{"ripit/Cornelius", "ripit/Tobias"},
	}

	result, err := RenderInit(data)
	if err != nil {
		t.Fatalf("RenderInit: %v", err)
	}

	checks := []string{
		"ripit/minder",
		"ripit/app",
		"ripit/infra",
		"abc1234",
		"k3s ready",
		"ripit/Cornelius",
		"Begin monitoring now",
	}
	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("init prompt missing %q", check)
		}
	}
}

func TestRenderPoll(t *testing.T) {
	data := &PollData{
		Project:  "ripit",
		Identity: "ripit/minder",
		NewCommits: []RepoCommits{
			{
				ShortName: "app",
				Commits: []gitpkg.LogEntry{
					{Hash: "def5678", Subject: "Fix login bug", Author: "Dev"},
				},
			},
		},
	}

	result, err := RenderPoll(data)
	if err != nil {
		t.Fatalf("RenderPoll: %v", err)
	}

	if !strings.Contains(result, "def5678") {
		t.Error("poll prompt missing commit hash")
	}
	if !strings.Contains(result, "Fix login bug") {
		t.Error("poll prompt missing commit subject")
	}
}

func TestRenderResume(t *testing.T) {
	data := &ResumeData{
		Project:        "ripit",
		Identity:       "ripit/minder",
		PausedAt:       "2026-03-10 12:00 UTC",
		TimeSincePause: "2h30m",
		StateContent:   "## Active Concerns\n- Schema drift",
	}

	result, err := RenderResume(data)
	if err != nil {
		t.Fatalf("RenderResume: %v", err)
	}

	if !strings.Contains(result, "2h30m") {
		t.Error("resume prompt missing pause duration")
	}
	if !strings.Contains(result, "Schema drift") {
		t.Error("resume prompt missing state content")
	}
}

func TestTruncate(t *testing.T) {
	data := &InitData{
		Project:         "test",
		Identity:        "test/minder",
		RefreshInterval: "5m",
		Repos: []RepoContext{
			{
				ShortName: "repo",
				Path:      "/tmp/repo",
				Branch:    "main",
				Readme:    strings.Repeat("x", 1000),
			},
		},
	}

	result, err := RenderInit(data)
	if err != nil {
		t.Fatalf("RenderInit: %v", err)
	}

	// The 1000-char readme should be truncated to 500 + "..."
	if strings.Contains(result, strings.Repeat("x", 600)) {
		t.Error("readme was not truncated")
	}
}
