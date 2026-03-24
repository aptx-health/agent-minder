# agent-minder

A Go CLI tool that coordinates AI agents working across multiple repositories. It monitors git activity, watches the [agent-msg](https://github.com/aptx-health/agent-msg) message bus, tracks GitHub issues/PRs, and uses Claude Code CLI for LLM analysis to keep agents informed and dispatch automated work.

Built as a coordination layer on top of agent-msg's simple bash scripts + SQLite foundation.

## How it works

```
agent-minder start <project>
```

Launches a TUI dashboard with three tabs:

```
┌──────────────────────────────────────────────────────────────┐
│ agent-minder: myproject  RUNNING  [dark]                      │
│ [1 Operations]  [2 Analysis]  [3 Autopilot]                  │
├──────────────────────────────────────────────────────────────┤
│                                                               │
│ Tracked Items (3)                                             │
│   #42 [Open]  Add auth middleware          repo/app           │
│   #18 [Mrgd]  Fix DB connection pool       repo/infra         │
│   #7  [Revw]  Update API docs              repo/app           │
│                                                               │
│ Worktrees                                                     │
│   app (/Users/me/repos/myproject-app)                         │
│   infra (/Users/me/repos/myproject-infra)                     │
│                                                               │
│ Event Log                                                     │
│   [14:32:05] poll: 3 new commits, 1 new message               │
│   [14:27:01] poll: No new activity                            │
│                                                               │
│ ?: help  p: pause  r: sync  s: settings  t: theme  q: quit   │
└──────────────────────────────────────────────────────────────┘
```

### Tab 1: Operations

Tracked items, worktree status, and event log. Press `r` to trigger a sync (gather git/bus data without LLM analysis). Track and untrack GitHub issues/PRs with `i`/`I`.

### Tab 2: Analysis

LLM analysis of project activity. Press `r` to run a full analysis cycle. The analyzer receives git commits, bus messages, tracked item changes, and project context, then produces a markdown summary with concerns and optional bus messages. Press `e` to expand/collapse the analysis view.

- `u` — User message: query the analyzer directly
- `m` — Broadcast: LLM crafts and publishes a coordination message
- `o` — Onboard: generate an onboarding message for new agents

### Tab 3: Autopilot

Automated agent management. Press `a` to launch autopilot, which converts tracked GitHub issues into a task queue, builds a dependency graph, and dispatches concurrent Claude Code agents to work on unblocked issues in isolated git worktrees.

## Supported platforms

| OS | Architecture | Status |
|----|-------------|--------|
| macOS | amd64 / arm64 | Supported (primary) |
| Linux | amd64 / arm64 | Supported |
| Windows | amd64 | Supported (native or WSL2) |

## Prerequisites

### Required

| Dependency | Min version | Purpose | Install |
|-----------|-------------|---------|---------|
| **Go** | 1.25+ | Build from source (bubbletea v2) | [go.dev/dl](https://go.dev/dl/) |
| **git** | 2.x | Repository monitoring, worktree management | [git-scm.com](https://git-scm.com/) |
| **[claude](https://docs.anthropic.com/en/docs/claude-code)** (Claude Code CLI) | Latest | All LLM calls (`claude -p`), autopilot agents | [docs.anthropic.com](https://docs.anthropic.com/en/docs/claude-code) |

No API keys are needed — the Claude Code CLI handles authentication.

### Required for specific features

| Dependency | Feature | What happens if missing |
|-----------|---------|------------------------|
| **`GITHUB_TOKEN`** or **`GH_TOKEN`** | Tracked items (issue/PR sweep), autopilot | `track`/`untrack` and the sweep pipeline are unavailable. Autopilot refuses to start. Token can be stored in the OS keychain via `agent-minder setup`. |
| **[gh](https://cli.github.com/)** (GitHub CLI) | Autopilot agent operations | Agents use `gh` to create PRs, post comments, manage labels. Not needed for non-autopilot usage. |

### Optional

| Dependency | Feature | What happens if missing |
|-----------|---------|------------------------|
| **[agent-msg](https://github.com/aptx-health/agent-msg)** | Inter-agent message bus | Bus features (broadcast, onboard, user messages) are unavailable. Polling, git monitoring, and GitHub sweeps continue normally. |
| **jq** / **lnav** | Debug log viewing | Structured log output and color-coded log viewing. `tail -f` works as a simpler alternative. |

### OS keychain

agent-minder uses [go-keyring](https://github.com/zalando/go-keyring) to store tokens securely in the OS-native keychain. The keychain is **optional** — if unavailable, credentials fall back to the config file or environment variables.

| Platform | Backend | Notes |
|----------|---------|-------|
| macOS | Keychain (Security framework) | Built-in |
| Linux | Secret Service API (D-Bus) | Requires `gnome-keyring` or `kwallet` |
| Windows | Windows Credential Manager | Built-in |

## Installation

```bash
go install github.com/dustinlange/agent-minder@latest
```

## Commands

### `agent-minder init <repo-dir> [repo-dir ...]`

Interactive wizard that bootstraps a new project: scans repos, derives a project name, selects a goal type, suggests bus topics, configures poll interval and LLM models, and writes everything to SQLite.

### `agent-minder start <project>` / `resume <project>`

Launches the TUI dashboard + polling loop.

### `agent-minder status <project>`

Quick CLI summary from SQLite — no LLM call.

### `agent-minder list`

List all configured projects.

### `agent-minder enroll <project> <repo-dir>`

Add a repo to an existing project.

### `agent-minder repo enroll` / `repo status` / `repo refresh`

Guided repo enrollment wizard with onboarding file generation, enrollment status checks, and re-scanning.

### `agent-minder track <project> <owner/repo> <number> [...]`

Track GitHub issues or PRs for monitoring.

### `agent-minder untrack <project> <owner/repo> <number> [...]`

Stop tracking GitHub issues or PRs.

### `agent-minder setup`

Configure credentials and integrations (GitHub token via keychain or config file).

### `agent-minder delete <project>`

Delete a project and all its associated data.

### `agent-minder deploy [issue#] [...]`

Launch agents on specific GitHub issues as a background daemon. Infers repo from the current working directory.

| Flag | Default | Description |
|------|---------|-------------|
| `--max-agents N` | min(issues, 5) | Max concurrent agents |
| `--max-turns N` | 50 | Max turns per agent |
| `--max-budget USD` | 3.00 | Budget per agent |
| `--dry-run` | — | Show plan without launching |
| `--project NAME` | — | Inherit settings from existing project |
| `--serve :PORT` | — | Enable HTTP API server |
| `--remote HOST:PORT` | — | Query remote daemon |
| `--api-key KEY` | — | API key for HTTP auth |

**Subcommands:** `deploy list`, `deploy status <id>`, `deploy open <id>`, `deploy stop <id>`, `deploy respawn <id>`, `deploy watch <id>`

### `agent-minder version`

Show version information.

## TUI keybindings

### Global

| Key | Action |
|-----|--------|
| `1` / `2` / `3` | Switch tabs |
| `Tab` / `Shift+Tab` | Cycle tabs |
| `s` | Open settings |
| `t` | Cycle theme (dark/light) |
| `p` | Pause/resume polling |
| `?` | Toggle help overlay |
| `d` | Dismiss warning banner |
| `q` / `Ctrl+C` | Quit |

### Operations tab

| Key | Action |
|-----|--------|
| `r` | Sync now (gather git/bus data, no LLM) |
| `i` | Track issues/PRs |
| `I` | Untrack issues/PRs |
| `x` | Expand/collapse tracked items |
| `w` | Toggle worktree panel |
| `f` | Filter events |

### Analysis tab

| Key | Action |
|-----|--------|
| `r` | Run full analysis |
| `e` | Expand/collapse analysis |
| `u` | User message (query analyzer) |
| `m` | Broadcast (LLM-crafted message to bus) |
| `o` | Onboard (generate onboarding message) |

### Autopilot tab

| Key | Action |
|-----|--------|
| `a` | Launch autopilot |
| `A` | Stop all agents (with confirmation) |
| `S` | Stop selected agent |
| `r` | Context-dependent: restart (failed/bailed/stopped), review (review/reviewed), spin off (manual) |
| `b` | Bump task resource limits (1.5x) |
| `c` | Copy worktree path to clipboard |
| `+` | Add agent slot |
| `P` | Pause/resume slot filling |
| `D` | Show dependency info for selected task |
| `G` | Rebuild dependency graph |
| `l` | View agent log |
| `e` | Expand/collapse task list |
| `i` | Toggle failure detail (on failed tasks) |

### Settings (`s`)

Configurable via the in-TUI settings dialog:

| Setting | Description |
|---------|-------------|
| Sync interval | How often status checks run (minutes) |
| Analyzer focus | Custom instructions for the analyzer's perspective |
| Autopilot max agents | Max concurrent agents |
| Autopilot max turns | Max turns per agent |
| Autopilot max budget | Max budget per agent (USD) |
| Autopilot skip label(s) | Comma-separated labels to exclude from autopilot |
| Autopilot base branch | Base branch for worktrees/PRs (empty = auto-detect) |
| Review max turns | Max turns per review agent (empty = reviews disabled) |
| Review max budget | Max budget per review agent (USD) |
| Auto-merge | Auto-merge low-risk PRs after review |

## Architecture

### LLM pipeline

All LLM calls go through `internal/claudecli`, which wraps `claude -p --output-format json`. No API keys needed — Claude Code CLI handles authentication.

Each poll cycle with new activity runs a single analysis call. The analyzer receives git commits, bus messages, tracked item changes, and project context, then returns structured JSON:

```json
{
  "analysis": "status update text",
  "concerns": [{"severity": "warning", "message": "..."}],
  "bus_message": {"topic": "project/coord", "message": "..."}
}
```

The analyzer uses persistent sessions — it resumes context from the previous poll rather than starting fresh each time. Model defaults to `sonnet` (configurable per project).

Concerns are managed by the analyzer: each cycle returns the full desired list, `reconcileConcerns()` diffs against existing (adds new, resolves dropped, updates severity). Severities: `info`, `warning`, `danger`.

### GitHub item sweep

Tracked items (issues/PRs) are swept each poll cycle:
- Fetches current status from GitHub API
- Detects state changes (open → closed, draft → ready, review state changes)
- Archives completed items to `completed_items` table
- Status tags: Open, Closd, Mrgd, Draft, Revw, Blkd

### Bus integration

Reads from and writes to the agent-msg SQLite database:
- **Client** — Read-only connection for polling messages
- **Publisher** — Read-write connection; supports `PublishReplace()` for single-message topics
- Both use `sqliteutil.OpenWithRecovery()` for stale WAL/SHM recovery
- agent-msg bash scripts remain fully compatible

### Autopilot

Press `a` to launch autopilot. The supervisor:
1. Scans tracked GitHub issues, excluding those with skip labels (default: `no-agent`)
2. Builds a dependency graph via one LLM call
3. Dispatches up to N concurrent Claude Code agents to work on unblocked issues
4. Each agent runs in an isolated git worktree
5. Agents either open a draft PR or bail with a comment explaining what they found
6. Supervisor monitors slots, refills as agents complete, discovers new issues

**Task lifecycle:**

```
queued → running → review → done (PR merged)
                 → bailed (agent gave up)
                 → failed (error, can resume/restart)
         blocked (waiting on dependencies)
         manual  (human-driven, spin off worktree with r)
         skipped (has exclusion label)
```

**Review automation:**
- When a task reaches `review`, supervisor checks every 30s if the PR was merged
- Press `r` to launch a review session: restores worktree, pre-loads PR context
- Review agents assess risk: `low-risk`, `needs-testing`, `suspect`
- Optional auto-merge for low-risk PRs (configurable)
- States: `review` → `reviewing` (agent active) → `reviewed` (assessment complete)

**Manual tasks:**
- Issues with skip/in-progress/needs-review/blocked labels become manual tasks
- Press `r` to spin off a worktree with preloaded context (dep graph, issue details)
- Branch convention: `manual/issue-<N>` (vs `agent/issue-<N>` for autopilot)

**Webhook notifications:**
- Configure via project settings (webhook URL, format, event filter)
- Supports Slack and generic JSON formats
- Events: task.started, task.completed, task.bailed, task.failed, task.stopped, task.discovered, autopilot.finished

#### Agent definition (optional)

Autopilot supports [Claude Code agent definitions](https://docs.anthropic.com/en/docs/claude-code/sub-agents) for consistent behavioral guidance. Install in a target repo or globally:

```bash
# Per-repo
cp agents/autopilot.md <your-repo>/.claude/agents/autopilot.md

# Globally
cp agents/autopilot.md ~/.claude/agents/autopilot.md
```

Agent definitions are **additive, never required** — autopilot works without them using built-in prompts. See [agents/README.md](agents/README.md) for details.

#### Skipping issues with labels

Issues labeled with skip labels (default: `no-agent`) are excluded from autopilot. Configure one or more skip labels (comma-separated) via the TUI settings (`s`).

### Data storage

All state in SQLite at `~/.agent-minder/minder.db` (WAL mode, foreign keys). Schema version: **v25**.

Key tables:

| Table | Purpose |
|-------|---------|
| projects | Name, goal, LLM config, poll settings, autopilot config, webhook config |
| repos / worktrees | Git repositories and worktrees per project |
| topics | Message bus topics to monitor |
| concerns | Active/resolved alerts (severity: info/warning/danger) |
| polls | Poll history with analysis responses |
| tracked_items | GitHub issues/PRs with content hash, summaries, draft/review state |
| completed_items | Archived terminal items |
| autopilot_tasks | Issue work queue (status, deps, worktree, PR, cost, review risk) |
| autopilot_dep_graphs | Persisted dependency graphs |

## Project structure

```
agent-minder/
├── agents/
│   ├── autopilot.md       # Agent definition for implementation
│   ├── reviewer.md        # Agent definition for PR review
│   └── onboarding.md      # Agent definition for onboarding
├── cmd/                    # Cobra commands
│   ├── init.go, start.go, status.go, list.go
│   ├── enroll.go, track.go, untrack.go
│   ├── setup.go, delete.go, repo.go
│   ├── deploy.go, deploy_*.go
│   └── version.go
├── internal/
│   ├── api/                # Remote daemon HTTP API (client + server)
│   ├── autopilot/          # Supervisor, prompt generation, agent lifecycle
│   ├── claudecli/          # Claude Code CLI wrapper (claude -p)
│   ├── config/             # Viper config + keychain credential management
│   ├── db/                 # SQLite schema (v25), migrations, CRUD
│   ├── deploy/             # Deployment daemon paths and helpers
│   ├── discovery/          # Repo scanning, project name derivation
│   ├── git/                # Git CLI wrappers (log, branches, worktrees)
│   ├── github/             # GitHub API client (go-github)
│   ├── msgbus/             # Agent-msg client (read) + publisher (write)
│   ├── notify/             # Webhook notifications (Slack + generic)
│   ├── onboarding/         # Repo onboarding YAML (inventory, permissions)
│   ├── poller/             # Poll loop, analysis parsing, item sweep
│   ├── secrets/            # Keychain integration
│   ├── sqliteutil/         # SQLite health + WAL recovery
│   └── tui/                # Bubbletea v2 dashboard (3 tabs, settings, themes)
├── docs/
│   └── automated-agents-design.md
├── scripts/
│   └── test-env.sh         # Isolated test environment for worktrees
├── lnav/
│   └── agent-minder.json   # lnav format for debug logs
└── main.go
```

## Dependencies

- [bubbletea v2](https://charm.land/bubbletea) + [lipgloss v2](https://charm.land/lipgloss) + [bubbles v2](https://charm.land/bubbles) — TUI framework
- [go-github](https://github.com/google/go-github) — GitHub API client
- [sqlx](https://github.com/jmoiron/sqlx) + [modernc.org/sqlite](https://modernc.org/sqlite) — Database (pure Go, no CGo)
- [cobra](https://github.com/spf13/cobra) + [viper](https://github.com/spf13/viper) — CLI + config
- [go-keyring](https://github.com/zalando/go-keyring) — OS keychain integration

## Testing

```bash
go test ./...                           # All unit tests
go test ./internal/db/... -v            # DB + migration tests
go test ./internal/poller/... -v        # Analysis parsing, concern dedup, sweep
go test ./internal/autopilot/... -v     # Autopilot supervisor, prompts, lifecycle
go test ./internal/msgbus/... -v        # Client + publisher tests
```

## Debug logging

Structured JSON logging via `log/slog` to `~/.agent-minder/debug.log`, enabled with `MINDER_DEBUG=1`.

```bash
# Quick watch
tail -f ~/.agent-minder/debug.log | jq '{time, level, msg, stage, step, component}'

# With lnav (color-coded by pipeline stage)
lnav -i lnav/agent-minder.json   # one-time install
lnav ~/.agent-minder/debug.log
```

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `MINDER_DB` | `~/.agent-minder/minder.db` | Database path |
| `MINDER_LOG` | `~/.agent-minder/debug.log` | Debug log path |
| `MINDER_DEBUG` | (unset) | Enable structured JSON debug logging |
| `AGENT_MSG_DB` | `~/repos/agent-msg/messages.db` | Agent-msg database path |
| `GITHUB_TOKEN` / `GH_TOKEN` | (from keychain/config) | GitHub API token |

## agent-msg integration

agent-msg is **optional**. Without it, agent-minder still monitors git repos, tracks GitHub items, runs LLM analysis, manages autopilot — you just won't have inter-agent messaging.

When available, agent-minder reads and writes to agent-msg's SQLite database. agent-msg bash scripts (`agent-pub`, `agent-check`, `agent-ack`, `agent-topics`) remain fully compatible.
