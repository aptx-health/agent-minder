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
		tools := []string{"Read", "Edit", "Write", "Bash(git *)"}
		args := buildClaudeArgs(task, "main", "org", "repo", 50, 3.00, tools)

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

		// Should NOT include --dangerously-skip-permissions.
		if strings.Contains(joined, "dangerously") {
			t.Error("should not include --dangerously-skip-permissions")
		}

		// Should include --allowedTools with comma-separated tools in CLI format.
		if !strings.Contains(joined, "--allowedTools") {
			t.Error("should include --allowedTools flag")
		}
		// Find the allowedTools value.
		for i, arg := range args {
			if arg == "--allowedTools" && i+1 < len(args) {
				toolsVal := args[i+1]
				if !strings.Contains(toolsVal, "Read") {
					t.Error("allowedTools should include Read")
				}
				if !strings.Contains(toolsVal, "Bash(git:*)") {
					t.Errorf("allowedTools should include Bash(git:*), got %q", toolsVal)
				}
				break
			}
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
		args := buildClaudeArgs(task, "main", "org", "repo", 75, 5.50, defaultAllowedTools)
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

func TestDefaultReviewerDefContent(t *testing.T) {
	checks := []string{
		"name: reviewer",
		"tools: Bash, Read, Edit, Write, Glob, Grep",
		"Review process",
		"Fix protocol",
		"Risk Assessment",
		"APPROVE | REQUEST_CHANGES",
		"Structured assessment",
		"Rebase and conflict resolution",
	}

	for _, check := range checks {
		if !strings.Contains(defaultReviewerDef, check) {
			t.Errorf("defaultReviewerDef missing expected content: %q", check)
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

func TestDefaultReviewerDefMatchesRepoFile(t *testing.T) {
	repoFile := filepath.Join("..", "..", "agents", "reviewer.md")
	data, err := os.ReadFile(repoFile)
	if err != nil {
		t.Fatalf("cannot read repo reviewer def file %s: %v (run tests from project root)", repoFile, err)
	}
	if string(data) != defaultReviewerDef {
		t.Error("defaultReviewerDef constant has drifted from agents/reviewer.md — update one to match the other")
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

		if source := DetectAgentDef(dir); source != AgentDefRepo {
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

		if source := DetectAgentDef(dir); source != AgentDefUser {
			t.Errorf("expected %q, got %q", AgentDefUser, source)
		}
	})

	t.Run("detects built-in without writing", func(t *testing.T) {
		dir := t.TempDir()

		if source := DetectAgentDef(dir); source != AgentDefBuiltIn {
			t.Errorf("expected %q, got %q", AgentDefBuiltIn, source)
		}

		// Verify nothing was written.
		path := filepath.Join(dir, ".claude", "agents", "autopilot.md")
		if _, err := os.Stat(path); err == nil {
			t.Error("DetectAgentDef should not write files, but autopilot.md was created")
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

func TestToCliAllowedTools(t *testing.T) {
	tools := []string{"Read", "Edit", "Bash(git *)", "Bash(gh *)"}
	got := toCliAllowedTools(tools)
	want := "Read,Edit,Bash(git:*),Bash(gh:*)"
	if got != want {
		t.Errorf("toCliAllowedTools() = %q, want %q", got, want)
	}
}

func TestResolveAllowedTools(t *testing.T) {
	t.Run("returns defaults when no onboarding file", func(t *testing.T) {
		dir := t.TempDir()
		tools := resolveAllowedTools(dir)
		if len(tools) != len(defaultAllowedTools) {
			t.Fatalf("expected %d tools, got %d", len(defaultAllowedTools), len(tools))
		}
		for i, tool := range tools {
			if tool != defaultAllowedTools[i] {
				t.Errorf("tool[%d] = %q, want %q", i, tool, defaultAllowedTools[i])
			}
		}
	})

	t.Run("returns defaults when onboarding has empty permissions", func(t *testing.T) {
		dir := t.TempDir()
		onboardDir := filepath.Join(dir, ".agent-minder")
		if err := os.MkdirAll(onboardDir, 0o755); err != nil {
			t.Fatal(err)
		}
		yaml := "version: 1\nscanned_at: 2024-01-01T00:00:00Z\npermissions:\n  allowed_tools: []\n"
		if err := os.WriteFile(filepath.Join(onboardDir, "onboarding.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}

		tools := resolveAllowedTools(dir)
		if len(tools) != len(defaultAllowedTools) {
			t.Fatalf("expected defaults, got %d tools", len(tools))
		}
	})

	t.Run("returns onboarding tools when present", func(t *testing.T) {
		dir := t.TempDir()
		onboardDir := filepath.Join(dir, ".agent-minder")
		if err := os.MkdirAll(onboardDir, 0o755); err != nil {
			t.Fatal(err)
		}
		yaml := `version: 1
scanned_at: 2024-01-01T00:00:00Z
permissions:
  allowed_tools:
    - Read
    - Write
    - "Bash(go *)"
    - "Bash(npm *)"
`
		if err := os.WriteFile(filepath.Join(onboardDir, "onboarding.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}

		tools := resolveAllowedTools(dir)
		expected := []string{"Read", "Write", "Bash(go *)", "Bash(npm *)"}
		if len(tools) != len(expected) {
			t.Fatalf("expected %d tools, got %d: %v", len(expected), len(tools), tools)
		}
		for i, tool := range tools {
			if tool != expected[i] {
				t.Errorf("tool[%d] = %q, want %q", i, tool, expected[i])
			}
		}
	})
}

func TestRenderResumeTaskContext(t *testing.T) {
	task := &db.AutopilotTask{
		IssueNumber:   42,
		IssueTitle:    "Add user authentication",
		IssueBody:     "Implement OAuth2 login flow",
		WorktreePath:  "/home/user/.agent-minder/worktrees/myproject/issue-42",
		Branch:        "agent/issue-42",
		FailureReason: "max_turns",
		FailureDetail: "used 50 of 50 turns",
	}

	prompt := renderResumeTaskContext(task, "main", "myorg", "myrepo")

	// Should contain resume header.
	if !strings.Contains(prompt, "Resuming Previous Work") {
		t.Error("should contain 'Resuming Previous Work' header")
	}

	// Should contain failure reason.
	if !strings.Contains(prompt, "max_turns") {
		t.Error("should contain failure reason")
	}

	// Should contain failure detail.
	if !strings.Contains(prompt, "used 50 of 50 turns") {
		t.Error("should contain failure detail")
	}

	// Should contain the standard task context.
	if !strings.Contains(prompt, "#42") {
		t.Error("should contain issue number from task context")
	}
	if !strings.Contains(prompt, "myorg/myrepo") {
		t.Error("should contain repository from task context")
	}
	if !strings.Contains(prompt, "Fixes #42") {
		t.Error("should contain commit fix reference from task context")
	}
}

func TestRenderResumeTaskContextNoFailure(t *testing.T) {
	// Stopped tasks may not have a failure reason.
	task := &db.AutopilotTask{
		IssueNumber:  10,
		IssueTitle:   "Stopped task",
		IssueBody:    "Some work",
		WorktreePath: "/tmp/worktree",
		Branch:       "agent/issue-10",
	}

	prompt := renderResumeTaskContext(task, "main", "org", "repo")

	if !strings.Contains(prompt, "Resuming Previous Work") {
		t.Error("should contain resume header")
	}
	// Should NOT contain failure reason line since there isn't one.
	if strings.Contains(prompt, "Previous attempt ended due to") {
		t.Error("should not contain failure reason when not set")
	}
}

func TestBuildResumeClaudeArgs(t *testing.T) {
	fakeHome := t.TempDir()
	origHomeDir := userHomeDir
	userHomeDir = func() (string, error) { return fakeHome, nil }
	t.Cleanup(func() { userHomeDir = origHomeDir })

	task := &db.AutopilotTask{
		IssueNumber:   42,
		IssueTitle:    "Test issue",
		IssueBody:     "Body",
		WorktreePath:  t.TempDir(),
		Branch:        "agent/issue-42",
		FailureReason: "max_budget",
		FailureDetail: "spent $3.00 of $3.00 budget",
	}

	args := buildResumeClaudeArgs(task, "main", "org", "repo", 50, 3.00, defaultAllowedTools)
	joined := strings.Join(args, " ")

	// Should use --agent autopilot.
	if args[0] != "--agent" || args[1] != "autopilot" {
		t.Errorf("expected '--agent autopilot', got %q %q", args[0], args[1])
	}

	// Should include --resume flag.
	if !strings.Contains(joined, "--resume") {
		t.Error("should include --resume flag")
	}

	// Prompt should contain resume context.
	prompt := args[len(args)-1]
	if !strings.Contains(prompt, "Resuming Previous Work") {
		t.Error("prompt should contain resume header")
	}
	if !strings.Contains(prompt, "max_budget") {
		t.Error("prompt should contain failure reason")
	}
}

func TestDetectAgentDefByName(t *testing.T) {
	fakeHome := t.TempDir()
	origHomeDir := userHomeDir
	userHomeDir = func() (string, error) { return fakeHome, nil }
	t.Cleanup(func() { userHomeDir = origHomeDir })

	t.Run("detects repo-level reviewer", func(t *testing.T) {
		dir := t.TempDir()
		agentDir := filepath.Join(dir, ".claude", "agents")
		if err := os.MkdirAll(agentDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(agentDir, "reviewer.md"), []byte("repo reviewer"), 0o644); err != nil {
			t.Fatal(err)
		}

		if source := DetectAgentDefByName(dir, AgentReviewer); source != AgentDefRepo {
			t.Errorf("expected %q, got %q", AgentDefRepo, source)
		}
	})

	t.Run("detects user-level reviewer", func(t *testing.T) {
		dir := t.TempDir()
		userAgentDir := filepath.Join(fakeHome, ".claude", "agents")
		if err := os.MkdirAll(userAgentDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(userAgentDir, "reviewer.md"), []byte("user reviewer"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(fakeHome, ".claude")) })

		if source := DetectAgentDefByName(dir, AgentReviewer); source != AgentDefUser {
			t.Errorf("expected %q, got %q", AgentDefUser, source)
		}
	})

	t.Run("falls back to built-in for reviewer", func(t *testing.T) {
		dir := t.TempDir()

		if source := DetectAgentDefByName(dir, AgentReviewer); source != AgentDefBuiltIn {
			t.Errorf("expected %q, got %q", AgentDefBuiltIn, source)
		}
	})
}

func TestEnsureAgentDefByName(t *testing.T) {
	fakeHome := t.TempDir()
	origHomeDir := userHomeDir
	userHomeDir = func() (string, error) { return fakeHome, nil }
	t.Cleanup(func() { userHomeDir = origHomeDir })

	t.Run("installs reviewer built-in to worktree", func(t *testing.T) {
		worktree := t.TempDir()

		source, err := ensureAgentDefByName(worktree, AgentReviewer)
		if err != nil {
			t.Fatal(err)
		}
		if source != AgentDefBuiltIn {
			t.Errorf("expected %q, got %q", AgentDefBuiltIn, source)
		}

		written := filepath.Join(worktree, ".claude", "agents", "reviewer.md")
		data, err := os.ReadFile(written)
		if err != nil {
			t.Fatalf("reviewer def not written: %v", err)
		}
		if !strings.Contains(string(data), "name: reviewer") {
			t.Error("written file missing reviewer frontmatter")
		}
		if !strings.Contains(string(data), "Review process") {
			t.Error("written file missing review instructions")
		}
	})

	t.Run("unknown agent name returns error", func(t *testing.T) {
		worktree := t.TempDir()

		_, err := ensureAgentDefByName(worktree, AgentName("nonexistent"))
		if err == nil {
			t.Error("expected error for unknown agent name")
		}
	})
}

func TestRenderReviewTaskContext(t *testing.T) {
	task := &db.AutopilotTask{
		IssueNumber:  42,
		IssueTitle:   "Add user authentication",
		IssueBody:    "Implement OAuth2 login flow",
		WorktreePath: "/home/user/.agent-minder/worktrees/myproject/issue-42",
		Branch:       "agent/issue-42",
		PRNumber:     99,
	}

	ctx := renderReviewTaskContext(task, "main", "myorg", "myrepo", "Build a secure healthcare platform")

	checks := []string{
		"#99", // PR number
		"#42", // Issue number
		"Add user authentication",
		"myorg/myrepo",
		"agent/issue-42",
		"main",
		"OAuth2 login flow",
		"Build a secure healthcare platform",
		"gh pr diff 99",
		"gh pr view 99",
		"git fetch origin main",
		"git rebase origin/main",
	}

	for _, check := range checks {
		if !strings.Contains(ctx, check) {
			t.Errorf("review task context missing: %q", check)
		}
	}
}

func TestRenderReviewTaskContextEmptyGoal(t *testing.T) {
	task := &db.AutopilotTask{
		IssueNumber:  10,
		IssueTitle:   "Fix bug",
		WorktreePath: "/tmp/wt",
		Branch:       "agent/issue-10",
		PRNumber:     15,
	}

	ctx := renderReviewTaskContext(task, "main", "org", "repo", "")

	if strings.Contains(ctx, "Project Goal") {
		t.Error("should not include project goal section when empty")
	}
}

func TestBuildReviewClaudeArgs(t *testing.T) {
	task := &db.AutopilotTask{
		IssueNumber:  42,
		IssueTitle:   "Test issue",
		IssueBody:    "Body",
		WorktreePath: "/tmp/wt",
		Branch:       "agent/issue-42",
		PRNumber:     99,
	}

	tools := []string{"Read", "Edit", "Write", "Bash(git *)"}
	args := buildReviewClaudeArgs(task, "main", "org", "repo", "Project goal", 30, 2.00, tools)

	// Should use --agent reviewer.
	if args[0] != "--agent" || args[1] != "reviewer" {
		t.Errorf("expected '--agent reviewer', got %q %q", args[0], args[1])
	}

	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "--output-format stream-json") {
		t.Error("should include --output-format stream-json")
	}
	if !strings.Contains(joined, "--max-turns 30") {
		t.Error("should include --max-turns 30")
	}
	if !strings.Contains(joined, "--max-budget-usd 2.00") {
		t.Error("should include --max-budget-usd 2.00")
	}

	// Prompt should contain review context.
	prompt := args[len(args)-1]
	if !strings.Contains(prompt, "#99") {
		t.Error("prompt should contain PR number")
	}
	if !strings.Contains(prompt, "#42") {
		t.Error("prompt should contain issue number")
	}
	if !strings.Contains(prompt, "Project goal") {
		t.Error("prompt should contain project goal")
	}
}

func TestDescriptionFor(t *testing.T) {
	tests := []struct {
		source AgentDefSource
		name   AgentName
		want   string
	}{
		{AgentDefRepo, AgentAutopilot, "repo-level (.claude/agents/autopilot.md)"},
		{AgentDefRepo, AgentReviewer, "repo-level (.claude/agents/reviewer.md)"},
		{AgentDefUser, AgentReviewer, "user-level (~/.claude/agents/reviewer.md)"},
		{AgentDefBuiltIn, AgentReviewer, "built-in default (will be installed to worktrees)"},
	}
	for _, tt := range tests {
		if got := tt.source.DescriptionFor(tt.name); got != tt.want {
			t.Errorf("DescriptionFor(%q, %q) = %q, want %q", tt.source, tt.name, got, tt.want)
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

	fmt.Printf("\n%s\n  REVIEW TASK CONTEXT (used with --agent reviewer)\n%s\n\n", sep, sep)
	task.PRNumber = 123
	fmt.Println(renderReviewTaskContext(task, "main", "myorg", "myrepo", "Build a secure healthcare platform"))

	fmt.Printf("\n%s\n  BUILT-IN DEFAULT REVIEWER DEFINITION\n%s\n\n", sep, sep)
	fmt.Print(defaultReviewerDef)
}
