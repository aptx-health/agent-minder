# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- **Notification webhooks**: Configurable webhook notifications for autopilot task state changes (started, completed, bailed, failed, stopped, budget limit, agent error, discovered, finished). Supports Slack-compatible incoming webhooks out of the box and a generic JSON format for other integrations. Rapid-fire events are batched into a single notification to avoid spamming. Configure per-project via `webhook_url`, `webhook_format`, and `webhook_events` columns. Schema v25 adds the notification columns to the projects table.
- **Restart resilience**: Deploy daemons now detect and recover from unclean shutdowns — stale running/reviewing tasks are reset to queued, orphaned worktrees cleaned up, and stale PID files removed. Heartbeat file tracks daemon liveness. New `deploy respawn` command to relaunch crashed daemons. Idempotent startup prevents duplicate daemons. (#286)
- **Deploy watch mode**: `deploy watch --milestone "v1.0"` or `deploy watch --label "ready-for-agent"` runs a long-lived daemon that continuously polls GitHub for new open issues matching a filter and auto-queues them for autopilot agents. Supports configurable poll interval, deduplication, skip label filtering, and never-exit semantics (#278)
- **TUI review states**: Display `reviewing` (with spinner-colored indicator) and `reviewed` (with risk tier) statuses in task list; review agents shown with "R:" prefix in slot display; risk summary line showing counts by tier; risk tier color-coded in task detail panel (green=low-risk, amber=needs-testing, red=suspect)
- **Schema v23**: Review automation columns — `autopilot_auto_merge`, `autopilot_review_max_turns`, `autopilot_review_max_budget_usd` on projects; `review_risk`, `review_comment_id` on autopilot_tasks; new `reviewing`/`reviewed` task statuses
- **Review agent spawning**: Supervisor automatically spawns reviewer agents when tasks enter `review` status, using review-specific resource limits and the reviewer agent definition
- **Reviewer rebase instructions**: Enhanced reviewer agent definition with detailed rebase detection, intelligent conflict resolution strategy, escape hatch for unresolvable conflicts, and PR commenting after rebase
- **Structured PR review comments**: Review agent posts a formatted markdown comment on the PR with risk tier, recommendation, and structured assessment; applies risk labels (`low-risk`, `needs-testing`, `suspect`) and stores comment ID for future updates
- **Review test command**: Review agent context now includes the project's test command from `onboarding.yaml`, enabling reviewers to run the correct test suite after making fixes
- **Convention-based test command detection**: When no test command is configured in `onboarding.yaml`, the reviewer automatically detects the project's test framework (Go, Node.js, Python, Rust, Makefile) and includes the conventional test command in the review context
- **Auto-merge for low-risk PRs**: When `autopilot_auto_merge` is enabled on a project, reviewed PRs assessed as `low-risk` are automatically squash-merged with a comment; failures leave the task in `reviewed` for manual intervention

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
