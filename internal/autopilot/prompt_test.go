package autopilot

import (
	"strings"
	"testing"

	"github.com/dustinlange/agent-minder/internal/db"
)

func TestRenderPrompt(t *testing.T) {
	task := &db.AutopilotTask{
		IssueNumber:  42,
		IssueTitle:   "Add user authentication",
		IssueBody:    "Implement OAuth2 login flow",
		WorktreePath: "/home/user/.agent-minder/worktrees/myproject/issue-42",
		Branch:       "agent/issue-42",
	}

	prompt := renderPrompt(task, "main", "myorg", "myrepo")

	// Check key content is present.
	checks := []string{
		"#42: Add user authentication",
		"OAuth2 login flow",
		"issue-42",
		"agent/issue-42",
		"main",
		"myorg/myrepo",
		"gh issue edit 42",
		"gh issue comment 42",
		"Fixes #42",
		"--add-label \"in-progress\"",
		"--add-label \"blocked\"",
		"-R myorg/myrepo",
	}

	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("prompt missing expected content: %q", check)
		}
	}
}

func TestRenderPromptEmptyBody(t *testing.T) {
	task := &db.AutopilotTask{
		IssueNumber:  1,
		IssueTitle:   "Fix bug",
		IssueBody:    "",
		WorktreePath: "/tmp/worktree",
		Branch:       "agent/issue-1",
	}

	prompt := renderPrompt(task, "main", "owner", "repo")

	if strings.Contains(prompt, "\n\n\n\n") {
		t.Error("prompt has excessive blank lines when body is empty")
	}
	if !strings.Contains(prompt, "#1: Fix bug") {
		t.Error("prompt missing issue title")
	}
}
