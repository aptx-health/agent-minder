# agent-minder

A Go CLI tool that coordinates AI agents working across multiple repositories. It monitors git activity, watches the [agent-msg](https://github.com/dustinlange/agent-msg) message bus, and uses a two-tier LLM pipeline to analyze changes and keep agents informed.

Built as a coordination layer on top of agent-msg's simple bash scripts + SQLite foundation.

## How it works

```
agent-minder start <project>
```

Launches a TUI dashboard that:

1. **Polls** git repos and the agent-msg bus on a configurable interval
2. **Tier 1 (Haiku)** — Cheap/fast summarizer. Digests raw git commits + bus messages into a terse summary
3. **Tier 2 (Sonnet)** — Rich analyzer. Produces structured JSON with actionable insights, flags concerns, and optionally publishes messages back to the bus when coordination is needed
4. **Displays** everything in a real-time terminal dashboard with event log, active concerns, and poll results

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
│ p: pause/resume • r: poll now • e: expand/collapse •    │
│ m: broadcast • t: theme • q: quit                       │
└─────────────────────────────────────────────────────────┘
```

## Installation

```bash
go install github.com/dustinlange/agent-minder@latest
```

Requires:
- Go 1.25+
- [agent-msg](https://github.com/dustinlange/agent-msg) installed (provides the shared SQLite message bus)
- `ANTHROPIC_API_KEY` environment variable set

## Commands

### `agent-minder init <repo-dir> [repo-dir ...]`

Interactive wizard that bootstraps a new project:
- Scans repos for git info and worktrees
- Derives a project name from directory names
- Selects a goal type (feature, bugfix, infrastructure, maintenance, standby)
- Suggests message bus topics (`<project>/app`, `<project>/coord`, etc.)
- Configures poll interval and writes everything to SQLite

### `agent-minder start <project>` / `resume <project>`

Launches the TUI dashboard + polling loop. Key bindings:

| Key | Action |
|-----|--------|
| `p` | Pause/resume polling |
| `r` | Trigger immediate poll |
| `e` | Expand/collapse poll result |
| `m` | Broadcast mode — type a message for the LLM to craft and publish to the bus |
| `t` | Cycle theme (dark/light) |
| `q` | Quit |

### `agent-minder status <project>`

Quick CLI summary from SQLite — no LLM call. Shows repos, concerns, and last poll result.

### `agent-minder enroll <project> <repo-dir>`

Add a repo to an existing project.

## Architecture

### Two-tier LLM pipeline

Each poll cycle with new activity runs two LLM calls:

- **Tier 1 (Haiku)** — Summarizes raw data (git commits, bus messages) into a concise factual summary. MaxTokens: 512.
- **Tier 2 (Sonnet)** — Analyzes the summary + active concerns + project context. Returns structured JSON with an analysis, optional concerns, and an optional bus message. MaxTokens: 1024.

The tier 2 response is parsed for:
- **Analysis** — Displayed in the TUI
- **Concerns** — Inserted into SQLite (deduplicated against existing active concerns via keyword overlap)
- **Bus message** — Published to agent-msg if the analyzer determines coordination is needed

### Bus publishing

The minder reads from and writes to the agent-msg SQLite database directly. When tier 2 decides a bus message is warranted (e.g., breaking changes detected, coordination needed), it publishes via `INSERT INTO messages`. Other agents see these messages through the normal agent-msg tools.

### Broadcast mode

Press `m` in the TUI to enter broadcast mode. Type a prompt describing what you want to communicate, and the tier 2 model crafts a context-aware message using current project state, then publishes it to the coordination topic.

### Data storage

All state lives in SQLite at `~/.agent-minder/minder.db` (WAL mode, foreign keys):

- **projects** — Name, goal, LLM config, poll settings
- **repos** — Git repositories tracked per project
- **worktrees** — Git worktrees per repo
- **topics** — Message bus topics to monitor
- **concerns** — Active/resolved issues the minder tracks
- **polls** — Full history of poll results with tier 1/tier 2 responses

## Project structure

```
agent-minder/
├── cmd/
│   ├── root.go          # Cobra root command
│   ├── init.go          # Interactive setup wizard
│   ├── start.go         # TUI + poller launch
│   ├── status.go        # CLI status summary
│   ├── enroll.go        # Add repo to project
│   ├── pause.go         # Pause hint
│   └── resume.go        # Alias for start
├── internal/
│   ├── db/              # SQLite schema, migrations, CRUD
│   ├── llm/             # Provider interface (Anthropic + OpenAI-compatible)
│   ├── poller/          # Two-tier poll loop, analysis parsing, broadcast
│   ├── tui/             # Bubbletea v2 dashboard
│   ├── git/             # Git CLI wrappers
│   ├── discovery/       # Repo scanning, project name derivation
│   └── msgbus/          # Agent-msg SQLite client (read) + publisher (write)
├── go.mod
├── go.sum
└── main.go
```

## Dependencies

- [bubbletea v2](https://charm.land/bubbletea) + [lipgloss v2](https://charm.land/lipgloss) + [bubbles v2](https://charm.land/bubbles) — TUI framework
- [anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go) + [openai-go](https://github.com/openai/openai-go) — LLM providers
- [sqlx](https://github.com/jmoiron/sqlx) + [modernc.org/sqlite](https://modernc.org/sqlite) — Database (pure Go, no CGo)
- [cobra](https://github.com/spf13/cobra) — CLI framework

## agent-msg integration

agent-minder sits on top of agent-msg's SQLite database:

- **Reads**: Recent messages, unread messages, topic summaries, active agents
- **Writes**: Publishes messages as `<project>/minder` identity when coordination is needed
- **Compatible**: agent-msg's bash scripts (`agent-pub`, `agent-check`, `agent-ack`, `agent-topics`) continue working against the same database
