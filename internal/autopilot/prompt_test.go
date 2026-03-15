package autopilot

import (
	"fmt"
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
		"Pre-check: assess complexity",
		"BEFORE making any changes",
		"more than 8 files",
		"architectural decisions",
		"bail immediately",
		"detailed implementation plan",
	}

	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("prompt missing expected content: %q", check)
		}
	}
}

func TestRenderPromptRebaseInstructions(t *testing.T) {
	task := &db.AutopilotTask{
		IssueNumber:  10,
		IssueTitle:   "Add feature",
		WorktreePath: "/tmp/wt",
		Branch:       "agent/issue-10",
	}

	prompt := renderPrompt(task, "develop", "org", "repo")

	checks := []string{
		"git fetch origin develop",
		"git rebase origin/develop",
		"rebase --abort",
		"draft PR targeting develop",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("prompt missing rebase content: %q", check)
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

func TestRenderTaskContext(t *testing.T) {
	task := &db.AutopilotTask{
		IssueNumber:  42,
		IssueTitle:   "Add user authentication",
		IssueBody:    "Implement OAuth2 login flow",
		WorktreePath: "/home/user/.agent-minder/worktrees/myproject/issue-42",
		Branch:       "agent/issue-42",
	}

	ctx := renderTaskContext(task, "main", "myorg", "myrepo")

	checks := []string{
		"#42",
		"Add user authentication",
		"OAuth2 login flow",
		"myorg/myrepo",
		"/home/user/.agent-minder/worktrees/myproject/issue-42",
		"agent/issue-42",
		"main",
		"gh issue edit 42",
		"gh issue comment 42",
		"Fixes #42",
		"git fetch origin main",
		"git rebase origin/main",
		"--add-label \"in-progress\"",
		"--add-label \"blocked\"",
		"--remove-label \"in-progress\"",
		"-R myorg/myrepo",
	}

	for _, check := range checks {
		if !strings.Contains(ctx, check) {
			t.Errorf("task context missing expected content: %q", check)
		}
	}
}

func TestRenderTaskContextEmptyBody(t *testing.T) {
	task := &db.AutopilotTask{
		IssueNumber:  1,
		IssueTitle:   "Fix bug",
		IssueBody:    "",
		WorktreePath: "/tmp/worktree",
		Branch:       "agent/issue-1",
	}

	ctx := renderTaskContext(task, "main", "owner", "repo")

	if strings.Contains(ctx, "\n\n\n\n") {
		t.Error("task context has excessive blank lines when body is empty")
	}
	if !strings.Contains(ctx, "#1") {
		t.Error("task context missing issue number")
	}
}

func TestRenderTaskContextNoBehavioralInstructions(t *testing.T) {
	task := &db.AutopilotTask{
		IssueNumber:  42,
		IssueTitle:   "Test issue",
		IssueBody:    "Test body",
		WorktreePath: "/tmp/wt",
		Branch:       "agent/issue-42",
	}

	ctx := renderTaskContext(task, "main", "org", "repo")

	// Task context must NOT contain behavioral instructions — those belong in the agent definition.
	behavioral := []string{
		"Pre-check",
		"BEFORE making any changes",
		"more than 8 files",
		"bail immediately",
		"over-engineer",
		"Quality gates",
	}

	for _, b := range behavioral {
		if strings.Contains(ctx, b) {
			t.Errorf("task context should not contain behavioral instruction: %q", b)
		}
	}
}

func TestBuildClaudeArgs(t *testing.T) {
	// Override userHomeDir so tests don't pick up the real ~/.claude/agents/.
	fakeHome := t.TempDir()
	origHomeDir := userHomeDir
	userHomeDir = func() (string, error) { return fakeHome, nil }
	t.Cleanup(func() { userHomeDir = origHomeDir })

	task := &db.AutopilotTask{
		IssueNumber:  42,
		IssueTitle:   "Test issue",
		IssueBody:    "Body",
		WorktreePath: t.TempDir(),
		Branch:       "agent/issue-42",
	}

	t.Run("without agent definition", func(t *testing.T) {
		args := buildClaudeArgs(task, "main", "org", "repo", 50, 3.00)

		// Should use -p as first arg (no --agent flag).
		if args[0] != "-p" {
			t.Errorf("expected first arg '-p', got %q", args[0])
		}

		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--agent") {
			t.Error("should not have --agent flag without agent definition")
		}

		// Should include stream-json flags.
		if !strings.Contains(joined, "--output-format stream-json") {
			t.Error("should include --output-format stream-json")
		}
		if !strings.Contains(joined, "--verbose") {
			t.Error("should include --verbose")
		}

		// Full prompt should contain behavioral instructions.
		prompt := args[len(args)-1]
		if !strings.Contains(prompt, "Pre-check") {
			t.Error("fallback prompt should contain behavioral instructions")
		}
	})

	t.Run("with agent definition", func(t *testing.T) {
		// Create .claude/agents/autopilot.md in the worktree.
		agentDir := filepath.Join(task.WorktreePath, ".claude", "agents")
		if err := os.MkdirAll(agentDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(agentDir, "autopilot.md"), []byte("---\nname: autopilot\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		args := buildClaudeArgs(task, "main", "org", "repo", 50, 3.00)

		// Should use --agent autopilot.
		if args[0] != "--agent" || args[1] != "autopilot" {
			t.Errorf("expected '--agent autopilot', got %q %q", args[0], args[1])
		}

		// Prompt should be task context only (no behavioral instructions).
		prompt := args[len(args)-1]
		if strings.Contains(prompt, "Pre-check") {
			t.Error("agent-def prompt should not contain behavioral instructions")
		}
		if !strings.Contains(prompt, "#42") {
			t.Error("agent-def prompt should contain issue number")
		}
	})
}

func TestPrintPrompts(t *testing.T) {
	if os.Getenv("PRINT_PROMPTS") == "" {
		t.Skip("set PRINT_PROMPTS=1 to print rendered prompts")
	}

	task := &db.AutopilotTask{
		IssueNumber:  42,
		IssueTitle:   "Add user authentication",
		IssueBody:    "Implement OAuth2 login flow with GitHub SSO",
		WorktreePath: "/home/user/.agent-minder/worktrees/myproject/issue-42",
		Branch:       "agent/issue-42",
	}

	sep := strings.Repeat("=", 80)

	fmt.Printf("\n%s\n  FULL PROMPT (fallback — no agent definition)\n%s\n\n", sep, sep)
	fmt.Println(renderPrompt(task, "main", "myorg", "myrepo"))

	fmt.Printf("\n%s\n  TASK CONTEXT (used with --agent autopilot)\n%s\n\n", sep, sep)
	fmt.Println(renderTaskContext(task, "main", "myorg", "myrepo"))
}
