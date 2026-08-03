# agent-minder

Go CLI coordination layer on top of [agent-msg](../agent-msg). Monitors git repos, watches the message bus, and uses Claude Code CLI for LLM analysis to coordinate agents.

## Quick orientation

- **Module**: `github.com/aptx-health/agent-minder`
- **Go version**: 1.25+ (bubbletea v2 requirement)
- **State**: SQLite at `~/.agent-minder/v2.db` (WAL mode, foreign keys, single-writer via `SetMaxOpenConns(1)`)
- **LLM**: agent CLIs (Claude Code, Codex, opencode) — no API key needed
- **Version**: 0.2.1-dev (`minder --version`)

## Architecture

Two distinct LLM paths:

- **Doer agents** go through `internal/runtime`, which abstracts the agent process behind the `AgentRuntime` interface. Three implementations: `claudecode` (`claude --agent`), `codex` (`codex exec`), and `opencode` (HTTP server + SDK). `internal/runtime/registry.go` resolves a runtime by name; `claude-code` is the default.
- **Analysis calls** go through `internal/claudecli`, which wraps `claude -p --output-format json`. Used for dependency-graph resolution, review-assessment extraction, and lesson grooming — not for agent execution.

One agent contract serves all three runtimes. `SlotContext.EnsureAgentDef` resolves the definition once (repo `.claude/agents/<name>.md` → user-level → built-in registry), then hands the body to the active runtime's `PrepareAgentDef`, which materializes it natively: `.claude/agents/<name>.md` for Claude Code, `AGENTS.md` for Codex, `.opencode/agent/<name>.md` for opencode.

### Supervisor (internal/supervisor)

Manages N concurrent Claude Code agents working on GitHub issues in isolated worktrees. Long-lived — stays alive after all tasks complete (both daemon and TUI modes).

**Job lifecycle:** `queued` → `running` → `review` (PR opened) → `reviewing` → `reviewed` → `done` (PR merged) | `bailed` (agent gave up)

**Key behaviors:**
- LLM-built dependency graph determines execution order; stored in `dep_graphs` table
- Agent contracts in `.claude/agents/*.md` declare mode, output, context providers, dedup strategies, and multi-stage pipelines
- Stage executor iterates declared pipeline stages with conditional routing (`on_success`/`on_failure`) and context passing between stages
- Built-in agents (`AgentTemplates()` in `internal/supervisor/templates.go`): `autopilot`, `reviewer`, `bug-fixer`, `dependency-updater`, `security-scanner`, `doc-updater`, `spike`. `autopilot` and `reviewer` are required and installed automatically; the rest are opt-in. `designer` is interactive and lives outside the registry in `agents/designer.md`.
- Review pipeline: supervisor spawns reviewer agent when jobs enter `review` status; posts structured PR comment with risk tier (`low-risk`/`needs-testing`/`suspect`)
- Auto-merge: when enabled, low-risk PRs are automatically squash-merged (waits for CI)
- Smart bail detection: multi-level JSON escaping handling, write-to-file pattern for issue comments
- Watch mode: continuous GitHub polling for new issues matching label/milestone filter
- Daemon mode: automated dep graph resolution via LLM with reasoning/confidence fields; low-confidence warnings

**Paths:**
- Worktree: `~/.agent-minder/worktrees/<deploy-id>/issue-<N>`, branch: `agent/issue-<N>`
- Agent logs: `~/.agent-minder/agents/<deploy-id>-issue-<N>.log`

**Agent command:** `claude --agent <name> -p --max-turns <N> --max-budget-usd <B> --allowedTools <tool> ... "<prompt>"` with `GITHUB_TOKEN` env var.

### DB schema (internal/db) — currently v10

**deployments**: id, repo_dir, owner, repo, mode, watch_filter, max_agents, max_turns, max_budget_usd, runtime, analyzer_model, skip_label, auto_merge, review_enabled, review_max_turns, review_max_budget, total_budget_usd, carried_cost_usd, base_branch, started_at

**jobs**: id, deployment_id, agent, name, issue_number, issue_title, issue_body, owner, repo, status (queued/running/review/reviewing/reviewed/done/bailed/blocked), current_stage, stages_json, result_json, worktree_path, branch, pr_number, cost_usd, agent_log, failure_reason, failure_detail, review_risk, review_comment_id, dependencies, max_turns, max_budget_usd, runtime, model, source_type, source_name, source_ref, queued_at, started_at, completed_at — UNIQUE on (deployment_id, name)

