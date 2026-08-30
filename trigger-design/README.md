# Trigger — design

Design workspace for **Trigger**: a minimal, declarative agent scheduler daemon.
This is a **new tool**, not agent-minder v2. It is built from the lessons learned
in agent-minder and pr-triage, and it is deliberately smaller.

**Status:** design / pre-implementation. Nothing here is code yet. The charter and
ADRs are the plan and the first dogfood of the pr-triage front-of-loop methodology
(charter → ADRs → behavioral contract → red-first tests → implement).

## What Trigger is (one line)

A single local daemon that runs declarative cron jobs and triggered one-offs against
swappable agent runtimes, driven by a thin TUI now and a GUI later, with local state
as the single source of truth.

## What Trigger is not

- Not agent-minder. No autopilot review, no auto-merge, no dependency graphs.
- Not GitHub-centric. GitHub is one trigger source among many.
- Not a heavy platform (contrast OpenHands). One daemon, one job.

## Index

- [charter.md](charter.md) — the scope contract for v1. Read this first.
- ADRs — [docs/adr/](docs/adr/):
  - [0001](docs/adr/0001-purpose-and-scope.md) — Purpose and minimal scope
  - [0002](docs/adr/0002-local-sqlite-source-of-truth.md) — Local SQLite is the source of truth
  - [0003](docs/adr/0003-acp-runtime-seam.md) — ACP-first runtime seam
  - [0004](docs/adr/0004-daemon-interface-split.md) — Daemon / interface split
  - [0005](docs/adr/0005-trigger-source-agnostic.md) — Trigger-source-agnostic (GitHub is one adapter)
  - [0006](docs/adr/0006-secrets-and-agent-permissions.md) — Secrets and agent permissions (macOS Keychain)
  - [0007](docs/adr/0007-agent-controllable-mcp-server.md) — Agent-controllable: machine-first API and MCP server
  - [0008](docs/adr/0008-workflows-deterministic-steps.md) — Workflows: deterministic, declarative ordered steps
  - [0009](docs/adr/0009-cross-tool-boundary-shared-conventions.md) — Cross-tool boundary: shared conventions over shared code
  - [0010](docs/adr/0010-go-template-variables.md) — Go-template variable interpolation
  - [0011](docs/adr/0011-internal-pubsub-two-buses.md) — Internal pub/sub: a work bus and an event bus, both store-first
  - [0012](docs/adr/0012-failure-handling-blocked-and-release.md) — Failure handling: bounded retry, then park as blocked for reasoning
  - [0013](docs/adr/0013-ask-and-resume-instead-of-bail.md) — Ask-and-resume: agents pause for clarification or scope instead of bailing
  - [0014](docs/adr/0014-answer-authority.md) — Answer authority: human or charter-bounded orchestrator
  - [0015](docs/adr/0015-principal-by-transport.md) — Principal-by-transport authority binding
- Design specs — [docs/design/](docs/design/):
  - [config-schema.md](docs/design/config-schema.md) — The declarative job/step config (GitHub-Actions-like)
  - [trigger-abstraction.md](docs/design/trigger-abstraction.md) — How a trigger fires a workflow (Fire event, pull vs push, dedup, lifecycle)
  - [run-lifecycle-and-slots.md](docs/design/run-lifecycle-and-slots.md) — Run state machine, atomic claim, slot model, crash reconcile, blocked/release
  - [daemon-api.md](docs/design/daemon-api.md) — One Service, thin faces (HTTP/MCP/webhook); transport implies principal; SSE event stream
  - [event-observability.md](docs/design/event-observability.md) — Event record, (epoch,id) cursor, taxonomy, subscribers (TUI/MCP/sinks)
  - [db-schema.md](docs/design/db-schema.md) — SQLite runtime state: runs, run_steps, events, trigger_state (config is NOT in the DB)
  - [tui.md](docs/design/tui.md) — TUI design brief: attention-first, parked-runs headline, keep checkout + log stream
  - [tui-mockup-brief.md](docs/design/tui-mockup-brief.md) — Pasteable prompt to commission TUI mockups from a design agent
  - [tui-mockups.md](docs/design/tui-mockups.md) — TUI mockups + interaction design (recommended direction, answer flows, keymap); rendered preview in [tui-mockup.html](docs/design/tui-mockup.html)
- Guidance — [docs/guidance/](docs/guidance/):
  - [glossary.md](docs/guidance/glossary.md) — Terminology (supervisor vs orchestrator, fire, buses, parking family)
  - [config-resolve-once.md](docs/guidance/config-resolve-once.md) — Resolve config once per run; store it on the run
  - [harvest-map.md](docs/guidance/harvest-map.md) — What to lift from agent-minder and pr-triage
- Harvest notes — [docs/harvest/](docs/harvest/): per-package deep dives (side-agent produced)
  - [sqliteutil-wal-recovery.md](docs/harvest/sqliteutil-wal-recovery.md) — WAL recovery + the epoch/cursor truncation contract
  - [event-log-store-first.md](docs/harvest/event-log-store-first.md) — Durable event log invariants (commit-is-publish, cursor, epoch)
  - [agent-runs-table.md](docs/harvest/agent-runs-table.md) — Per-step/attempt run record shape
  - [script-execution-config.md](docs/harvest/script-execution-config.md) — Deterministic script step config + hardening
  - [agentutil-log-parsing.md](docs/harvest/agentutil-log-parsing.md) — Agent log / result parsing + failure taxonomy
  - [git-worktree-helpers.md](docs/harvest/git-worktree-helpers.md) — Worktree add/list/include helpers
  - [checkout-and-auth.md](docs/harvest/checkout-and-auth.md) — Worktree checkout + OS-keyring auth
- Research — [docs/research/](docs/research/):
  - [open-questions.md](docs/research/open-questions.md) — Decisions still open; research tasks for side agents
  - [ask-and-resume-prior-art.md](docs/research/ask-and-resume-prior-art.md) — Prior art behind ADR 0013 (MCP elicitation, LangGraph, Temporal, ACP…)

## Doc discipline (from pr-triage)

Every doc carries front matter with `title`, `status`, `date`, and `tags`. ADR status is
exactly one of `deferred | accepted | superseded`:

- `accepted` — the decision is in force.
- `deferred` — the decision is deliberately postponed; the ADR records the question, not
  the mechanism.
- `superseded` — replaced by a newer ADR; keeps a `superseded_by:` pointer and stays in
  the tree as history.

A decision that is not yet ratified is not an ADR yet — it lives in the charter or in
discussion until ratified, then enters as `accepted` (or `deferred`).

**Immutability:** never edit the *decision* of an accepted ADR. To change it, write a new
ADR on top and mark the old one `superseded`. The only permitted edit to a past ADR is the
supersession bookkeeping (flip `status`, fill `superseded_by`).

One fact per reference doc. Link related docs with wiki-style `[[name]]`.
