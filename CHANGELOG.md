# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- **Weekly cron schedules for proactive agents**: Built-in agent definitions now include default cron schedules (weekly dependency checks, security scans, doc updates) (#431)
- **Repo-level agent defs and overnight cron jobs**: Agent definitions can be stored in repos at `.claude/agents/` and discovered at deploy time; overnight cron scheduling for proactive agents (#375)
- **Built-in agents**: `dependency-updater`, `security-scanner`, and `doc-updater` agents ship as built-in agent definitions alongside `autopilot`, `reviewer`, `designer`, and `onboarding` (#402, #404)
- **`minder agents list|show` commands**: List available agents (built-in + repo-level) and display agent definition details including contract fields (#407)
- **Enroll agent setup**: `minder enroll` now installs agent definitions into the repo and guides the onboarding agent to customize them (#418)
- **Agent flag context providers**: `--agent` flag on deploy wires context providers declared in agent contracts (#406)
- **Version flag**: `minder --version` now prints the current version (0.2.1-dev) (#422)
- **Stage executor pipeline**: Generic stage pipeline execution from contract YAML with conditional routing (`on_success`/`on_failure`), context passing between stages, and built-in pipeline templates. Smart review retries only trigger on fixable issues (not bail/exhaust), with review feedback injected as additional prompt context on retry. Per-stage retry tracking prevents infinite loops. (#374)
- **Agent log parser tests**: Comprehensive unit tests for `agentutil.ParseAgentLog` covering valid result events, error results, missing result events, malformed JSON lines, empty files, large lines, and first-result-wins behavior (#389)
- **Discovery ScanRepo unit tests**: Tests for language detection (including dedup for python/java), CI system detection, build file detection, empty repo edge case, and non-repo error handling (#390)
- **Daemon PID and heartbeat lifecycle tests**: Comprehensive unit tests for WritePID/RemovePID, WriteHeartbeat/ReadHeartbeat round-trip, WasCrashShutdown with stale/recent/missing heartbeats, IsRunning with current/dead/invalid PIDs, CleanStalePID, and StartHeartbeat goroutine lifecycle (#391)
- **JSON output for status command**: `minder status <deploy-id> --json` outputs structured JSON for scripting and piping into `jq`. Works in both local and remote modes. (#377)
- **Daemon API server tests**: httptest-based unit tests for all daemon HTTP API endpoints — status, tasks, dep-graph, lessons, stop, resume, and API key middleware (#379)
- **Deployment guide + service units**: Complete deployment documentation for running agent-minder watch mode on Ubuntu VPS (systemd) and macOS (LaunchAgent). Includes service units, install scripts, environment templates, logrotate config, firewall/Tailscale guidance, and troubleshooting. New `--foreground` flag on `deploy` and `deploy watch` for process-manager-friendly execution (#287)
- **Remote TUI client**: `agent-minder deploy tui --remote host:port` launches a k9s-like live dashboard for monitoring remote deploy daemons. Features: live-updating task table with color-coded statuses, dependency graph visualization, analysis results viewer, agent log streaming with auto-tail, and action keys for refresh/poll/stop. Configurable poll intervals via `--task-poll` and `--analysis-poll` flags. Auth via `--api-key` flag or `MINDER_API_KEY` env var. New `internal/remotetui` package with bubbletea v2 model. (#282)
- **Watch polling for TUI autopilot**: Optional `--watch-milestone` / `--watch-label` flags on the `start` command enable continuous GitHub issue discovery during autopilot sessions. New issues matching the filter are created as `pending` tasks and automatically ingested with incremental dep analysis. TUI shows a "watching" indicator when active. (#337)

### Fixed
- **Scheduler SQLite contention and UNIQUE constraint**: Fixed SQLITE_BUSY errors from concurrent scheduler/supervisor DB access; handle UNIQUE constraint violations when reinserting schedules; improved error logging (#431)
- **Daemon exiting on scheduler-only deploys**: Fixed daemon exiting immediately when running with only scheduled jobs and no issue arguments (#427)
- **Enroll validation**: Use minder CLI for YAML validation instead of Python/Ruby YAML parsers (#422)
- **Default review stage**: Add default review stage when agent contract has no stages declared, preventing nil pointer errors (#374)
- **Smart bail extraction**: Handle multi-level JSON escaping in bail report extraction; improved write-to-file pattern for issue comments; fix bail extraction and worktree cleanup across deploy IDs (#408)
- **Stable slot numbering for running jobs**: `RunningJobs()`, `SlotStatus()`, and `StopAgent()` now sort by job ID before assigning slot numbers, preventing nondeterministic ordering from Go map iteration (#384)
- **Git log date parsing fallback**: `Log()`, `LogSince()`, and `LogGrep()` now use `time.Now()` as fallback when commit dates fail RFC3339 parsing, instead of silently producing zero-valued timestamps. Extracted shared `parseLogOutput()` helper to deduplicate parsing logic across all three functions. (#392)
- **Replace custom lastIndex with strings.LastIndex**: Removed custom `lastIndex()` in review.go that reimplemented `strings.LastIndex()` and had a potential panic on short strings (#383)
- **Daemon client HTTP status range check**: Accept all 2xx status codes (not just 200) in `getJSON()` and `post()` methods, so 201/204 responses are no longer treated as errors (#386)
- **Watch filter value validation**: `ParseWatchFilter()` now rejects values containing invalid characters (e.g., semicolons, newlines, slashes). Added comprehensive test coverage for all parse paths. (#387)
- **Distinct error codes in task log endpoint**: `handleTaskLog` now returns `"task_not_found"` when the task ID doesn't exist vs `"log_not_found"` when the task exists but has no log file, instead of a single ambiguous 404 (#388)
- **Daemon heartbeat cleanup on graceful stop**: `StartHeartbeat` stop function now blocks until the heartbeat goroutine fully exits, preventing a race where the heartbeat file could be rewritten after removal during shutdown. Fixes false-positive crash detection on next startup. (#378)
- **Review session uses reviewer agent def and PR context**: The `r` (review session) command now launches Claude with `--agent reviewer` and includes PR body and comments in the initial prompt, matching the automated review path. Previously the session used no agent definition and omitted PR comments. (#348)
- **Max turns fallback detection**: When an agent's stream-json result event is missing (nil result), the supervisor now counts assistant events from the log file to detect turn limit exhaustion. Previously these agents were misclassified as "bailed" instead of "failed" with reason "max_turns". (#340)
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
