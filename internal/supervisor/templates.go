package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentTemplate defines a template for installing agent definitions.
type AgentTemplate struct {
	Name        string
	Required    bool   // required agents are installed automatically
	Frontmatter string // YAML frontmatter (between --- markers)
	DefaultBody string // default instruction body
}

// AgentTemplates returns all known agent templates.
func AgentTemplates() []AgentTemplate {
	return []AgentTemplate{
		{
			Name:     "autopilot",
			Required: true,
			Frontmatter: `name: autopilot
description: >
  Autonomous agent that implements GitHub issues in isolated git worktrees.
tools: Bash, Read, Edit, Write, Glob, Grep
mode: reactive
output: pr
context:
  - issue
  - repo_info
  - lessons
  - sibling_jobs
  - dep_graph`,
			DefaultBody: `You are an autonomous agent working on a GitHub issue in an isolated git worktree.
Your task context is provided in the user prompt.

## First steps
1. Label the issue in-progress using the gh command from your task context
2. Post a starting comment
3. Read the full issue with comments and any linked issues
4. Explore the codebase to understand the relevant code

## If you can complete the work
- Implement the changes
- Ensure tests pass
- Commit with "Fixes #<issue>" in the message
- Rebase onto the base branch before pushing
- Open a draft PR

## If you cannot proceed
- Post a comment on the issue explaining what blocks you
- Add the "blocked" label and remove the "in-progress" label

## Constraints
- Only modify files within your worktree
- Do not keep retrying if stuck — bail early with good context
- Do not over-engineer`,
		},
		{
			Name:     "reviewer",
			Required: true,
			Frontmatter: `name: reviewer
description: >
  Reviews PRs opened by autopilot agents. Checks for correctness,
  test coverage, and code quality.
tools: Bash, Read, Edit, Write, Glob, Grep
mode: reactive
output: pr`,
			DefaultBody: `You are a code reviewer examining a PR opened by an automated agent.
Review context is provided in the user prompt.

## Review process
1. Read the PR diff and understand the changes
2. Check that the implementation matches the issue requirements
3. Run the test command if provided
4. Look for bugs, edge cases, and missing error handling
5. Assess risk level: low-risk, needs-testing, or suspect

## If changes are needed
- Make the fixes directly in the worktree
- Run tests to verify
- Commit and push`,
		},
	}
}

// InstallAgentDef writes an agent definition file to the repo's .claude/agents/ directory.
// Only writes the frontmatter + default body. Returns the file path.
func InstallAgentDef(repoDir string, tmpl AgentTemplate) (string, error) {
	dir := filepath.Join(repoDir, ".claude", "agents")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create agents dir: %w", err)
	}

	path := filepath.Join(dir, tmpl.Name+".md")
	content := fmt.Sprintf("---\n%s\n---\n\n%s\n", tmpl.Frontmatter, tmpl.DefaultBody)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("write agent def: %w", err)
	}
	return path, nil
}

// ValidateAgentDefs checks all .md files in .claude/agents/ parse correctly.
// Returns a list of validation errors (empty if all valid).
func ValidateAgentDefs(repoDir string) []string {
	agentsDir := filepath.Join(repoDir, ".claude", "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil // no agents dir is fine
	}

	var errors []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(agentsDir, e.Name())
		_, err := ParseContract(path)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", e.Name(), err))
		}
	}
	return errors
}
