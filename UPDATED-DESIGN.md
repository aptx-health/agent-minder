# agent-minder — Design Document

## Overview

A Go CLI tool that acts as a coordination layer on top of agent-msg. It monitors multiple repositories, watches the message bus, tracks git activity, and keeps both AI agents and the human operator informed about cross-repo state.

**agent-msg** (Layer 0) stays lean: five bash scripts + SQLite. **agent-minder** (Layer 1) adds structure, awareness, and orchestration on top.

## Architecture

```
agent-minder (Go CLI — Cobra)
  ├── init         → wizard: discover repos, suggest topics, write config
  ├── start        → launch monitoring loop via claude CLI
  ├── status       → catch-up summary for the human (no AI call needed)
  ├── enroll       → add repo/worktree to active project
  ├── pause        → stop polling loop
  └── resume       → restart from saved state
```

Go handles plumbing (config, git, SQLite, scheduling). Claude Code handles thinking (interpretation, decisions, publishing).

### How it talks to Claude Code

The `claude` CLI supports non-interactive usage:

```bash
claude -p "your prompt here"       # one-shot, prints result
claude --resume <session-id>       # resume a conversation
claude -c                          # continue last conversation
```

agent-minder assembles structured prompts from templates + live data, then invokes `claude`. The Go binary never does AI reasoning itself.

## CLI Commands

### `agent-minder init <repo-dir> [repo-dir ...]`

Interactive wizard that bootstraps a project:

