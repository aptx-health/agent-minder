# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed
- **Autopilot settings display**: Settings panel and autopilot confirm screen now show effective defaults (150 turns, $10.00 budget) instead of raw DB zeros when no override is configured. Bumped default fallbacks from 50→150 turns and $3.00→$10.00. (#341)
- **Worktree preserved until PR merge**: Worktrees for review tasks are no longer deleted immediately when the agent posts a PR. They are now cleaned up only when the task reaches `done` (PR merged), preventing disruption to users with active terminal sessions in the worktree. (#342)

### Changed
- **Long-lived supervisor**: Supervisor stays alive after all tasks complete instead of exiting — both daemon and TUI modes. Watch loop creates supervisor once at startup; new tasks are inserted as `pending` and ingested by the supervisor's 30-second ticker with incremental dep analysis. TUI autopilot mode stays active with idle slots visible until user explicitly presses `A` to stop. Eliminates dual-goroutine dep graph race conditions. (#336)

### Added
- **Automated dep graph resolution for daemon mode**: In watch/unattended mode, the LLM now auto-selects a single conservative dependency strategy using `--json-schema` structured output, eliminating the interactive carousel. Includes `reasoning` and `confidence` fields stored in `autopilot_dep_graphs` for auditability. Low-confidence graphs (below 60%) emit a warning concern and webhook notification but do not block. Incremental dep analysis with reverse dependency injection runs automatically when new issues are discovered by the watch loop. Schema v28 adds `reasoning` and `confidence` columns. (#279)
- **Observability TUI tab**: New tab (keybinding `4`) showing daily, weekly, and overall project cost breakdowns with per-task cost detail, using live data from the cost aggregation backend (#228)
- **Cost controls and circuit breakers**: Total spend tracking with configurable budget ceiling (`--total-budget 50.00`) that auto-pauses task launches when hit. 80% warning and 100% limit notifications via webhook. Configurable behavior for running agents (`--budget-pause-running` to also stop them). Resume via `POST /resume` API endpoint. Budget utilization exposed in `/metrics` and `/status` endpoints. Persistent cost aggregation: task costs are banked to `carried_cost_usd` before clearing, so budget ceilings survive daemon restarts. Schema v27 adds `total_budget_usd`, `budget_pause_running`, and `carried_cost_usd` columns. (#285)
- **CLI remote client**: `--remote host:port` flag on `deploy list`, `deploy status`, and `deploy stop` commands to query a remote daemon's HTTP API instead of the local SQLite database. Supports `--api-key` flag and `MINDER_REMOTE`/`MINDER_API_KEY` env vars for defaults. Graceful error handling for connection refused, auth failure, and timeout. Remote stop sends `POST /stop` to trigger graceful daemon shutdown. (#281)
- **HTTP API server**: Embeddable HTTP API server (`--serve :7749`) in deploy daemon exposing `/status`, `/tasks`, `/tasks/:id`, `/tasks/:id/log` (with SSE streaming), `/dep-graph`, `/analysis`, `/analysis/poll`, `/metrics`, `/stop`, and `/resume` endpoints; API key auth via `--api-key` flag or `MINDER_API_KEY` env var; CORS headers; graceful shutdown on SIGTERM
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
