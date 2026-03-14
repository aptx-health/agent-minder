package autopilot

import (
	"os"
	"path/filepath"
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
		"go build ./...",
		"go test ./...",
		"Test results",
		"do NOT open a PR",
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

func TestCheckAgentLogForTests(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name     string
		content  string
		verified bool
		detail   string
	}{
		{
			name:     "both build and test present",
			content:  "running go build ./...\nok\nrunning go test ./...\nok  \tgithub.com/foo/bar\t0.5s\n",
			verified: true,
		},
		{
			name:     "no build or test evidence",
			content:  "I made some changes and opened a PR.\n",
			verified: false,
			detail:   "no evidence of build/test validation in agent log",
		},
		{
			name:     "only build, no test",
			content:  "go build ./...\nok\n",
			verified: false,
			detail:   "no evidence of test validation in agent log",
		},
		{
			name:     "only test, no build",
			content:  "go test ./...\nok  \tgithub.com/foo/bar\t0.5s\n",
			verified: false,
			detail:   "no evidence of build validation in agent log",
		},
		{
			name:     "tests failing at end of log",
			content:  "go build ./...\ngo test ./...\nFAIL\tgithub.com/foo/bar\t0.5s\n",
			verified: false,
			detail:   "agent log suggests tests may still be failing",
		},
		{
			name:     "tests failed then passed",
			content:  "go build ./...\ngo test ./...\nFAIL\tgithub.com/foo/bar\t0.5s\nfixed the issue\ngo test ./...\nok \tgithub.com/foo/bar\t0.5s\n",
			verified: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logPath := filepath.Join(dir, tt.name+".log")
			os.WriteFile(logPath, []byte(tt.content), 0644)

			verified, detail := checkAgentLogForTests(logPath)
			if verified != tt.verified {
				t.Errorf("verified = %v, want %v", verified, tt.verified)
			}
			if !tt.verified && detail != tt.detail {
				t.Errorf("detail = %q, want %q", detail, tt.detail)
			}
		})
	}
}

func TestCheckAgentLogForTestsMissingFile(t *testing.T) {
	verified, detail := checkAgentLogForTests("/nonexistent/path.log")
	if verified {
		t.Error("expected verified=false for missing file")
	}
	if detail != "could not read agent log to verify tests" {
		t.Errorf("unexpected detail: %q", detail)
	}
}
