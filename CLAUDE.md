# agent-minder

Go CLI coordination layer on top of [agent-msg](../agent-msg). Monitors git repos, watches the message bus, and uses Claude Code CLI for LLM analysis to coordinate agents.

## Quick orientation

- **Module**: `github.com/dustinlange/agent-minder`
- **Go version**: 1.25+ (bubbletea v2 requirement)
- **State**: SQLite at `~/.agent-minder/minder.db` (WAL mode, foreign keys)
- **agent-msg DB**: `~/repos/agent-msg/messages.db` (or `AGENT_MSG_DB` env var)
- **LLM**: Claude Code CLI (`claude -p`) — no API key needed

## Architecture

### LLM pipeline (internal/poller)

All LLM calls go through `internal/claudecli`, which wraps `claude -p` with `--output-format json`. The `claudecli.Completer` interface is the single abstraction for all LLM interactions.

Each poll cycle with new activity runs a single `claude -p --model <analyzer_model>` call with `--json-schema` to enforce structured output:

```json
{
  "analysis": "status update text",
  "concerns": [{"severity": "warning", "message": "..."}],
  "bus_message": {"topic": "project/coord", "message": "..."}
}
```

Raw git commits and bus messages are passed directly to the analyzer (no separate summarization step). The model is controlled by `project.LLMAnalyzerModel` (default: `"sonnet"`).

- `parseAnalysis()` in `analysis.go` handles raw JSON, markdown-fenced JSON, and plain text fallback
- Concerns are managed by the analyzer: each cycle returns the full desired concern list, `reconcileConcerns()` diffs against existing (adds new, resolves dropped, updates severity)
- Concern severity levels: `info`, `warning`, `danger` — color-coded in TUI
- Bus messages are published via `msgbus.Publisher` only when the analyzer includes `bus_message`

### Bus integration (internal/msgbus)

- `Client` — read-only connection (`?mode=ro`) for polling messages
- `Publisher` — read-write connection for publishing messages back to agent-msg; supports `PublishReplace()` for single-message topics (e.g., onboarding)
- Both work against the same `messages.db` file; agent-msg bash scripts remain compatible
- Both use `sqliteutil.OpenWithRecovery()` to auto-detect and recover from stale WAL/SHM files

### TUI (internal/tui)

**UX patterns: see [TUI-UX-GUIDE.md](TUI-UX-GUIDE.md)** — all new/modified interactions must follow those conventions.

Bubbletea v2 dashboard. Key bindings: `p` pause, `r` poll now, `e` expand, `u` user msg, `m` broadcast, `o` onboard, `a` autopilot, `A` stop autopilot (with confirmation), `t` theme, `q` quit.

- Spinner (bubbles/v2/spinner MiniDot) shown during manual poll (`r`), broadcast, and onboard generation
- Concerns capped at 5 displayed with "+N more" indicator; color-coded by severity (info=muted, warning=amber, danger=bold red)
- Event log dynamically sized to remaining terminal height
- Broadcast mode: `m` opens textarea, ctrl+d sends through tier 2 LLM → publishes to bus
- Onboard mode: `o` opens textarea for optional guidance, ctrl+d generates onboarding message via tier 2 LLM → publishes to `<project>/onboarding` with replace semantics
- Autopilot modes: `""` (normal) → `"confirm"` (y/n to launch) → `"running"` (slot status displayed) → `"stop-confirm"` (y/n to stop)
- During autopilot, poll frequency is halved (min 30s) and `StatusBlock()` is injected into tier 2 analyzer input

### Autopilot (internal/autopilot)

Supervisor manages N concurrent Claude Code agents working on GitHub issues in isolated worktrees.

**Flow:** `a` in TUI → `Prepare()` (checks for existing tasks, offers keep/rebuild if found, else fresh scan) → user selects dep graph option → `Launch()` fills slots with unblocked tasks → agents run `claude -p` → inspect outcome → clean up → refill slots.

**Task lifecycle:** `queued` → `running` → `review` (PR opened) → `done` (PR merged) | `bailed` (no PR, agent gave up)