**dep_graphs**: deployment_id (PK), graph_json, option_name, reasoning, confidence, created_at

**lessons**: id, repo_scope, content, source, active, pinned, times_injected, times_helpful, times_unhelpful, superseded_by, last_injected_at, last_helpful_at, last_unhelpful_at, created_at, updated_at

**job_lessons**: job_id, lesson_id (composite PK)

**repo_onboarding**: repo_dir (PK), owner, repo, yaml_content, validation_status, validation_failures, scanned_at

**job_schedules**: name, deployment_id, cron_expr, trigger_expr, agent, description, budget, max_turns, runtime, model, enabled, last_run_at, next_run_at, created_at — PK (deployment_id, name)

Migrations: v1→v2 (tasks→jobs rename, add agent/name/stage columns), v2→v3 (job_schedules table), v3→v4 (UNIQUE constraint change from deployment_id+issue_number to deployment_id+name for proactive agents), v4→v5 (add last_helpful_at/last_unhelpful_at to lessons for decay-weighted scoring), v5→v6 (deployments.runtime), v6→v7 (per-job and per-schedule runtime overrides), v7→v8 (per-job and per-schedule model overrides), v8→v9 (job_schedules PK rescoped from name-only to (deployment_id, name), preserving last-run history), v9→v10 (jobs.source_type/source_name/source_ref for job provenance).

Schema changes go in `internal/db/schema.go`: increment `schemaVersion`, add a migration guard, and never edit an existing migration constant. `TestClaudeMDSchemaVersion` in `internal/supervisor` asserts the version documented above matches the constant.

## Package map

| Package | Purpose | Notes |
|---------|---------|-------|
| `cmd/` | Cobra commands | deploy, status, stop, enroll, lesson, jobs, agents, auth, checkout, logs, reaper, worktrees, tui |
| `internal/supervisor` | Job supervisor | Contracts, context providers, dedup, review, dep graph, bail, stage executor |
| `internal/runtime` | Doer runtime abstraction | `AgentRuntime` interface + registry; `claudecode`, `codex`, `opencode` implementations |
| `internal/daemon` | Deploy daemon | PID files, heartbeat, HTTP API server + client |
| `internal/scheduler` | Job scheduler | Cron parser, `jobs.yaml` config, scheduled job firing |
| `internal/db` | SQLite schema + CRUD | sqlx.DB wrapper, migrations in `schema.go` |
| `internal/claudecli` | Claude Code CLI wrapper | `Completer` interface, `claude -p` for analysis calls |
| `internal/auth` | Credential resolution | OS keyring via go-keyring, env var fallback |
| `internal/picker` | Interactive selection | Terminal pickers for issues, agents, runtimes |
| `internal/reaper` | Stale resource cleanup | Abandoned worktrees, orphaned branches |
| `internal/git` | Git CLI wrappers | `LogSince()`, `Branches()`, `WorktreeList()` |
| `internal/github` | GitHub API client | go-github wrapper, ETag transport, URL parsing |
| `internal/lesson` | Learning system | Lesson selection, injection, grooming |
| `internal/onboarding` | Repo scanning + config | `onboarding.yaml` generation, validator |
| `internal/discovery` | Language/framework detection | `ScanRepo()`, `DeriveProjectName()` |
| `internal/agentutil` | Agent log parsing | `ParseAgentLog()` for stream-json results |
| `internal/sqliteutil` | SQLite health + WAL recovery | `OpenWithRecovery()`, stale -shm/-wal cleanup |

## Commands

- `deploy [issues...] [flags]` — Launch agents on issues or start daemon. Key flags: `--repo`, `--agent`, `--watch`, `--serve`, `--foreground`, `--max-agents`, `--auto-merge`, `--total-budget`.
- `status [deploy-id]` — Deployment status (`--json` for structured output, `--remote host:port` for remote daemon).
- `stop [deploy-id]` — Stop a running deployment (local or `--remote`).
- `enroll [repo-dir]` — Scan repo, generate `onboarding.yaml`, install agent definitions.
- `lesson add|list|edit|remove|pin|groom` — Manage the learning system.
- `jobs list|run` — View and trigger scheduled jobs from `jobs.yaml`.
- `agents list|show <name>` — List available agents or show agent definition details.
- `auth` — Store and check the GitHub token in the OS keyring.
- `checkout <issue>` — Check out an agent's worktree for a job locally.
- `logs <job>` — Tail or dump an agent log.
- `worktrees list|clean` — Inspect and remove agent worktrees.
- `reaper` — Clean up stale worktrees and orphaned branches.

