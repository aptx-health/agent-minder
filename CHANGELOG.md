# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `--version` flag on root CLI command, backed by `internal/build.Version` constant

## [0.1.0] - 2026-03-19

Initial pre-release. This captures the current feature set ahead of public open-source release.

### Added

- **Two-tier LLM pipeline**: Haiku summarizes git and bus activity, Sonnet analyzes and generates structured JSON with concerns and optional bus messages
- **Autopilot supervisor**: Manages concurrent Claude Code agents working on GitHub issues in isolated git worktrees, with dependency graph, dynamic task discovery, and label management
- **TUI dashboard**: Bubbletea v2 terminal UI with tabs for project overview, tracked items, and autopilot status. Includes broadcast mode, onboarding generation, theme cycling, and settings editor
- **Bus integration**: Read/write bridge to agent-msg message bus for inter-agent coordination
- **Tracked item management**: GitHub issue/PR tracking with bulk add/remove, filter mode, content-hash-based sweep pipeline, and auto-pruning
- **Onboarding system**: Interactive repo scanning via enrollment agent, onboarding.yaml generation, and onboarding message publishing
- **Agent definitions**: Three-tier failover for autopilot agent configuration (repo → user → built-in default)
- **Credential management**: OS keychain integration (macOS Keychain, Linux Secret Service, Windows Credential Manager) with env var and config file fallbacks
- **Per-tier LLM providers**: Configurable model selection per pipeline tier for cost optimization
- **Review sessions**: Launch Claude Code review sessions for autopilot PRs awaiting review
- **Dependency graph**: LLM-generated task dependency analysis with interactive rebuild, skip support, and cross-repo blocking
- **CLI commands**: `init`, `start`, `status`, `enroll`, `track`, `untrack`, `list`, `delete`, `setup`, `repo enroll/status/refresh`
- **Debug logging**: Structured JSON logging to `~/.agent-minder/debug.log` with lnav format file
- **DB schema v9**: Full migration support from v1 through v9, covering projects, polls, tracked items, completed items, autopilot tasks, and more
- **Error handling**: LLM provider retry with jitter, SQLite busy timeout, WAL recovery, and stale worktree cleanup