**Key behaviors:**
- `Prepare()` preserves existing tasks across restarts — if tasks exist, shows reprepare-choice (k=keep, r=rebuild); selected dep graph stored in `autopilot_dep_graphs` table
- On reprepare keep: stale running → queued, everything else preserved; new tracked items discovered and analyzed incrementally with reverse dep injection
- On reprepare rebuild: done stays done, review/failed/bailed/stopped/running → manual, queued/blocked → cleared; full dep graph regenerated
- Issues with the skip label (default `no-agent`, configurable via `project.AutopilotSkipLabel`) are excluded
- Dependency graph built via one LLM call using analyzer model; includes all tracked items as context for cross-repo deps
- External dependency blocking: `QueuedUnblockedTasks()` cross-references `tracked_items` — if a dep is tracked and open, it blocks
- New task discovery: handled during ReprepareKeep — new tracked items get incremental dep analysis with reverse dependency injection (existing queued/blocked tasks can gain deps on new issues)
- Review check: every 30s, checks if PRs for `review` tasks have been merged → promotes to `done`
- Label management: agent adds `in-progress` on start; supervisor swaps to `needs-review` when PR detected; removes `needs-review` when merged
- On restart, `Prepare()` clears all tasks and rebuilds — no resume of stale state
- Stop confirmation: `A` → "Stop all running agents? (y/n)" — waits for agent processes to exit naturally

**Paths:**
- Worktree: `~/.agent-minder/worktrees/<project>/issue-<N>`, branch: `agent/issue-<N>`
- Agent logs: `~/.agent-minder/agents/<project>-issue-<N>.log`

**Agent command:** `claude --agent autopilot -p --max-turns <N> --max-budget-usd <B> --allowedTools <tool> ... "<prompt>"` with `GITHUB_TOKEN` env var. Allowed tools are loaded from `.agent-minder/onboarding.yaml` (if present) or a built-in default set.

### DB schema (internal/db) — currently v21

**projects**: name, goal_type, goal_description, refresh_interval_sec, message_ttl_sec, auto_enroll_worktrees, minder_identity, llm_provider (deprecated), llm_model (deprecated), llm_summarizer_model, llm_analyzer_model, autopilot_max_agents, autopilot_max_turns, autopilot_max_budget_usd, autopilot_skip_label

**polls**: project_id, new_commits, new_messages, concerns_raised, llm_response (legacy), tier1_response, tier2_response, bus_message_sent, polled_at

**autopilot_tasks**: project_id, issue_number, issue_title, issue_body, dependencies (JSON), status (queued/running/done/bailed/blocked/manual/skipped), worktree_path, branch, pr_number, agent_log, started_at, completed_at, max_turns_override (nullable), max_budget_override (nullable) — UNIQUE on project_id+issue_number

**autopilot_dep_graphs**: project_id (UNIQUE), graph_json, option_name, created_at — persists the selected dependency graph across autopilot restarts

**completed_items**: project_id, source, owner, repo, number, item_type, title, final_status, summary, completed_at — archived from tracked_items when they reach terminal state (only if progress_summary was non-empty)

**Also**: repos, worktrees, topics, concerns (see `schema.go` for full DDL)

Migrations: v1→v2 (two-tier LLM columns), v3 (tracked_items), v4 (content hash + summaries), v5 (idle_pause_sec), v6 (is_draft + review_state), v7 (completed_items), v8 (analyzer_focus), v9 (autopilot_tasks table + autopilot project columns), v18 (deprecate llm_provider/llm_model columns), v20 (autopilot_dep_graphs table for persisting dep graphs), v21 (per-task max_turns_override + max_budget_override columns).

`Poll.LLMResponse()` accessor returns tier2 > tier1 > raw (backward compat).

## Package map