1. **Scan each repo directory:**
   - Read README.md, CLAUDE.md (understand what the project is)
   - `git log --oneline -20` (recent activity)
   - `git branch -a` (who's working on what)
   - `git worktree list` (discover active worktrees)
   - `git diff main...<branch>` for active branches (what's in flight)

2. **Derive project name:**
   - From git remote URL, or common directory parent, or ask user
   - Example: `~/repos/ripit-app` + `~/repos/ripit-infra` → project "ripit"

3. **Suggest topics:**
   - One topic per repo: `<project>/<short-name>` derived from directory name
     - `ripit-app` → `ripit/app`
     - `ripit-infra` → `ripit/infra`
   - Always add a coordination topic: `<project>/coord`
   - User can accept, edit, add, or remove

4. **Configure settings (with defaults):**
   - Refresh interval (default: 5m)
   - Message TTL / history window (default: 48h)
   - Auto-enroll new worktrees (default: yes)

5. **Write config + initial state:**
   - `~/.agent-minder/<project>/config.yaml`
   - `~/.agent-minder/<project>/state.md`

6. **Register topics in agent-msg DB** (soft enforcement — see below)

### `agent-minder start <project>`

1. Gather current state: git logs, message bus, worktree status
2. Assemble initial prompt from Go templates + live data
3. Launch `claude -p "<prompt>"` to create a session
4. Enter poll loop (likely using Claude Code's `/loop` skill):
   - `agent-check` — new messages?
   - Quick `git log` on each repo — new commits since last check?
   - `git worktree list` — new or removed worktrees?
   - Compare against state file — anything relevant?
   - If yes: publish targeted message, update state file
   - If no: sleep until next interval

### `agent-minder status <project>`

**This is the most important command for the human.** No AI call — reads state file + does quick git checks, renders a template.

Example output:

```
Minder: ripit (running, last poll 2m ago)

Repos:
  ripit-app (main + 1 worktree)
    main: 3 new commits since yesterday (schema migration, auth middleware, tests)
    feature/auth: stale, no activity in 3 days
  ripit-infra (main)
    main: 1 new commit (ArgoCD app manifest for ripit)

Messages (4 unread):
  [15] ripit/migration — Tobias: "P0 complete, k3s ready, need Docker image"
  [16] ripit/backend — Cornelius: "Added email_verified migration"
  ...

Active concerns:
  ⚠ Schema changed in ripit-app but ripit-infra worker queries not updated
  ✓ k3s cluster bootstrapped and waiting

Stale worktrees:
  ripit-app/feature/auth — no activity in 3 days, consider pruning
```

### `agent-minder enroll <project> <repo-dir>`

Add a new repo or worktree to an active project:
- Scan the directory (same as init does per-repo)
- Auto-detect if it's a worktree of an already-enrolled repo
- Suggest a topic name
- Update config.yaml
- Notify the running minder session (publish a message to the coord topic)

### `agent-minder pause <project>`

Stop the polling loop. State file persists. Useful when stepping away — no point burning tokens on a loop with nobody watching.

### `agent-minder resume <project>`

Re-launch Claude Code with: "Here's your state file, here's what's new since you paused, continue monitoring." Uses `claude --resume` if the session is still alive, otherwise starts fresh with state context.

## Data Model

### Config file: `~/.agent-minder/<project>/config.yaml`

```yaml
project: ripit
refresh_interval: 5m
message_ttl: 48h
auto_enroll_worktrees: true

repos:
  - path: ~/repos/ripit-app
    short_name: app
    worktrees:
      - path: ~/repos/ripit-app
        branch: main
      - path: ~/repos/ripit-app-feature-auth
        branch: feature/auth
  - path: ~/repos/ripit-infra
    short_name: infra
    worktrees:
      - path: ~/repos/ripit-infra
        branch: main

topics:
  - ripit/app
  - ripit/infra
  - ripit/coord

minder_identity: ripit/minder
claude_session_id: <populated at start time>
```

### State file: `~/.agent-minder/<project>/state.md`

This is the minder's "memory" — survives context compaction and pause/resume cycles. Written by Claude during monitoring, read by `status` command.

```markdown
# Minder State: ripit

## Watched Repos
- ~/repos/ripit-app — Next.js app, Prisma schema, active branch: feature/auth
- ~/repos/ripit-infra — k3s infra, ArgoCD, currently on main

## Active Concerns
- ripit-app is modifying User schema — ripit-infra worker queries may need updating
- k3s cluster is bootstrapped, waiting for Docker image from app

## Recent Activity
- [2026-03-01 22:42] ripit-infra: PostgreSQL + Redis deployed to cluster
- [2026-03-02 00:38] ripit-app: Fixed local dev postgres persistence

## Monitoring Plan
- Watch for schema changes in ripit-app (Prisma migrations dir)
- Watch for new messages on ripit/*
- Alert if both repos touch shared contract (API types, DB schema)

## Last Poll
- Time: 2026-03-02 01:15 UTC
- Messages checked: 0 new
- Git activity: none since last poll
```

## Topic Enforcement

**Soft enforcement.** agent-msg (Layer 0) stays permissive — `agent-pub` always works. The minder adds guardrails:

- Registered topics are stored in config.yaml
- If an agent publishes to an unregistered topic, the minder notices and can:
  - Re-publish the content to the correct topic
  - Ask the human if the new topic should be registered
  - Just log it as a note in state.md
- This keeps Layer 0 zero-config while Layer 1 adds structure

## Git Commits as Signals

The minder reads git commit logs as an implicit communication channel:
- Commit messages provide context for what's happening without agents needing to manually publish
- The message bus (`agent-pub`) is for things NOT in commits: questions, status updates, coordination requests, cross-repo alerts
- The minder synthesizes both sources: "ripit-app had 3 commits touching the schema + there's a message on ripit/coord asking about migration timing"

## Minder Behaviors

### What it publishes

The minder is an active participant, not just a relay:

- **Conflict detection**: "ripit-app and ripit-infra both modified the User type in the last hour — someone should coordinate"
- **Dependency nudges**: "ripit-app merged schema changes 2 hours ago but ripit-infra hasn't picked them up yet"
- **Status summaries**: Periodic "here's where everything stands" on the coord topic
- **Staleness alerts**: "feature/auth worktree has had no activity in 3 days — consider pruning"
- **Worktree lifecycle**: "New worktree detected: ripit-app/fix-login on branch fix/login"

### What it does NOT do

- Direct agents or assign work (it's an observer/coordinator, not a manager)
- Make code changes
- Merge branches or deploy

## Project Structure

```
agent-minder/
├── cmd/
│   ├── root.go           # Cobra root command, global flags
│   ├── init.go           # wizard: discover repos, suggest topics, write config
│   ├── start.go          # launch monitoring loop via claude CLI
│   ├── status.go         # catch-up summary for the human
│   ├── enroll.go         # add repo/worktree to active project
│   ├── pause.go          # stop polling
│   └── resume.go         # restart from state
├── internal/
│   ├── config/           # yaml config read/write, defaults
│   ├── discovery/        # repo scanning, worktree detection, README parsing
│   ├── git/              # git log, diff, branch, worktree list wrappers
│   ├── msgbus/           # SQLite client for agent-msg DB (read topics, messages)
│   ├── state/            # state file read/write/merge
│   ├── claude/           # claude CLI invocation wrapper (start, resume, one-shot)
│   └── prompt/           # Go templates for assembling prompts
├── templates/
│   ├── init_prompt.md.tmpl     # initial context prompt for claude
│   ├── poll_prompt.md.tmpl     # per-cycle poll prompt
│   ├── resume_prompt.md.tmpl   # resume-after-pause prompt
│   └── status.md.tmpl          # human-facing status output
├── config/
│   └── defaults.yaml           # default settings
├── go.mod
├── go.sum
├── main.go
└── README.md
```

### Key dependencies (Go)

- `github.com/spf13/cobra` — CLI framework
- `github.com/spf13/viper` — config management
- `gopkg.in/yaml.v3` — config serialization
- `modernc.org/sqlite` or `github.com/mattn/go-sqlite3` — SQLite access for reading agent-msg DB
- Standard library for git operations (exec.Command wrapping git)

## Build Order

1. Scaffold Go repo with `cobra-cli init`
2. `internal/config` — config struct + yaml read/write
3. `internal/git` — git wrappers (log, branch, worktree list, diff)
4. `internal/discovery` — repo scanning
5. `cmd/init` — wizard flow
6. `internal/msgbus` — SQLite read client for agent-msg
7. `templates/status.md.tmpl` + `cmd/status` — human catch-up (most useful early)
8. `internal/claude` — claude CLI wrapper
9. `templates/init_prompt.md.tmpl` + `cmd/start` — launch monitoring
10. `cmd/enroll`, `cmd/pause`, `cmd/resume`

## Claude API Integration During Init

### Why

The `init` wizard collects a lot of raw data per repo (README, CLAUDE.md, 20 commit messages, branch names, diffs). Dumping all of that into the minder's initial prompt wastes context. Instead, use cheap Anthropic API calls (Haiku) during init to preprocess and summarize, so the minder's Claude Code session starts with tight, curated context.

### Where it fits

Go calls the Anthropic API directly via `github.com/anthropics/anthropic-sdk-go`. This is *not* a Claude Code invocation — it's a fast, structured preprocessing step that runs during the `init` wizard before any `claude -p` session is created.

### What it does

**Per-repo summarization** — For each scanned repo, send to Haiku:
- README.md (or first ~2k chars)
- CLAUDE.md (if present)
- `git log --oneline -20`
- Active branch names

Prompt: "Summarize this repo in 2-3 sentences: what it is, what tech it uses, and what's actively being worked on."

**Cross-repo goal inference** — After all repos are summarized, send the summaries together to Haiku and ask it to propose a project goal. This feeds into the goal selection step (see below).

### New dependency

```
github.com/anthropics/anthropic-sdk-go
```

Reads `ANTHROPIC_API_KEY` from environment. If not set, skip AI summarization gracefully — fall back to raw data in prompts (current behavior).

### Where summaries land

- Per-repo summaries go into `state.md` under `## Watched Repos` (replacing raw data)
- Goal goes into both `config.yaml` and `state.md`
- The minder's `start` prompt template reads from state.md, so it gets the curated context automatically

## Project Goals

### Goal selection during init

After repo scanning and AI summarization, the wizard presents goal options:

```
What's the goal for this project?

  1. Feature work — building or shipping something new
  2. Bug fix — tracking down and fixing a specific issue
  3. Infrastructure — multi-repo infra, migration, or deployment work
  4. Maintenance — docs, deps, cleanup, refactoring
  5. On-call / standby — monitoring, ready to respond if something comes up
  6. Other — describe it

> _
```

After selecting a category, the user provides a description (or accepts the AI-inferred one if API is available):

```
> 3
Describe the work (or press Enter to accept AI suggestion):
  [AI suggestion: "Migrating ripit-app from Docker Compose to k3s, with ripit-infra handling cluster setup"]
> _
```

### Config representation

```yaml
goal:
  type: infrastructure    # feature | bugfix | infrastructure | maintenance | standby | other
  description: "Migrating ripit-app from Docker Compose to k3s cluster"
```

### How goal type drives behavior

| Goal type      | Refresh interval | Alert urgency | Minder personality |
|----------------|-----------------|---------------|-------------------|
| feature        | 5m (default)    | Medium        | Track progress, flag cross-repo deps |
| bugfix         | 3m              | High          | Focus on the fix, flag regressions |
| infrastructure | 5m              | Medium-High   | Watch for drift between repos |
| maintenance    | 10m             | Low           | Light touch, just track what's done |
| standby        | 15m             | Immediate on activity | Quiet unless something happens |

These map to prompt template variants or conditional sections in the poll prompt:
- Standby: "Only alert if there's new git activity or messages. Don't publish summaries."
- Bugfix: "Prioritize any commits or messages related to the bug. Flag if the fix touches multiple repos."
- Feature: "Track progress toward the goal. Nudge if a repo falls behind."

### State file integration

```markdown
# Minder State: ripit

## Goal
**Type:** Infrastructure
**Description:** Migrating ripit-app from Docker Compose to k3s cluster

## Watched Repos
- ~/repos/ripit-app — Next.js app with Prisma, actively modifying schema and auth
- ~/repos/ripit-infra — k3s cluster setup, ArgoCD manifests, PostgreSQL + Redis deployed
...
```

The goal section is at the top of state.md so it's the first thing the minder sees on every poll cycle and after every resume.

## Init Flow (Revised)

Incorporating both API summarization and goal selection, the full init flow becomes:

1. **Scan repos** — git log, branches, worktrees, README, CLAUDE.md (Go, no AI)
2. **Derive project name** — from git remotes or directory names (Go)
3. **Suggest topics** — `<project>/<short-name>` per repo + coord topic (Go)
4. **AI summarization** (if ANTHROPIC_API_KEY available):
   - Per-repo summaries via Haiku
   - Cross-repo goal inference via Haiku
5. **Goal selection** — present categories, accept/edit AI suggestion or free-form input
6. **Configure settings** — refresh interval (defaulted from goal type), TTL, auto-enroll
7. **Write config.yaml + state.md** — with summaries and goal baked in
8. **Register topics in agent-msg DB**

Steps 1-3 are instant. Step 4 adds ~2-3 seconds total (parallel Haiku calls). Steps 5-8 are interactive.

## Open Questions

- Should the minder have its own identity in agent-msg? (Probably yes — `<project>/minder`)
- Should `agent-minder status` also show a quick diff summary, or just commit counts?
- Should goal type be changeable after init? (`agent-minder goal <project> bugfix "new description"`)
- Future: GitHub integration — watch PRs, CI status, issues in addition to local git
- Future: Multiple minders for the same project (e.g., one per environment: staging vs prod)
- Future: Web dashboard / TUI for real-time monitoring
