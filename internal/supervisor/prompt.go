package supervisor

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aptx-health/agent-minder/internal/db"
	gitpkg "github.com/aptx-health/agent-minder/internal/git"
	"github.com/aptx-health/agent-minder/internal/onboarding"
)

// AgentDefSource identifies which tier provided the agent definition.
type AgentDefSource string

const (
	AgentDefRepo    AgentDefSource = "repo"
	AgentDefUser    AgentDefSource = "user"
	AgentDefBuiltIn AgentDefSource = "built-in"
)

// Description returns a human-readable description.
func (s AgentDefSource) Description() string {
	switch s {
	case AgentDefRepo:
		return "repo-level (.claude/agents/autopilot.md)"
	case AgentDefUser:
		return "user-level (~/.claude/agents/autopilot.md)"
	case AgentDefBuiltIn:
		return "built-in default"
	default:
		return string(s)
	}
}

// AgentName identifies which agent type to resolve.
type AgentName string

const (
	AgentAutopilot AgentName = "autopilot"
	AgentReviewer  AgentName = "reviewer"
)

// defaultAgentDef is the built-in fallback agent definition. It shares its
// instruction body with the autopilot template registry entry so the two
// cannot drift.
const defaultAgentDef = `---
name: autopilot
description: >
  Autonomous agent that implements GitHub issues in isolated git worktrees.
  Install in a repo's .claude/agents/ directory for project-specific guidance.
tools: Bash, Read, Edit, Write, Glob, Grep
mode: reactive
output: pr
stages:
  - name: implement
  - name: review
    agent: reviewer
    on_failure: skip
    retries: 1
context:
  - issue
  - repo_info
  - lessons
  - sibling_jobs
  - dep_graph
---

` + autopilotBody + `
`

// defaultReviewerDef is the built-in reviewer agent definition.
const defaultReviewerDef = `---
name: reviewer
description: >
  Reviews PRs opened by autopilot agents. Checks for correctness,
  test coverage, and code quality.
tools: Bash, Read, Edit, Write, Glob, Grep
mode: reactive
output: pr
---

` + reviewerBody + `
`

// defaultAllowedTools is the default tool permission set.
var defaultAllowedTools = []string{
	"Bash(git:*)",
	"Bash(gh:*)",
	"Bash(npm:*)",
	"Bash(go:*)",
	"Bash(make:*)",
	"Read",
	"Edit",
	"Write",
	"Glob",
	"Grep",
}

// ensureAgentDef checks for an agent definition and installs the built-in fallback if needed.
func ensureAgentDef(worktreePath string) (AgentDefSource, error) {
	return ensureAgentDefByName(worktreePath, AgentAutopilot)
}

func ensureAgentDefByName(worktreePath string, name AgentName) (AgentDefSource, error) {
	source, _, err := resolveAgentDefByName(worktreePath, name)
	return source, err
}

func resolveAgentDefByName(worktreePath string, name AgentName) (AgentDefSource, []byte, error) {
	filename := string(name) + ".md"

	// Check repo-level.
	repoPath := filepath.Join(worktreePath, ".claude", "agents", filename)
	if _, err := os.Stat(repoPath); err == nil {
		body, err := os.ReadFile(repoPath)
		if err != nil {
			return "", nil, err
		}
		return AgentDefRepo, body, nil
	}

	// Check user-level.
	home, _ := os.UserHomeDir()
	userPath := filepath.Join(home, ".claude", "agents", filename)
	if _, err := os.Stat(userPath); err == nil {
		body, err := os.ReadFile(userPath)
		if err != nil {
			return "", nil, err
		}
		return AgentDefUser, body, nil
	}

	// Install from template registry, falling back to built-in constants.
	for _, tmpl := range AgentTemplates() {
		if tmpl.Name == string(name) {
			path, err := InstallAgentDef(worktreePath, tmpl)
			if err != nil {
				return "", nil, err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return "", nil, err
			}
			return AgentDefBuiltIn, body, nil
		}
	}

	// Legacy fallback for agents not in the template registry.
	def := defaultAgentDef
	if name == AgentReviewer {
		def = defaultReviewerDef
	}
	dir := filepath.Join(worktreePath, ".claude", "agents")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", nil, fmt.Errorf("create agent dir: %w", err)
	}
	body := []byte(def)
	if err := os.WriteFile(filepath.Join(dir, filename), body, 0644); err != nil {
		return "", nil, fmt.Errorf("write agent def: %w", err)
	}
	return AgentDefBuiltIn, body, nil
}

// resolveBaseBranch determines the base branch from onboarding.yaml, deploy config, or git default.
func resolveBaseBranch(repoDir string, deploy *db.Deployment) string {
	// 1. Onboarding config takes priority (repo-specific knowledge).
	f, err := onboarding.Parse(onboarding.FilePath(repoDir))
	if err == nil && f.Context.BaseBranch != "" {
		return f.Context.BaseBranch
	}
	// 2. Deploy flag / config.
	if deploy.BaseBranch != "" {
		return deploy.BaseBranch
	}
	// 3. Git default branch detection.
	if branch, err := gitpkg.DefaultBranch(repoDir); err == nil && branch != "" {
		return branch
	}
	return "main"
}

// resolveAllowedTools reads tools from onboarding.yaml or returns defaults.
func resolveAllowedTools(repoDir string) []string {
	f, err := onboarding.Parse(onboarding.FilePath(repoDir))
	if err != nil {
		return defaultAllowedTools
	}
	if len(f.Permissions.AllowedTools) == 0 {
		return defaultAllowedTools
	}
	return f.Permissions.AllowedTools
}

// resolveTestCommand reads from onboarding.yaml or auto-detects.
func resolveTestCommand(repoDir string) string {
	f, err := onboarding.Parse(onboarding.FilePath(repoDir))
	if err == nil && f.Context.TestCommand != "" {
		return f.Context.TestCommand
	}
	return detectTestCommand(repoDir)
}

func detectTestCommand(repoDir string) string {
	if _, err := os.Stat(filepath.Join(repoDir, "go.mod")); err == nil {
		return "go test ./..."
	}
	if _, err := os.Stat(filepath.Join(repoDir, "package.json")); err == nil {
		return "npm test"
	}
	for _, marker := range []string{"pytest.ini", "setup.cfg", "pyproject.toml", "setup.py"} {
		if _, err := os.Stat(filepath.Join(repoDir, marker)); err == nil {
			return "pytest"
		}
	}
	if _, err := os.Stat(filepath.Join(repoDir, "Cargo.toml")); err == nil {
		return "cargo test"
	}
	if _, err := os.Stat(filepath.Join(repoDir, "Makefile")); err == nil {
		return "make test"
	}
	return ""
}

// resolveTimeout reads test, build, or setup timeout from onboarding.yaml.
// Returns default timeouts when not configured.
func resolveTimeout(repoDir string, kind string) string {
	f, err := onboarding.Parse(onboarding.FilePath(repoDir))
	if err == nil {
		switch kind {
		case "test":
			if f.Context.TestTimeout != "" {
				return f.Context.TestTimeout
			}
		case "build":
			if f.Context.BuildTimeout != "" {
				return f.Context.BuildTimeout
			}
		case "setup":
			if f.Context.SetupTimeout != "" {
				return f.Context.SetupTimeout
			}
		}
	}

	// Defaults.
	switch kind {
	case "test":
		return "5m"
	case "build":
		return "3m"
	case "setup":
		return "5m"
	default:
		return "5m"
	}
}