| Package | Purpose | Notes |
|---------|---------|-------|
| `internal/autopilot` | Autopilot supervisor | Manages concurrent Claude Code agents on GitHub issues |
| `cmd/` | Cobra commands | init, start, status, enroll, pause, resume |
| `internal/db` | SQLite schema + CRUD | `Store` wraps sqlx.DB, migrations in `schema.go` |
| `internal/claudecli` | Claude Code CLI wrapper | `Completer` interface, `claude -p` invocation |
| `internal/poller` | Poll loop + LLM pipeline | `poller.go`, `analysis.go` (parsing + dedup) |
| `internal/tui` | Bubbletea dashboard | `app.go` (model/update/view), `styles.go` (themes) |
| `internal/git` | Git CLI wrappers | `LogSince()`, `Branches()`, `WorktreeList()` |
| `internal/discovery` | Repo scanning | `ScanRepo()`, `DeriveProjectName()`, `SuggestTopics()` |
| `internal/sqliteutil` | SQLite health + WAL recovery | `OpenWithRecovery()`, stale -shm/-wal cleanup |
| `internal/msgbus` | Agent-msg client + publisher | Read-only `Client`, read-write `Publisher` + `PublishReplace()` |
| `internal/config` | Global config + credentials | Viper-based YAML config, keychain integration via `internal/secrets` |

## Commands

- `init <repo-dir> [...]` — Interactive wizard → SQLite. Sets goal, topics, poll interval, LLM models.
- `start <project>` / `resume <project>` — TUI + poller. Creates publisher for bus writes.
- `status <project>` — CLI text summary, no LLM call.
- `enroll <project> <repo-dir>` — Add repo to project.

## Testing

```bash
go test ./...                           # All unit tests
go test ./internal/db/... -v            # DB + migration tests
go test ./internal/poller/... -v        # Analysis parsing + concern dedup
go test ./internal/msgbus/... -v        # Client + publisher tests

# Integration test (requires Claude Code CLI + existing agent-test project):
go test -tags integration -run TestIntegrationAnalysisPipeline -v ./internal/poller/ -timeout 120s
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

- `Poller.doPoll()` is the main loop body — gathers git + bus data, runs single analysis call via `claudecli.Completer`, publishes, records
- `Poller.Broadcast()` is the user-initiated broadcast path — gathers context, calls completer, publishes
- `Poller.Onboard()` generates onboarding messages — gathers rich context, calls completer, publishes to `<project>/onboarding` with replace semantics
- All TUI async operations use bubbletea Cmd pattern (return `func() tea.Msg`), not raw goroutines
- Spinner ticks flow through the standard bubbletea Update loop via `spinner.TickMsg`
- Theme is global mutable state (package-level `themeIndex`), cycled via `cycleTheme()`

## Useful shortcuts

```bash
# Recreate a completed agent's worktree (branch must still exist — works for review tasks)
git worktree add ~/.agent-minder/worktrees/<project>/issue-<N> agent/issue-<N>

# Shell helper
minder-checkout() {
  git worktree add ~/.agent-minder/worktrees/$1/issue-$2 agent/issue-$2
  cd ~/.agent-minder/worktrees/$1/issue-$2
}
# Usage: minder-checkout minder-improvement 65
```

## Testing in worktrees

Use `scripts/test-env.sh` to run an isolated instance against its own DB and log file:

```bash
source scripts/test-env.sh <project-name>
go run . start "$MINDER_PROJECT"
```

This auto-derives paths from the branch name (e.g., `agent/issue-65` → `~/.agent-minder/minder-agent-issue-65.db`), copies the production DB on first run, and enables debug logging.

**Environment variables:**
- `MINDER_DB` — override database path (default: `~/.agent-minder/minder.db`)
- `MINDER_LOG` — override debug log path (default: `~/.agent-minder/debug.log`)
- `MINDER_DEBUG=1` — enable structured JSON debug logging

## Claude Code CLI notes

- All LLM calls use `claude -p --output-format json` via `internal/claudecli`
- `--json-schema` enforces structured output → appears in `structured_output` field (not `result`)
- `--model haiku`/`--model sonnet` aliases work (no need for full model IDs)
- `--tools ""` disables tool use for cheap/fast calls (e.g., tracked item sweep)
- ~10s overhead per `claude -p` call regardless of tools setting
- No API key required — Claude Code CLI handles authentication