## Testing

```bash
go test ./...                              # All unit tests
go test ./internal/db/... -v               # DB + migration tests
go test ./internal/supervisor/... -v       # Supervisor, contracts, context, dedup, templates
go test ./internal/runtime/... -v          # Runtime registry + claudecode/codex/opencode
go test ./internal/scheduler/... -v        # Cron parser, config, scheduler
go test ./internal/daemon/... -v           # HTTP API endpoints
go test ./internal/lesson/... -v           # Lesson selection + grooming
```

## Debug logging

Structured JSON logging via `log/slog` to `~/.agent-minder/debug.log`, enabled with `MINDER_DEBUG=1`.

- Package-level `debugLogger *slog.Logger` with `slog.NewJSONHandler`; `debugLog(msg, attrs...)` is the logging function
- Every log line has structured attrs: `stage` (gather/tier1/tier2/sweep/broadcast/onboard/publish/reconcile), `step` (start/input/output/skip/error/complete), `component` (git_summarizer/bus_summarizer/analyzer/sweep_haiku/pr_status), `model`, `item`
- Long content in `system_prompt`, `user_prompt`, `response` fields

### Viewing logs

```bash
# Quick watch
tail -f ~/.agent-minder/debug.log | jq '{time, level, msg, stage, step, component}'

# With lnav (color-coded by pipeline stage)
lnav -i lnav/agent-minder.json   # one-time install
lnav ~/.agent-minder/debug.log
```

The `lnav/agent-minder.json` format file ships with the repo. It color-codes stages and hides prompt/response fields (expand with `p` in lnav).

## Key patterns

- Supervisor is long-lived — stays alive after all tasks complete, refills slots on a 30s ticker
- Agent contracts (`.claude/agents/*.md`) declare mode, output, context providers, dedup strategies, and pipeline stages
- Context providers assemble prompt context from declared providers (issue, repo_info, file_list, recent_commits, lessons, sibling_jobs, dep_graph)
- Stage executor iterates declared pipeline stages with conditional routing and context passing
- Dedup engine prevents duplicate work via stackable strategies (branch_exists, open_pr_with_label, recent_run)
- Doer agents run through `internal/runtime`; `internal/claudecli` handles analysis calls only
- SQLite uses single-writer (`SetMaxOpenConns(1)`) to prevent SQLITE_BUSY contention between supervisor, scheduler, and API goroutines

### Where agent guidance lives

Four layers, each owning one thing. Keeping them separate is what stops them contradicting each other:

| Layer | Owns | Lives in |
|-------|------|----------|
| Facts | Architecture, package map, schema, commands, invariants | This file |
| Task context | Issue, repo info, branch, test command, siblings, dep graph | `internal/supervisor/context.go` providers |
| Behavior | How an agent decides, when it stops, what it produces | `.claude/agents/<name>.md` and `AgentTemplates()` bodies |
| House style | Scope, human-facing output format, delegation budget | `renderHouseStyle()` in `internal/supervisor/context.go`, injected into every prompt |

A contract that restates facts from this file, or restates the house style, will drift out of sync — point at them instead. `defaultAgentDef` and `defaultReviewerDef` in `prompt.go` share their bodies with the registry constants for the same reason.

## Environment variables

- `GITHUB_TOKEN` — GitHub API token (required for agent execution)
- `MINDER_DB` — override database path (default: `~/.agent-minder/v2.db`)
- `MINDER_LOG` — override debug log path (default: `~/.agent-minder/debug.log`)
- `MINDER_DEBUG=1` — enable structured JSON debug logging
- `MINDER_API_KEY` — API key for remote daemon access

## Claude Code CLI notes

- Analysis calls use `claude -p --output-format json` via `internal/claudecli`
- `--json-schema` enforces structured output → appears in `structured_output` field (not `result`)
- `--model haiku`/`--model sonnet` aliases work (no need for full model IDs)
- `--tools ""` disables tool use for cheap/fast calls (e.g., tracked item sweep)
- ~10s overhead per `claude -p` call regardless of tools setting
- No API key required — Claude Code CLI handles authentication
