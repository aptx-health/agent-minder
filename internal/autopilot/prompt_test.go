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

func TestEnsureAgentDef(t *testing.T) {
	// Override userHomeDir so tests don't pick up the real ~/.claude/agents/.
	fakeHome := t.TempDir()
	origHomeDir := userHomeDir
	userHomeDir = func() (string, error) { return fakeHome, nil }
	t.Cleanup(func() { userHomeDir = origHomeDir })

	t.Run("repo-level agent def", func(t *testing.T) {
		worktree := t.TempDir()
		agentDir := filepath.Join(worktree, ".claude", "agents")
		if err := os.MkdirAll(agentDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(agentDir, "autopilot.md"), []byte("repo custom"), 0o644); err != nil {
			t.Fatal(err)
		}

		source, err := ensureAgentDef(worktree)
		if err != nil {
			t.Fatal(err)
		}
		if source != AgentDefRepo {
			t.Errorf("expected source %q, got %q", AgentDefRepo, source)
		}
	})

	t.Run("user-level agent def", func(t *testing.T) {
		worktree := t.TempDir()
		userAgentDir := filepath.Join(fakeHome, ".claude", "agents")
		if err := os.MkdirAll(userAgentDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(userAgentDir, "autopilot.md"), []byte("user custom"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(fakeHome, ".claude")) })

		source, err := ensureAgentDef(worktree)
		if err != nil {
			t.Fatal(err)
		}
		if source != AgentDefUser {
			t.Errorf("expected source %q, got %q", AgentDefUser, source)
		}
	})

	t.Run("built-in fallback", func(t *testing.T) {
		worktree := t.TempDir()

		source, err := ensureAgentDef(worktree)
		if err != nil {
			t.Fatal(err)
		}
		if source != AgentDefBuiltIn {
			t.Errorf("expected source %q, got %q", AgentDefBuiltIn, source)
		}

		// Verify the file was written to the worktree.
		written := filepath.Join(worktree, ".claude", "agents", "autopilot.md")
		data, err := os.ReadFile(written)
		if err != nil {
			t.Fatalf("built-in agent def not written: %v", err)
		}
		if !strings.Contains(string(data), "name: autopilot") {
			t.Error("written file missing agent def frontmatter")
		}
		if !strings.Contains(string(data), "Pre-check") {
			t.Error("written file missing behavioral instructions")
		}
	})

	t.Run("repo-level takes precedence over user-level", func(t *testing.T) {
		worktree := t.TempDir()

		// Create both repo-level and user-level.
		repoAgentDir := filepath.Join(worktree, ".claude", "agents")
		if err := os.MkdirAll(repoAgentDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repoAgentDir, "autopilot.md"), []byte("repo"), 0o644); err != nil {
			t.Fatal(err)
		}

		userAgentDir := filepath.Join(fakeHome, ".claude", "agents")
		if err := os.MkdirAll(userAgentDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(userAgentDir, "autopilot.md"), []byte("user"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(fakeHome, ".claude")) })

		source, err := ensureAgentDef(worktree)
		if err != nil {
			t.Fatal(err)
		}
		if source != AgentDefRepo {
			t.Errorf("repo-level should take precedence, got %q", source)
		}
	})
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

	t.Run("always uses --agent autopilot", func(t *testing.T) {
		args := buildClaudeArgs(task, "main", "org", "repo", 50, 3.00)

		// Should always use --agent autopilot as first args.
		if args[0] != "--agent" || args[1] != "autopilot" {
			t.Errorf("expected '--agent autopilot', got %q %q", args[0], args[1])
		}

		joined := strings.Join(args, " ")

		// Should include stream-json flags.
		if !strings.Contains(joined, "--output-format stream-json") {
			t.Error("should include --output-format stream-json")
		}
		if !strings.Contains(joined, "--verbose") {
			t.Error("should include --verbose")
		}

		// Prompt should be task context only (no behavioral instructions).
		prompt := args[len(args)-1]
		if strings.Contains(prompt, "Pre-check") {
			t.Error("prompt should not contain behavioral instructions (those are in agent def)")
		}
		if !strings.Contains(prompt, "#42") {
			t.Error("prompt should contain issue number")
		}
	})

	t.Run("includes max turns and budget", func(t *testing.T) {
		args := buildClaudeArgs(task, "main", "org", "repo", 75, 5.50)
		joined := strings.Join(args, " ")

		if !strings.Contains(joined, "--max-turns 75") {
			t.Error("should include --max-turns 75")
		}
		if !strings.Contains(joined, "--max-budget-usd 5.50") {
			t.Error("should include --max-budget-usd 5.50")
		}
	})
}

