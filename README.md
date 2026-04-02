# minder

A self-hosted agent orchestration tool. Dispatches Claude Code agents to work on GitHub issues in parallel, reviews their output, learns from results, and manages the full lifecycle from issue to merged PR.

## What it does

```bash
minder deploy 42 55 60 --repo . --foreground     # work on specific issues
minder deploy --watch label:agent-ready           # auto-pick up labeled issues
minder deploy --serve :7749 --auto-merge          # daemon with API + auto-merge
minder deploy --agent security-scanner            # run a proactive agent
```

Minder takes GitHub issues and runs them through a pipeline:

```
Issues → Dependency graph → Parallel agents → Code review → Lesson capture → PR
```

Each agent runs in an isolated git worktree with its own branch. The supervisor manages concurrency, budget limits, and the review pipeline. Everything is tracked in SQLite.

## Quick start

```bash
# Install
go install github.com/aptx-health/agent-minder/cmd/minder@latest

# Set up
export GITHUB_TOKEN=ghp_...

# Deploy on issues
minder deploy 42 --repo /path/to/repo --foreground

# Check status
minder status
```

## Features

### Multi-agent orchestration
- LLM-built dependency graphs determine execution order
- Up to N concurrent agents (configurable with `--max-agents`)
- Slot backfill: as agents finish, new ones start automatically
- Budget ceiling with 80% warning and automatic pause

### Agent contracts
Agents declare their behavior in YAML frontmatter in `.claude/agents/*.md`:

```yaml
---
name: dependency-updater
mode: proactive              # no issue needed
output: pr                   # opens a PR
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

Contract fields: `mode` (reactive/proactive), `output` (pr/issue/report/none), `context` (providers), `dedup` (strategies), `stages` (multi-step pipeline), `timeout`.

### Context providers
Agents get context assembled from declared providers:

| Provider | Description |
|----------|-------------|
| `issue` | Issue title, body, and comments from GitHub |
| `repo_info` | Languages, test command, base branch, worktree path |
| `file_list` | Repository file tree (depth 3) |
| `recent_commits:<days>` | Git log from last N days |
| `lessons` | Relevant lessons from the learning system |
| `sibling_jobs` | Other jobs in the same deployment |
| `dep_graph` | Dependency graph for the deployment |

### Automated review
After an agent opens a PR, a review agent assesses it:
- **low-risk**: Clean, well-tested. Auto-merge eligible.
- **needs-testing**: Looks correct but needs human verification.
- **suspect**: Has issues requiring human review.

Review produces structured JSON with risk level, summary, lessons, and specific issues found.

### Learning system
Minder learns from agent outcomes:
- Lessons captured automatically from review findings
- Injected into future agent prompts (~2000 token budget)
- Effectiveness tracking: helpful/unhelpful counts per lesson
- Per-scope: repo-specific or global lessons
- Grooming: stale/ineffective auto-deactivation, LLM-assisted consolidation

```bash
minder lesson list                        # show all lessons
minder lesson add "Always run tests"      # add manually
minder lesson groom --dry-run             # preview consolidation
```

### Job scheduler
Define recurring jobs in `.agent-minder/jobs.yaml`:

```yaml
jobs:
  weekly-deps:
    schedule: "0 9 * * 1"          # cron expression
    agent: dependency-updater
    description: "Check for outdated dependencies"
    budget: 3.0

  bug-triage:
    trigger: "label:bug"           # event trigger
    agent: autopilot
