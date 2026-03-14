package autopilot

import "strings"

// defaultAllowedTools defines the tools pre-approved for autopilot agents.
//
// This replaces --dangerously-skip-permissions with a least-privilege approach.
// Claude Code scopes file operations (Read, Write, Edit) to the working directory,
// so agents can only access files within their assigned worktree. Bash commands
// are restricted to common development tools via prefix-match patterns.
//
// Projects can extend this list via .claude/settings.json in their repo.
var defaultAllowedTools = []string{
	// Core file operations (scoped to worktree by Claude Code)
	"Read", "Write", "Edit", "Glob", "Grep",
	// Web access
	"WebFetch", "WebSearch",
	// Version control
	"Bash(git:*)", "Bash(gh:*)",
	// Go
	"Bash(go:*)", "Bash(golangci-lint:*)", "Bash(gofmt:*)", "Bash(goimports:*)",
	// Make
	"Bash(make:*)",
	// Node.js ecosystem
	"Bash(npm:*)", "Bash(npx:*)", "Bash(yarn:*)", "Bash(pnpm:*)",
	"Bash(bun:*)", "Bash(node:*)", "Bash(deno:*)", "Bash(tsc:*)",
	// Python
	"Bash(python:*)", "Bash(python3:*)", "Bash(pip:*)", "Bash(pip3:*)",
	"Bash(pytest:*)", "Bash(uv:*)",
	// Rust
	"Bash(cargo:*)", "Bash(rustc:*)",
	// Java/JVM
	"Bash(mvn:*)", "Bash(gradle:*)", "Bash(java:*)", "Bash(javac:*)",
	// .NET
	"Bash(dotnet:*)",
	// Ruby
	"Bash(bundle:*)", "Bash(rake:*)", "Bash(ruby:*)", "Bash(gem:*)",
	// Swift
	"Bash(swift:*)", "Bash(swiftc:*)",
	// Linters/formatters
	"Bash(eslint:*)", "Bash(prettier:*)", "Bash(black:*)", "Bash(ruff:*)",
	// Docker
	"Bash(docker:*)", "Bash(docker-compose:*)",
	// Shell utilities
	"Bash(ls:*)", "Bash(cat:*)", "Bash(mkdir:*)", "Bash(cp:*)", "Bash(mv:*)",
	"Bash(rm:*)", "Bash(chmod:*)", "Bash(echo:*)", "Bash(printf:*)",
	"Bash(grep:*)", "Bash(find:*)", "Bash(fd:*)", "Bash(rg:*)",
	"Bash(head:*)", "Bash(tail:*)", "Bash(wc:*)", "Bash(sort:*)",
	"Bash(sed:*)", "Bash(awk:*)", "Bash(diff:*)", "Bash(which:*)",
	"Bash(pwd:*)", "Bash(env:*)", "Bash(cd:*)", "Bash(touch:*)",
	"Bash(test:*)", "Bash([:*)", "Bash(tee:*)", "Bash(tr:*)",
	"Bash(cut:*)", "Bash(xargs:*)", "Bash(true:*)", "Bash(false:*)",
	"Bash(export:*)", "Bash(date:*)", "Bash(uname:*)",
	"Bash(curl:*)", "Bash(wget:*)",
	"Bash(tar:*)", "Bash(unzip:*)", "Bash(gzip:*)",
}

// allowedToolsArg returns the --allowedTools argument value for the claude CLI.
func allowedToolsArg() string {
	return strings.Join(defaultAllowedTools, ",")
}