func TestDefaultAgentDefContent(t *testing.T) {
	// Verify the built-in default contains expected content.
	checks := []string{
		"name: autopilot",
		"tools: Bash, Read, Edit, Write, Glob, Grep",
		"Pre-check: assess complexity",
		"more than 8 files",
		"bail immediately",
		"Quality gates",
		"Your first steps",
	}

	for _, check := range checks {
		if !strings.Contains(defaultAgentDef, check) {
			t.Errorf("defaultAgentDef missing expected content: %q", check)
		}
	}
}

func TestDefaultAgentDefMatchesRepoFile(t *testing.T) {
	// Ensure the embedded defaultAgentDef constant stays in sync with agents/autopilot.md.
	// The repo file is at <project-root>/agents/autopilot.md; from this test package
	// that's ../../agents/autopilot.md.
	repoFile := filepath.Join("..", "..", "agents", "autopilot.md")
	data, err := os.ReadFile(repoFile)
	if err != nil {
		t.Fatalf("cannot read repo agent def file %s: %v (run tests from project root)", repoFile, err)
	}
	if string(data) != defaultAgentDef {
		t.Error("defaultAgentDef constant has drifted from agents/autopilot.md — update one to match the other")
	}
}

func TestDetectAgentDef(t *testing.T) {
	fakeHome := t.TempDir()
	origHomeDir := userHomeDir
	userHomeDir = func() (string, error) { return fakeHome, nil }
	t.Cleanup(func() { userHomeDir = origHomeDir })

	t.Run("detects repo-level", func(t *testing.T) {
		dir := t.TempDir()
		agentDir := filepath.Join(dir, ".claude", "agents")
		if err := os.MkdirAll(agentDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(agentDir, "autopilot.md"), []byte("repo"), 0o644); err != nil {
			t.Fatal(err)
		}

		if source := detectAgentDef(dir); source != AgentDefRepo {
			t.Errorf("expected %q, got %q", AgentDefRepo, source)
		}
	})

	t.Run("detects user-level", func(t *testing.T) {
		dir := t.TempDir()
		userAgentDir := filepath.Join(fakeHome, ".claude", "agents")
		if err := os.MkdirAll(userAgentDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(userAgentDir, "autopilot.md"), []byte("user"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(fakeHome, ".claude")) })

		if source := detectAgentDef(dir); source != AgentDefUser {
			t.Errorf("expected %q, got %q", AgentDefUser, source)
		}
	})

	t.Run("detects built-in without writing", func(t *testing.T) {
		dir := t.TempDir()

		if source := detectAgentDef(dir); source != AgentDefBuiltIn {
			t.Errorf("expected %q, got %q", AgentDefBuiltIn, source)
		}

		// Verify nothing was written.
		path := filepath.Join(dir, ".claude", "agents", "autopilot.md")
		if _, err := os.Stat(path); err == nil {
			t.Error("detectAgentDef should not write files, but autopilot.md was created")
		}
	})
}

func TestAgentDefSourceDescription(t *testing.T) {
	tests := []struct {
		source AgentDefSource
		want   string
	}{
		{AgentDefRepo, "repo-level (.claude/agents/autopilot.md)"},
		{AgentDefUser, "user-level (~/.claude/agents/autopilot.md)"},
		{AgentDefBuiltIn, "built-in default (will be installed to worktrees)"},
		{AgentDefSource("unknown"), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.source.Description(); got != tt.want {
			t.Errorf("AgentDefSource(%q).Description() = %q, want %q", tt.source, got, tt.want)
		}
	}
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

	fmt.Printf("\n%s\n  FULL PROMPT (legacy — no agent definition)\n%s\n\n", sep, sep)
	fmt.Println(renderPrompt(task, "main", "myorg", "myrepo"))

	fmt.Printf("\n%s\n  TASK CONTEXT (used with --agent autopilot)\n%s\n\n", sep, sep)
	fmt.Println(renderTaskContext(task, "main", "myorg", "myrepo"))

	fmt.Printf("\n%s\n  BUILT-IN DEFAULT AGENT DEFINITION\n%s\n\n", sep, sep)
	fmt.Print(defaultAgentDef)
}
