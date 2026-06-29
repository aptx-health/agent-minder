# minder

[![CI](https://github.com/aptx-health/agent-minder/actions/workflows/ci.yml/badge.svg)](https://github.com/aptx-health/agent-minder/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/aptx-health/agent-minder)](go.mod)

A self-hosted orchestration layer for Claude Code. Automatically dispatch the right agent for the job, whether that's a scheduled cron task (dependency updates, security scans, doc syncs) or a reactive trigger from a GitHub label. Tag an issue with `bug` and a bug-fixer agent reproduces it, writes a regression test, and opens a PR. Runs in the foreground for inspection or as a background daemon.

Need to check in on a more complex job? Run `minder checkout` for follow-up options: view logs, restore the worktree for manual testing, resume with a fresh Claude session that has the agent's context preloaded, or jump straight to the PR.

![minder checkout — follow-up options for any agent's work](docs/screenshots/checkout-menu.png)

Powers automated development on [ripit.fit](https://ripit.fit).

![minder checkout — months of agent work across one repo](docs/screenshots/checkout.png)

## Why minder?

If you want to dispatch Claude Code interactively, `claude agents` is great. minder is for the headless half: work that should happen without you typing a prompt.

A few things that make it useful as a devops-minded layer:

- The unit of work is a GitHub issue. Tag one with `bug` or `agent-ready` from anywhere — your phone, an admin panel, a Slack action, a teammate's laptop — and the right agent picks it up. No local CLI required to dispatch.
- Configuration lives in `.agent-minder/jobs.yaml`, checked into the repo. You can grep it, diff it, code-review it, and version it alongside the agent definitions in `.claude/agents/`.
- Custom agents per task type: a `spike` posts research findings as a comment, `autopilot` opens a PR, `bug-fixer` writes a regression test before fixing the code. You tag once; the routing is done.
- Multi-stage pipelines with conditional routing. After an agent opens a PR, a `reviewer` agent grades it (low-risk, needs-testing, or suspect). Low-risk PRs can auto-merge once CI passes.
- `minder enroll` scans the repo, researches its stack and conventions, and generates tailored Claude Code agent definitions in `.claude/agents/`. The generated agents are standard subagents, usable inside or outside minder.
- Self-hosted and open source (MIT). Runs on your laptop in the foreground or as a daemon on a server with a remote client (`minder status --remote host:port`).

## Quick start

```bash
# Install
go install github.com/aptx-health/agent-minder/cmd/minder@latest

# Authenticate (stores token in OS keychain)
minder auth login

# Or set env var
export GITHUB_TOKEN=ghp_...

# Enroll a repo (scans, installs agents, configures jobs)
minder enroll /path/to/repo

# Deploy on issues
minder deploy 42 --repo /path/to/repo --foreground

# For ongoing use: current dir, expose API, foreground visibility, generous budget
minder deploy --repo . --serve :7749 --foreground --max-turns 150 --budget 10 --total-budget 35

# Check status
minder status
```

`--serve :7749` exposes the HTTP API so `minder checkout` and `minder status` can reach the daemon. The budget flags (`--max-turns 150 --budget 10 --total-budget 35`) leave headroom for complex jobs and work well if you're on Claude Max.

## How it works

```
Issues -> Dependency graph -> Parallel agents -> Code review -> Lesson capture -> PR
```

Each agent runs in an isolated git worktree with its own branch. The supervisor manages concurrency, budget limits, and the review pipeline. Lessons from reviewer feedback get folded back into future agent prompts. Everything is tracked in SQLite at `~/.agent-minder/v2.db`.

## Features

### Job scheduler
Define recurring jobs and label triggers in `.agent-minder/jobs.yaml`, similar to a CI/CD workflow file but for autonomous coding work. Reactive triggers (label-based) and scheduled jobs (cron) sit in one file, versioned with the repo:

```yaml
jobs:
  weekly-deps:
    schedule: "0 9 * * 1"          # cron expression
    agent: dependency-updater
    runtime: codex                 # optional; falls back to deployment runtime
    description: "Check for outdated dependencies"
    budget: 3.0

  bug-triage:
    trigger: "label:bug"           # label trigger -> agent
    agent: bug-fixer
    runtime: claude-code           # optional per-trigger override

  spike:
    trigger: "label:spike"
    agent: spike
    description: "Research and discovery"
    budget: 5.0
```

The trigger map prints at startup so you can see exactly what's wired up:

![minder deploy — trigger map and live dispatch on startup](docs/screenshots/triggers.png)

```bash
minder jobs list                   # show schedules
minder jobs run weekly-deps        # manual trigger
```

### Runtime selection

The doer runtime is selected in this order:

1. Job-level `runtime:` in `.agent-minder/jobs.yaml` for scheduled or trigger-created jobs.
2. Explicit `minder deploy --runtime <claude-code|codex>`.
3. Repo default in `.agent-minder/config.yaml`:

```yaml
runtime: codex
```

4. User default in `~/.agent-minder/config.yaml`.
5. Built-in default: `claude-code`.

### Built-in agent types

| Agent | Mode | Output | Default trigger | Description |
|-------|------|--------|-----------------|-------------|
| **autopilot** | reactive | pr | `label:agent-ready` | Implements GitHub issues end-to-end |
| **reviewer** | reactive | pr | (auto) | Reviews PRs, assesses risk, makes fixes |
| **bug-fixer** | reactive | pr | `label:bug` | Reproduces bugs, writes regression tests, fixes |
| **spike** | reactive | issue | `label:spike` | Research and discovery; investigates questions and posts findings |
| **dependency-updater** | proactive | pr | cron | Scans and updates outdated dependencies |
| **security-scanner** | proactive | issue | cron | Runs security audits, reports findings |
| **doc-updater** | proactive | pr | cron | Syncs documentation with code changes |

### Agent contracts
Agents declare their behavior in YAML frontmatter in `.claude/agents/*.md`:

```yaml
---
name: dependency-updater
mode: proactive              # no issue needed
output: pr                   # opens a PR
stages:
  - name: scan
  - name: review
    agent: reviewer
    on_failure: skip
    retries: 1
context:                     # what context to inject
  - repo_info
  - file_list
  - recent_commits:7
  - lessons
dedup:                       # skip if duplicate work exists
  - branch_exists
  - open_pr_with_label:dependencies
  - recent_run:168
timeout: 1h
---

You are a dependency update agent...
```

Contract fields: `mode` (reactive/proactive), `output` (pr/issue/comment/none), `context` (providers), `dedup` (strategies), `stages` (multi-step pipeline), `timeout`.

Each agent type also declares meaningful pipeline stage names (e.g., autopilot's stage is `implement`, bug-fixer's is `fix`, spike's is `research`). Agents without explicit stages fall back to `run`.

### Context providers
Agents get context assembled from declared providers:

| Provider | Description |
|----------|-------------|
| `issue` | Issue title, body, and comments from GitHub |
| `repo_info` | Languages, test/build commands with timeout wrappers, base branch, worktree path |
| `file_list` | Repository file tree (depth 3) |
| `recent_commits:<days>` | Git log from last N days |
| `lessons` | Relevant lessons from the learning system |
| `sibling_jobs` | Other jobs in the same deployment |
| `dep_graph` | Dependency graph for the deployment |

### Multi-agent orchestration
- Up to N concurrent agents (configurable with `--max-agents`)
- Slot backfill: as agents finish, new ones start automatically
- Budget ceiling with 80% warning and automatic pause

### Automated review
After an agent opens a PR, a review agent assesses it:
- `low-risk`: clean, well-tested. Auto-merge eligible.
- `needs-testing`: looks correct but needs human verification.
- `suspect`: has issues requiring human review.

Review produces structured JSON with risk level, summary, lessons, and specific issues found. The extraction call has a 2-minute timeout to prevent hanging on concurrent reviews.

### Learning system
Minder learns from agent outcomes:
- Lessons captured automatically from review findings
- Injected into future agent prompts (~2000 token budget)
- Tracks helpful/unhelpful counts per lesson
- Scoped per repo or globally
- Grooming auto-deactivates stale lessons; LLM consolidation merges overlapping ones

```bash
minder lesson list                        # show all lessons
minder lesson add "Always run tests"      # add manually
minder lesson groom --dry-run             # preview consolidation
```

### Dedup engine
Stackable strategies prevent duplicate work:
- `open_pr` — skip if open PR exists for branch (default for reactive PR agents)
- `branch_exists` — skip if branch already exists on remote
- `open_pr_with_label:<label>` — skip if matching PR is open
- `recent_run:<hours>` — skip if same agent ran recently

Reactive PR agents automatically get `open_pr` dedup to prevent re-running issues across daemon restarts. Unlike `branch_exists`, this allows retries when the agent was interrupted (usage limit, crash) before opening a PR.

### Watch mode
Continuously poll GitHub for new issues matching a filter:

```bash
minder deploy --watch label:agent-ready --serve :7749
minder deploy --watch milestone:v2.0
```

Trigger routes from `jobs.yaml` are polled automatically — `--watch` flag is optional when triggers are configured.

### Daemon mode + HTTP API
Run as a background daemon with a REST API:

```bash
minder deploy 42 55 --serve :7749       # start daemon
curl localhost:7749/status | jq          # check status
curl localhost:7749/jobs | jq            # list jobs
minder stop <deploy-id>                  # stop daemon
```

Endpoints: `/status`, `/jobs`, `/jobs/{id}`, `/jobs/{id}/log`, `/dep-graph`, `/metrics`, `/lessons`, `/stop`, `/resume`.

### Usage limit recovery
When an agent hits a Claude Code session/usage limit, minder automatically:
1. Detects the limit (via stream events or error text patterns)
2. Sets job status to `waiting`
3. Sleeps with backoff (1h, 2h, 3h)
4. Resumes the session using `--resume <session_id>`
5. Up to 3 retry attempts before bailing

No human intervention needed.

### Command timeout wrappers
Test and build commands injected into agent context include `timeout` wrappers (default 5m for tests, 3m for builds). Configurable via `test_timeout` and `build_timeout` in `onboarding.yaml`. Prevents agents from burning turns waiting on hung processes.

### Orphan process reaper
Agent runs occasionally leave detached descendants alive in the worktree — `npm start`, `next dev`, `overmind` and similar processes call `setsid` and escape their parent's process group, so they survive the agent that spawned them. The supervisor sweeps every worktree for these orphans and kills them (SIGTERM, then SIGKILL after a 2-second grace) at three points:

- after every agent process exits (scoped to that one worktree)
- at supervisor startup (across all worktrees whose jobs are not currently running)
- on a 5-minute ticker while the supervisor is running

Each kill emits one event line in foreground mode (e.g. `[reaper] job#265 issue-699: killed pid=39076 ppid=1 cmd=next-server age=20m sig=KILL`) and a structured `component=reaper` record in `debug.log`. Active agents' processes are never touched — the sweep skips any job in `running` state. For manual cleanup or testing: `minder reaper sweep <worktree-path>`.

## SwiftBar menu bar plugin

A macOS menu bar widget that shows agent status at a glance. Supports all job statuses including `waiting` (usage limit recovery). See `xbar/minder.5s.sh`.

## Commands

| Command | Description |
|---------|-------------|
| `minder deploy [issues...] [flags]` | Launch agents on issues or start daemon |
| `minder status [deploy-id]` | Show deployment status (`--json` for structured output) |
| `minder stop [deploy-id]` | Stop a running deployment |
| `minder tui` | Launch interactive TUI dashboard |
| `minder auth login\|status\|logout` | Manage GitHub token in OS keychain |
| `minder lesson add\|list\|edit\|remove\|pin\|groom` | Manage the learning system |
| `minder jobs list\|run` | View and trigger scheduled jobs |
| `minder agents list\|show\|add` | List, inspect, or create agent definitions |
| `minder checkout [issue]` | Check out an agent's worktree for review (interactive picker, `--remote`) |
| `minder logs [issue]` | View agent log output (interactive picker, `--follow`, `--remote`, `--raw`) |
| `minder enroll [repo-dir]` | Scan a repo and generate onboarding config |

### Deploy flags

| Flag | Default | Description |
|------|---------|-------------|
| `--repo <dir>` | `.` | Repository directory |
| `--agent <name>` | `autopilot` | Agent type to use |
| `--runtime <name>` | hierarchy | Doer runtime override (`claude-code` or `codex`) |
| `--watch <filter>` | — | Watch for issues (`label:<name>` or `milestone:<name>`) |
| `--serve <addr>` | — | Start HTTP API (e.g., `:7749`) |
| `--foreground` | — | Don't daemonize |
| `--max-agents <n>` | `3` | Concurrent agent slots |
| `--max-turns <n>` | `50` | Per-job turn limit |
| `--budget <usd>` | `5.00` | Per-job budget |
| `--total-budget <usd>` | `25.00` | Total deployment budget |
| `--auto-merge` | — | Auto-merge low-risk PRs (waits for CI) |
| `--base-branch <name>` | auto-detect | Base branch for worktrees/PRs |
| `--api-key <key>` | — | Require API key for HTTP access |

## Prerequisites

| Dependency | Purpose |
|-----------|---------|
| **Go 1.25+** | Build from source |
| **git** | Worktree management, branch operations |
| **[Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code)** | Agent execution (`claude --agent`) |
| **`GITHUB_TOKEN`** | GitHub API access (via env var or `minder auth login`) |
| **[gh CLI](https://cli.github.com/)** | Agents use `gh` for PR creation and issue management |

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `GITHUB_TOKEN` | — | GitHub API token (or use `minder auth login`) |
| `MINDER_DB` | `~/.agent-minder/v2.db` | Database path |
| `MINDER_LOG` | `~/.agent-minder/debug.log` | Debug log path |
| `MINDER_DEBUG` | — | Enable structured JSON debug logging |
| `MINDER_API_KEY` | — | API key for remote daemon access |

## Development

### Testing

```bash
go test ./...                              # all tests
go test ./internal/db/... -v               # DB + migrations
go test ./internal/supervisor/... -v       # supervisor, contracts, context, dedup
go test ./internal/scheduler/... -v        # cron parser, config, scheduler
go test ./internal/daemon/... -v           # HTTP API endpoints
```

### Debug logging

```bash
MINDER_DEBUG=1 minder deploy 42 --foreground

# Watch in another terminal
tail -f ~/.agent-minder/debug.log | jq '{time, msg, agent, issue}'
```

### Agent logs

Each agent run produces a stream-json log:

```bash
# List logs
ls ~/.agent-minder/agents/

# Watch a running agent
tail -f ~/.agent-minder/agents/<deploy-id>-issue-<N>.log | \
  jq -r 'if .type == "assistant" then (.message.content[]? |
    if .type == "tool_use" then "🔧 \(.name)" else empty end)
  else empty end'
```

## Architecture

```
cmd/minder/main.go          # Entry point
cmd/                         # Cobra commands (deploy, status, stop, lesson, jobs, agents, auth, enroll, tui)
internal/
  supervisor/                # Job manager, context providers, contracts, dedup, review, bail, templates
  scheduler/                 # Cron parser, jobs.yaml, scheduled job firing
  daemon/                    # PID files, heartbeat, HTTP API server + client
  db/                        # SQLite schema (v4), models, queries, migrations
  claudecli/                 # Claude Code CLI wrapper
  github/                    # GitHub API client (go-github, ETag caching)
  git/                       # Git CLI wrappers
  auth/                      # OS keyring integration (macOS Keychain, Linux libsecret)
  lesson/                    # Lesson selection, injection, grooming
  onboarding/                # Repo scanning, onboarding YAML
  discovery/                 # Language/framework detection
  agentutil/                 # Agent log parsing
  sqliteutil/                # WAL recovery
xbar/                        # macOS SwiftBar menu bar plugin
.agent-minder/               # Per-repo config (jobs.yaml, onboarding.yaml)
```

### Data storage

SQLite at `~/.agent-minder/v2.db` (WAL mode, foreign keys, single-writer via `SetMaxOpenConns(1)`). Schema v4.

| Table | Purpose |
|-------|---------|
| `deployments` | Deploy runs with config (agents, budget, model, base branch) |
| `jobs` | Work queue (agent, status, worktree, PR, cost, stages, results) |
| `job_schedules` | Cron schedules with last/next run tracking |
| `dep_graphs` | LLM-generated dependency graphs |
| `lessons` | Persistent feedback with effectiveness tracking |
| `job_lessons` | Which lessons were injected into which jobs |
| `repo_onboarding` | Cached repo scanning results |
