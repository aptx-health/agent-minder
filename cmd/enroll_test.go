package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aptx-health/agent-minder/internal/discovery"
	"github.com/aptx-health/agent-minder/internal/onboarding"
	runtimepkg "github.com/aptx-health/agent-minder/internal/runtime"
)

func TestBuildEnrollAgentCommandClaude(t *testing.T) {
	binDir := t.TempDir()
	writeStubExe(t, binDir, "claude")
	t.Setenv("PATH", binDir)

	cmd, err := buildEnrollAgentCommand(runtimepkg.NameClaudeCode, "/tmp/repo", "system prompt")
	if err != nil {
		t.Fatalf("buildEnrollAgentCommand: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	for _, want := range []string{"claude", "--append-system-prompt", "system prompt", "Begin the onboarding interview"} {
		if !strings.Contains(got, want) {
			t.Fatalf("claude args missing %q: %v", want, cmd.Args)
		}
	}
	if cmd.Dir != "/tmp/repo" {
		t.Fatalf("Dir = %q, want /tmp/repo", cmd.Dir)
	}
}

func TestBuildEnrollAgentCommandCodex(t *testing.T) {
	binDir := t.TempDir()
	writeStubExe(t, binDir, "codex")
	t.Setenv("PATH", binDir)

	cmd, err := buildEnrollAgentCommand(runtimepkg.NameCodex, "/tmp/repo", "system prompt")
	if err != nil {
		t.Fatalf("buildEnrollAgentCommand: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	for _, want := range []string{"codex", "--cd /tmp/repo", "--sandbox workspace-write", "--ask-for-approval on-request", "system prompt", "Begin the onboarding interview"} {
		if !strings.Contains(got, want) {
			t.Fatalf("codex args missing %q: %v", want, cmd.Args)
		}
	}
	if cmd.Dir != "/tmp/repo" {
		t.Fatalf("Dir = %q, want /tmp/repo", cmd.Dir)
	}
}

func TestBuildEnrollSystemPromptHarnessAware(t *testing.T) {
	info := &discovery.RepoInfo{
		Path: "/tmp/repo",
		Inventory: onboarding.Inventory{
			Languages: []string{"Go"},
		},
	}
	prompt := buildEnrollSystemPrompt(info, "/tmp/repo/.agent-minder/onboarding.yaml", "Installed agent definitions: autopilot", runtimepkg.NameCodex)
	for _, want := range []string{
		"Onboarding runtime: codex",
		"canonical agent contract format",
		".claude/agents/<name>.md",
		"Codex receives",
		".agent-minder/config.yaml",
		"runtime: codex",
		"Harness compatibility checks",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}

func writeStubExe(t *testing.T, dir, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".bat"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write stub executable: %v", err)
	}
}
