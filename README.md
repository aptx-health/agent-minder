# agent-minder

A Go CLI tool that coordinates AI agents working across multiple repositories. It monitors git activity, watches the [agent-msg](https://github.com/aptx-health/agent-msg) message bus, tracks GitHub issues/PRs, and uses a two-tier LLM pipeline to analyze changes and keep agents informed.

Built as a coordination layer on top of agent-msg's simple bash scripts + SQLite foundation.

## How it works

```
agent-minder start <project>
```

Launches a TUI dashboard that:

1. **Polls** git repos and the agent-msg bus on a configurable interval
2. **Tier 1 (Haiku)** — Cheap/fast summarizer. Digests raw git commits + bus messages into a terse summary
3. **Tier 2 (Sonnet)** — Rich analyzer. Produces structured JSON with actionable insights, flags concerns, and optionally publishes messages back to the bus when coordination is needed
4. **Sweeps** tracked GitHub issues/PRs for status changes, runs per-item Haiku summaries on changed items
5. **Displays** everything in a real-time terminal dashboard with event log, active concerns, tracked items, and poll results

```
┌─────────────────────────────────────────────────────────┐
│ agent-minder: myproject  RUNNING  [dark]                │
│   feature — Building the new auth system                │
│                                                         │
│ Repos                                                   │
│   app (/Users/me/repos/myproject-app)                   │
│   infra (/Users/me/repos/myproject-infra)               │
│                                                         │
│ Topics                                                  │
│   myproject/app                                         │
│   myproject/infra                                       │
│   myproject/coord                                       │
│                                                         │
│ Tracked Items (3)                                       │
│   #42 [Open]  Add auth middleware          repo/app     │
│   #18 [Mrgd]  Fix DB connection pool       repo/infra  │
│   #7  [Revw]  Update API docs              repo/app    │
│                                                         │
│ Active Concerns (2)                                     │
│   [WARN] Schema changed in app but infra queries ...    │
│   [INFO] feature/auth branch stale for 3 days           │
│                                                         │
│ Last Poll  [e: expand]                                  │
│   3 commits, 1 message (took 4.2s)                      │
│   App and infra both active. Schema migration in app... │
│                                                         │
│ Event Log                                               │
│   [14:32:05] poll: 3 new commits, 1 new message         │
│   [14:27:01] poll: No new activity                      │
│                                                         │
│ ?: help • p: pause • r: poll • e: expand • i: items •   │
│ f: filter • m: broadcast • u: user msg • o: onboard •   │
│ t: theme • q: quit                                      │
└─────────────────────────────────────────────────────────┘
```

## Supported platforms

| OS | Architecture | Status | Notes |
|----|-------------|--------|-------|
| macOS | amd64 (Intel) | Supported | Primary development platform |
| macOS | arm64 (Apple Silicon) | Supported | Primary development platform |
| Linux | amd64 | Supported | Tested on Ubuntu, Fedora |
| Linux | arm64 | Supported | |
| Windows | amd64 | Supported | Native or WSL2 |

## Prerequisites

### Required

| Dependency | Min version | Purpose | Install |
|-----------|-------------|---------|---------|
| **Go** | 1.25+ | Build from source (bubbletea v2 requires 1.25+) | [go.dev/dl](https://go.dev/dl/) |
| **git** | 2.x | Repository monitoring, worktree management | Package manager or [git-scm.com](https://git-scm.com/) |
| **`ANTHROPIC_API_KEY`** | — | LLM pipeline (tier 1 + tier 2 analysis) | [console.anthropic.com](https://console.anthropic.com/) |

### Required for specific features

| Dependency | Feature | What happens if missing |
|-----------|---------|------------------------|
| **`GITHUB_TOKEN`** or **`GH_TOKEN`** | Tracked items (issue/PR sweep), autopilot | `track`/`untrack` commands and the sweep pipeline are unavailable. Autopilot refuses to start with an error message. Token can also be stored in the OS keychain via `agent-minder setup`. |
| **[gh](https://cli.github.com/)** (GitHub CLI) | Autopilot agent operations | Autopilot agents use `gh` to interact with GitHub (create PRs, post comments, manage labels). Without it, agents will fail. Not needed for non-autopilot usage. |
| **[claude](https://docs.anthropic.com/en/docs/claude-code)** (Claude Code CLI) | Autopilot, LLM-assisted init | Autopilot dispatches work via `claude -p`. The `init` command can optionally use Claude Code for repo onboarding. Without it, autopilot cannot run and init skips the onboarding step. |

### Optional

| Dependency | Feature | What happens if missing |
|-----------|---------|------------------------|
| **[agent-msg](https://github.com/aptx-health/agent-msg)** | Inter-agent message bus | Bus features (broadcast, onboard, user messages) are unavailable. A warning is logged at startup. Polling, git monitoring, and GitHub sweeps continue normally. |
| **jq** | Debug log viewing | `tail -f` log viewing works without it, but structured output (`jq` piping) is unavailable. |
| **lnav** | Color-coded debug log viewer | Optional — `tail -f` works as a simpler alternative. |

### OS keychain (go-keyring)

agent-minder uses [go-keyring](https://github.com/zalando/go-keyring) to store API keys and tokens securely in the OS-native keychain. The keychain is **optional** — if unavailable, credentials fall back to the config file or environment variables.

| Platform | Backend | System requirement |
|----------|---------|-------------------|
| **macOS** | Keychain (Security framework) | None — built-in |
| **Linux** | Secret Service API (via D-Bus) | Requires a running D-Bus session and a secret service provider (e.g., `gnome-keyring`, `kwallet`). The daemon must be running, not just installed — start it with `gnome-keyring-daemon --start --components=secrets` or ensure your desktop session launches it automatically. Install: `sudo apt install gnome-keyring dbus-x11` (Ubuntu/Debian) or `sudo dnf install gnome-keyring` (Fedora). Headless servers and minimal containers typically lack these — the keychain will be unavailable but agent-minder works fine without it. |
| **Windows** | Windows Credential Manager (WinCred) | None — built-in |
| **WSL2** | Secret Service API (via D-Bus) | Same as Linux. Some WSL2 distros ship without D-Bus — install as above. |

**Graceful degradation:** On startup, `agent-minder setup` probes the keychain with a test write/delete. If the probe fails, credentials are stored in the config file (`~/.agent-minder/config.yaml`) instead. A warning is printed when a keychain write fails:

```
Warning: keychain write failed: <error> — falling back to config file
```

### Platform-specific notes

**Ubuntu / Debian:**
```bash
# Keychain support (optional)
sudo apt install gnome-keyring dbus-x11

# If running headless (SSH, containers), start a D-Bus session:
eval $(dbus-launch --sh-syntax)
```

**Fedora:**
```bash
# Keychain support (optional)
sudo dnf install gnome-keyring
```

**macOS:**
No additional system dependencies. Keychain, git, and all required frameworks are built-in.

**Windows (native):**
Windows Credential Manager is built-in. Install git from [git-scm.com](https://git-scm.com/). The TUI requires a terminal emulator with ANSI support (Windows Terminal, ConEmu, or similar).

**WSL2:**
Behaves like Linux. Install dependencies via your distro's package manager. Note that `pbcopy` (used for clipboard operations in the TUI) is macOS-only — clipboard features are unavailable on Linux/WSL2.

## Installation

```bash
go install github.com/dustinlange/agent-minder@latest
```

## Commands

### `agent-minder init <repo-dir> [repo-dir ...]`

Interactive wizard that bootstraps a new project:
- Scans repos for git info and worktrees
- Derives a project name from directory names
- Selects a goal type (feature, bugfix, infrastructure, maintenance, standby)
- Suggests message bus topics (`<project>/app`, `<project>/coord`, etc.)
- Configures poll interval, LLM models, and writes everything to SQLite

### `agent-minder start <project>` / `resume <project>`

Launches the TUI dashboard + polling loop. Key bindings:

| Key | Action |
|-----|--------|
| `?` | Show help overlay |
| `p` | Pause/resume polling |
| `r` | Trigger immediate poll |
| `e` | Expand/collapse poll result |
| `i` | Toggle tracked items panel |
| `I` | Bulk track — add issues/PRs across repos |
| `w` | Bulk untrack — remove tracked items |
| `d` | Cleanup — remove terminal (merged/closed) items |
| `f` | Filter events by keyword |
| `u` | User message — post a raw message to a topic |
| `m` | Broadcast mode — type a message for the LLM to craft and publish to the bus |
| `o` | Onboard mode — generate an onboarding message for new agents |
| `a` | Launch autopilot — assign agents to tracked GitHub issues |
| `A` | Stop autopilot (with confirmation) |
| `t` | Cycle theme (dark/light) |
| `q` | Quit |

### `agent-minder status <project>`

Quick CLI summary from SQLite — no LLM call. Shows repos, concerns, and last poll result.

### `agent-minder list`

List all configured projects.

### `agent-minder enroll <project> <repo-dir>`

Add a repo to an existing project.

### `agent-minder track <project> <owner/repo> <number> [number ...]`

Track GitHub issues or PRs. The sweep pipeline will monitor their status each poll cycle.

### `agent-minder untrack <project> <owner/repo> <number> [number ...]`

Stop tracking GitHub issues or PRs.

### `agent-minder setup <project>`

Re-run project configuration (goal, poll interval, LLM models, etc.).

### `agent-minder delete <project>`

Delete a project and all its associated data.

## Architecture

### Two-tier LLM pipeline

Each poll cycle with new activity runs two LLM calls:

- **Tier 1 (Haiku)** — Summarizes raw data (git commits, bus messages) into a concise factual summary. MaxTokens: 512.
- **Tier 2 (Sonnet)** — Analyzes the summary + active concerns + tracked items + project context. Returns structured JSON with an analysis, optional concerns, and an optional bus message. MaxTokens: 1024.

The tier 2 response is parsed for:
- **Analysis** — Displayed in the TUI
- **Concerns** — Managed via `reconcileConcerns()`: each cycle returns the full desired list, diffs against existing (adds new, resolves dropped, updates severity). Severities: `info`, `warning`, `danger`.
- **Bus message** — Published to agent-msg if the analyzer determines coordination is needed

### GitHub item sweep

Tracked items (issues/PRs) are swept each poll cycle:
- Fetches current status from GitHub API (`go-github` client)
- Detects state changes (open → closed, draft → ready, review state changes)
- Runs a per-item **Haiku** call to generate a progress summary when status changes
- Archives completed items (merged/closed with a summary) to `completed_items` table
- Status tags in TUI: Open, Closd, Mrgd, Draft, Revw, Blkd

### Bus integration

The minder reads from and writes to the agent-msg SQLite database directly:
- **`Client`** — Read-only connection (`?mode=ro`) for polling messages
- **`Publisher`** — Read-write connection for publishing; supports `PublishReplace()` for single-message topics (e.g., onboarding)
- Both use `sqliteutil.OpenWithRecovery()` to auto-detect and recover from stale WAL/SHM files
- agent-msg's bash scripts (`agent-pub`, `agent-check`, `agent-ack`) remain fully compatible

### Broadcast mode

Press `m` in the TUI to enter broadcast mode. Type a prompt describing what you want to communicate, and the tier 2 model crafts a context-aware message using current project state, then publishes it to the coordination topic.

### Onboard mode

Press `o` to generate an onboarding message for new agents joining the project. Optionally provide guidance text. The tier 2 model generates a comprehensive onboarding message and publishes it to `<project>/onboarding` with replace semantics (only the latest onboarding message is kept).

### Autopilot

Press `a` to launch autopilot — minder converts tracked GitHub issues into a task queue, builds a dependency graph (one LLM call), and dispatches up to N concurrent Claude Code agents to work on unblocked issues in isolated git worktrees. Agents self-triage: they either complete the work and open a draft PR, or bail with a detailed comment explaining what they found.

**Minder is a work dispatcher, not a quality gate.** It generates the dependency graph and assigns work — the target repo's own tooling (pre-commit hooks, linters, test suites, CI/CD) is responsible for enforcing code quality. Repos should have a `CLAUDE.md` with project conventions and build/test commands so agents can work effectively.

#### Skipping issues with GitHub labels

Issues can be excluded from autopilot by adding skip labels in GitHub. By default, issues labeled `no-agent` are skipped. You can configure one or more skip labels (comma-separated) via:

- **Init wizard**: prompted during `agent-minder init`
- **TUI settings**: press `s` → edit "Autopilot skip label(s)"

For example, setting skip labels to `no-agent, manual, human-only` will exclude any tracked issue that carries any of those labels. The dependency graph also marks skipped issues so downstream tasks aren't blocked waiting on work that won't be automated.

#### Agent definition (optional)

Autopilot supports an optional [Claude Code agent definition](https://docs.anthropic.com/en/docs/claude-code/sub-agents) that gives agents consistent behavioral guidance. **The agent definition is additive, never required** — it's a thin layer to make agent behaviors more predictable and customizable. Without it, autopilot works exactly as before using its built-in prompt.

To use it, install `agents/autopilot.md` in a target repo or globally:

```bash
# Per-repo (committed with the project)
cp agents/autopilot.md <your-repo>/.claude/agents/autopilot.md

# Or globally (applies to all repos)
cp agents/autopilot.md ~/.claude/agents/autopilot.md
```

When the supervisor detects the definition (project-level or user-level), it switches from a single monolithic prompt to `claude --agent autopilot -p "<task context>"` — the agent definition provides the behavioral instructions (workflow, constraints, bail conditions) and the prompt carries only the dynamic task context (issue number, paths, commands).

You can customize the definition per-repo to adjust complexity thresholds, add project-specific conventions, or modify the workflow. See [agents/README.md](agents/README.md) for details.

See [docs/automated-agents-design.md](docs/automated-agents-design.md) for the full design.

### Data storage

All state lives in SQLite at `~/.agent-minder/minder.db` (WAL mode, foreign keys). Schema version: **v9**.

- **projects** — Name, goal, LLM config, poll settings, idle pause
- **repos** — Git repositories tracked per project (with optional summary)
- **worktrees** — Git worktrees per repo
- **topics** — Message bus topics to monitor
- **concerns** — Active/resolved issues the minder tracks
- **polls** — Full history of poll results with tier 1/tier 2 responses
- **tracked_items** — GitHub issues/PRs being monitored (with content hash, progress summary, draft/review state)
- **completed_items** — Archived from tracked_items when they reach terminal state
- **autopilot_tasks** — Issue work queue for autopilot (status, dependencies, worktree path, PR number)

## Project structure

```
agent-minder/
├── agents/
│   └── autopilot.md    # Claude Code agent definition template for autopilot
├── cmd/
│   ├── root.go          # Cobra root command
│   ├── init.go          # Interactive setup wizard
│   ├── start.go         # TUI + poller launch
│   ├── status.go        # CLI status summary
│   ├── list.go          # List projects
│   ├── enroll.go        # Add repo to project
│   ├── track.go         # Track GitHub issues/PRs
│   ├── untrack.go       # Untrack GitHub issues/PRs
│   ├── setup.go         # Reconfigure project
│   ├── delete.go        # Delete project
│   ├── pause.go         # Pause hint
│   └── resume.go        # Alias for start
├── internal/
│   ├── autopilot/       # Autopilot supervisor, agent prompt, slot management
│   ├── db/              # SQLite schema, migrations (v1→v9), CRUD
│   ├── llm/             # Provider interface (Anthropic + OpenAI-compatible)
│   ├── poller/          # Two-tier poll loop, analysis parsing, item sweep
│   ├── tui/             # Bubbletea v2 dashboard (app, styles, layout, filter)
│   ├── git/             # Git CLI wrappers (log, branches, worktrees)
│   ├── github/          # GitHub API client (go-github), issue/PR status fetching
│   ├── discovery/       # Repo scanning, project name derivation
│   ├── sqliteutil/      # SQLite health + WAL recovery
│   ├── msgbus/          # Agent-msg SQLite client (read) + publisher (write)
│   └── secrets/         # Secret detection and filtering
├── lnav/
│   └── agent-minder.json  # lnav format file for debug log viewing
├── go.mod
├── go.sum
└── main.go
```

## Dependencies

- [bubbletea v2](https://charm.land/bubbletea) + [lipgloss v2](https://charm.land/lipgloss) + [bubbles v2](https://charm.land/bubbles) — TUI framework
- [anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go) + [openai-go](https://github.com/openai/openai-go) — LLM providers
- [go-github](https://github.com/google/go-github) — GitHub API client
- [sqlx](https://github.com/jmoiron/sqlx) + [modernc.org/sqlite](https://modernc.org/sqlite) — Database (pure Go, no CGo)
- [cobra](https://github.com/spf13/cobra) — CLI framework

## Testing

```bash
go test ./...                           # All unit tests
go test ./internal/db/... -v            # DB + migration tests
go test ./internal/poller/... -v        # Analysis parsing, concern dedup, sweep, tracked items
go test ./internal/msgbus/... -v        # Client + publisher tests
go test ./internal/github/... -v        # GitHub URL parsing + status tests

# Integration test (requires ANTHROPIC_API_KEY + existing agent-test project):
go test -tags integration -run TestIntegrationTwoTierPipeline -v ./internal/poller/ -timeout 90s
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

## agent-msg integration

agent-msg is **optional**. Without it, agent-minder still monitors git repos, tracks GitHub items, runs the LLM pipeline, and displays everything in the TUI — you just won't have inter-agent messaging.

When agent-msg is available, agent-minder sits on top of its SQLite database:

- **Reads**: Recent messages, unread messages, topic summaries, active agents
- **Writes**: Publishes messages as `<project>/minder` identity when coordination is needed
- **Compatible**: agent-msg's bash scripts (`agent-pub`, `agent-check`, `agent-ack`, `agent-topics`) continue working against the same database
- **Graceful degradation**: If the agent-msg DB is missing at startup, a warning is logged and bus features are disabled. Polling, git monitoring, and GitHub sweeps continue normally.
