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
daemon vs. a small external message-bus consumer vs. a watched
file/directory. Criteria: simplicity, auth, restart-safety, fit with the daemon/API split
([[0004-daemon-interface-split]]). Deliverable: a recommendation with trade-offs.

### R3 — Declarative config schema  *(drafted → [[config-schema]])*
A first draft exists in [[config-schema]] (GitHub-Actions-like: jobs → trigger → ordered
steps; script or agent; resolve-once). Remaining: validate the shape against agent-minder
`jobs.yaml`, Kestra, and Windmill for anything missed; firm up the webhook trigger and
permissions sub-schemas (depend on R2/R5/R6).

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
fast-follow — prove the scheduler/trigger core first. *(2026-08-30 refinement:* the fan-out
*entry seam is the operator's call, entered conversationally through their orchestrator —*
*signaled by the completed workflow's handoff packet ([[0023-handoff-packet-and-pickup-verb]]),*
*not by an engine completion signal.*)*

### D3 — Lessons system
Keep dropped (BYOA-deprioritized) or plan a slot for it later? Leaning: dropped for v1,
revisit on evidence.

### D4 — Charter authoring ownership  *(resolved by [[0016-trigger-owns-proactive-loop]])*
**Resolved:** Trigger owns it — charter authoring is a first-class Trigger workflow
(fast-follow after v1 core). pr-triage narrows to the reactive gate.

### D5 — Charter workflow design input  *(learnings landed → drafted)*
The first-class charter workflow was grounded in a **real run-through**: the codex-runtime
implementation in pr-triage (issue #129) was a manual test of the charter process. Those
learnings landed and are captured in [[front-of-loop-dogfood-crosswalk]]; they became the
input spec for the charter workflow's design, now drafted in [[charter-workflow]] with three
supporting ADRs — [[0018-ratified-contract-protection]],
[[0019-human-attention-budget-conformance-layers]],
[[0020-expected-red-and-topology-agnostic-review]] (all `deferred`, awaiting Dustin's
ratification). See [[0016-trigger-owns-proactive-loop]].

### D6 — Execution-mode split per step (interactive vs. autonomous) *(resolved → [[0021-step-execution-and-done]])*
**Resolved (2026-08-30):** steps carry an `execution` attribute (who does the work) and a
`done` attribute (what must be true to advance). `execution: agent | human | [agent, human]`
with the actual holder frozen per-run at claim time (resolve-once); `done` splits into
deterministic conductor checks (artifact schema + script checks) and judgment owned by the
workflow (`approve: human | agent`, the gate). Authority modes (ADR 0014) become the
per-job default that per-step `done.approve` may narrow — **superseding 0014** on placement.
The *leaning* is now an *eligibility*: ground + charter + author-red + implement accept
either `[agent, human]`; verify steps default `agent` (verdict artifact, no human ok); the
two gates are `approve: human`. The actual ratio of interactive vs. autonomous per step
stays emergent — observe real runs and let it settle.

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
