---
title: "Open questions and research tasks"
status: open
date: 2026-08-29
tags: [research, open-questions, planning]
---

# Open questions and research tasks

Decisions not yet made and questions worth farming to side agents (opencode / Gemini via
OpenRouter). Each research task is written so it can be handed off standalone. Record the
answer inline and, when a decision lands, promote it to an ADR.

## Research tasks (farmable to a side agent)

### R1 — ACP Go client maturity  *(blocks [[0003-acp-runtime-seam]] confidence)*
Does a usable Go client for the Agent Client Protocol exist today (library, maturity,
license, last release)? Does opencode expose an ACP endpoint, or only its own SDK/serve
API? Does Claude Code speak ACP, and how? Deliverable: a short report with links, a
"ready / not ready" verdict, and — if not ready — the thinnest path to drive opencode
(its serve API + SDK) behind a Trigger-owned runtime interface.

### R2 — External-trigger transport
For "spun up from an external trigger," compare: a localhost HTTP webhook endpoint on the
daemon vs. a small message-bus consumer (e.g. the existing agent-msg bus) vs. a watched
file/directory. Criteria: simplicity, auth, restart-safety, fit with the daemon/API split
([[0004-daemon-interface-split]]). Deliverable: a recommendation with trade-offs.

### R3 — Declarative config schema
Survey how comparable tools express declarative scheduled + triggered jobs (agent-minder
`jobs.yaml`, Kestra, Windmill, GitHub Actions workflow). Deliverable: a proposed YAML
schema for a Trigger job that covers cron, one-off, and webhook triggers uniformly, with
runtime/model selection per job. Must also express a **step list** (per
[[0008-workflows-deterministic-steps]]): each step's kind/runtime/model/agent, its
secrets/permissions block ([[0006-secrets-and-agent-permissions]]), and the two routing
conditions (`on_success`, `on_failure`). A single-step job and a multi-step workflow use the
same shape.

### R4 — TUI framework choice
Confirm bubbletea v2 / lipgloss v2 is still the right TUI base (agent-minder uses it), or
whether a lighter option fits a thin API-reading view better. Deliverable: a short
recommendation. Low priority — the daemon/API split makes this swappable.

### R6 — MCP server surface  *(feeds [[0007-agent-controllable-mcp-server]])*
Design the MCP surface for orchestrating agents. Which tools/verbs (create job, fire
one-off, list/status, fetch results, cancel)? How are job events streamed to an agent
(poll vs. subscribe/SSE)? What resources are exposed (job list, run logs)? How is the local
MCP endpoint authenticated so only an authorized agent can drive Trigger (ties to R5)?
Survey current Go MCP server libraries and their maturity/license. Deliverable: a proposed
tool/resource list and a transport/auth recommendation.

### R5 — Secrets and agent permissions (detail)  *(refines accepted [[0006-secrets-and-agent-permissions]])*
The mechanism is decided (macOS Keychain via go-keyring; containment delegated to the
runtime). Remaining detail: **Secrets** — confirm go-keyring is the right library for the
macOS Keychain vs. alternatives (e.g. 99designs/keyring); define exactly how a job
references a secret by name and how secrets are scoped per service/trigger-source.
**Permissions** — map what each runtime's permission mode actually enforces (Claude Code
permission modes, opencode equivalents), so Trigger knows precisely what it can delegate.
Deliverable: the confirmed library choice and the per-step secrets/permissions config
fields R3 should carry.

## Decisions for Dustin (not research — product calls)

### D1 — Repo boundary
Own repo (`~/repos/trigger`) vs. staying a branch in agent-minder during design. Current
state: design lives in an agent-minder branch for convenience and harvest proximity.
Leaning: graduate to its own repo once the skeleton exists. Decide before first code.

### D2 — Autopilot fan-out timing
Is the charter-aware autopilot fan-out (spawn N agents on issues, make behavioral tests
green) in v1, or a fast-follow after daemon + cron + triggers + one-offs work? Leaning:
fast-follow — prove the scheduler/trigger core first.

### D3 — Lessons system
Keep dropped (BYOA-deprioritized) or plan a slot for it later? Leaning: dropped for v1,
revisit on evidence.

### D4 — Charter authoring ownership
Who writes the red charter/behavioral tests that a future autopilot consumes: manual for
now, pr-triage front-of-loop, or a Trigger pre-stage? Leaning: manual now; decide once the
consumption loop is proven. (Ties to D2.)

## Answered

- **Plugin API — no (2026-08-29).** Sticking with a modular monolith. Extension seams
  (Trigger sources, runtimes) stay internal Go interfaces = compile-time plugins only. No
  dynamic loading, no plugin marketplace, no published plugin API. Revisit only on a
  concrete force (third party shipping adapters, or decoupled release cadence), per the
  pr-triage spike §7 extract-on-force rule.
- **TUI keeps the `checkout` affordances (2026-08-29).** The v1 TUI should retain
  agent-minder's genuinely useful interface conveniences: summon/check out a job's worktree
  locally, and jump to the PR. Keep the TUI otherwise minimal — no heavy features beyond
  these. See [[harvest-map]].