```

```bash
minder jobs list                   # show schedules
minder jobs run weekly-deps        # manual trigger
```

### Dedup engine
Stackable strategies prevent duplicate work:
- `branch_exists` — skip if branch already exists
- `open_pr_with_label:<label>` — skip if matching PR is open
- `recent_run:<hours>` — skip if same agent ran recently

### Watch mode
Continuously poll GitHub for new issues matching a filter:

```bash
minder deploy --watch label:agent-ready --serve :7749
minder deploy --watch milestone:v2.0
```

### Daemon mode + HTTP API
Run as a background daemon with a REST API:

```bash
minder deploy 42 55 --serve :7749       # start daemon
curl localhost:7749/status | jq          # check status
curl localhost:7749/jobs | jq            # list jobs
minder stop <deploy-id>                  # stop daemon
```

Endpoints: `/status`, `/jobs`, `/jobs/{id}`, `/jobs/{id}/log`, `/dep-graph`, `/metrics`, `/lessons`, `/stop`, `/resume`.

### xbar menu bar plugin
macOS menu bar widget shows agent status at a glance. See `xbar/minder.5s.sh`.

## Commands

| Command | Description |
|---------|-------------|
| `minder deploy [issues...] [flags]` | Launch agents on issues or start daemon |
| `minder status [deploy-id]` | Show deployment status (`--json` for structured output) |
| `minder stop [deploy-id]` | Stop a running deployment |
| `minder lesson add\|list\|edit\|remove\|pin\|groom` | Manage the learning system |
| `minder jobs list\|run` | View and trigger scheduled jobs |
| `minder enroll [repo-dir]` | Scan a repo and generate onboarding config |

### Deploy flags

| Flag | Default | Description |
|------|---------|-------------|
| `--repo <dir>` | `.` | Repository directory |
| `--agent <name>` | `autopilot` | Agent type to use |
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
| **`GITHUB_TOKEN`** | GitHub API access for issues, PRs, labels |
| **[gh CLI](https://cli.github.com/)** | Agents use `gh` for PR creation and issue management |

## Architecture

```
cmd/minder/main.go          # Entry point
cmd/                         # Cobra commands (deploy, status, stop, lesson, jobs, enroll)
internal/
  supervisor/                # Job manager, context providers, contracts, dedup, review
  scheduler/                 # Cron parser, jobs.yaml, scheduled job firing
  daemon/                    # PID files, heartbeat, HTTP API server + client
  db/                        # SQLite schema (v3), models, queries
  claudecli/                 # Claude Code CLI wrapper
  github/                    # GitHub API client (go-github)
  git/                       # Git CLI wrappers
  lesson/                    # Lesson selection, injection, grooming
  onboarding/                # Repo scanning, onboarding YAML
  discovery/                 # Language/framework detection
  agentutil/                 # Agent log parsing
  sqliteutil/                # WAL recovery
xbar/                        # macOS menu bar plugin
.agent-minder/               # Per-repo config (jobs.yaml, onboarding.yaml)
```

### Data storage

SQLite at `~/.agent-minder/v2.db` (WAL mode, foreign keys). Schema v3.

| Table | Purpose |
|-------|---------|
| `deployments` | Deploy runs with config (agents, budget, model, base branch) |
| `jobs` | Work queue (agent, status, worktree, PR, cost, stages, results) |
| `job_schedules` | Cron schedules with last/next run tracking |
| `dep_graphs` | LLM-generated dependency graphs |
| `lessons` | Persistent feedback with effectiveness tracking |
| `job_lessons` | Which lessons were injected into which jobs |
| `repo_onboarding` | Cached repo scanning results |

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `GITHUB_TOKEN` | — | GitHub API token (required) |
| `MINDER_DB` | `~/.agent-minder/v2.db` | Database path |
| `MINDER_LOG` | `~/.agent-minder/debug.log` | Debug log path |
| `MINDER_DEBUG` | — | Enable structured JSON debug logging |

## Testing

```bash
go test ./...                              # all tests
go test ./internal/db/... -v               # DB + migrations
go test ./internal/supervisor/... -v       # supervisor, contracts, context, dedup
go test ./internal/scheduler/... -v        # cron parser, config, scheduler
go test ./internal/daemon/... -v           # HTTP API endpoints
```

## Debug logging

```bash
MINDER_DEBUG=1 minder deploy 42 --foreground

# Watch in another terminal
tail -f ~/.agent-minder/debug.log | jq '{time, msg, agent, issue}'
```

## Agent logs

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
