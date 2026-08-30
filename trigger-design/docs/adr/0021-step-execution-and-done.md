---
title: "ADR 0021 — Steps carry execution and done attributes; gates fold into done.approve"
status: deferred
date: 2026-08-30
tags: [architecture, workflow, steps, execution, definition-of-done, gates, authority, stations]
superseded_by:
---

<!-- deferred: ratified in conversation 2026-08-30; flip to accepted on Dustin's sign-off.
     Supersedes [[0014-answer-authority]]: job-level authority mode becomes the per-job
     default; per-step `done.approve` may narrow it. New: `execution` axis (who does the
     work) alongside the existing authority axis (who approves). -->

## Context

A workflow is an ordered sequence of steps ([[0008-workflows-deterministic-steps]]). Today
the engine knows exactly two ways a step gets done: the supervisor dispatches a headless
agent, or the run parks on `awaiting_input` and anyone authorized drops in an answer
([[0013-ask-and-resume-instead-of-bail]]). The "assembly line / station" observation: a step
can also sit on a board waiting for a worker to pull it, where the worker may be a solo
agent, a human, or a human+agent pair operating in the human's own tool. No new primitive —
a step with two attributes accomplishes it:

- **`execution`** — who does the work (the step's producer).
- **`done`** — what must be true for the conductor to advance past the step.

A **gate** is not a third kind of thing. It is a step with no producer and only an
`approve` entry in its `done` — the same `awaiting_input` parking mechanics
([[0013-ask-and-resume-instead-of-bail]], [[run-lifecycle-and-slots]]) at a different
richness: decision-only park (gate) vs. produce-an-artifact park (station).

Two prior decisions bound this:

- **The conductor never routes on an LLM decision** ([[0008-workflows-deterministic-steps]]).
  Nothing below may give the conductor a judgment call.
- **The engine is workflow-general** ([[0017-engine-is-workflow-general]]). Nothing below
  may privilege "charter." A station must be equally useful for a dependency-update
  workflow, an approval-gated deploy, anything.

## Decision

**A step carries two optional attributes — `execution` (who does the work) and `done`
(what must be true to advance). A gate is a step with only `done.approve`. Authority mode
([[0014-answer-authority]]) becomes the per-job default that a per-step `done.approve` may
narrow, never widen.**

### 1. `execution` — who does the work

| Value | Meaning | Mechanics |
| --- | --- | --- |
| `agent` | A headless agent run, dispatched by the supervisor | Today's behavior — no change |
| `human` | A human (or human+agent pair in the human's own tool, e.g. Claude Code), park-and-handoff | Parks on `awaiting_input` with a checkout; the human produces the artifact in their own worktree and resumes with it |
| `[agent, human]` | Either — an open station, claimed by whoever is available | Claim mechanics (below); actual holder frozen **per-run** at claim time (resolve-once) |

- Applies to `agent`-kind steps (where there is judgment to split). A `script` step is
  always the conductor's — **it is the "mech" worker**; a third execution value was
  rejected as redundant (ADR 0008 already declares scripts as deterministic steps).
- Default (badge absent): `agent` — unchanged, backward compatible.
- `human` execution is *park-and-handoff*, **not session-resume**: the human works in their
  own tool on the shared checkout and returns an **artifact**, not a session continuation.
  This is the same `awaiting_input` park as a gate, at produce-an-artifact richness.

### 2. `done` — what must be true to advance (definition of done, per step)

`done` splits into two layers, with the split owned by the conductor:

- **Deterministic, conductor-checkable:**
  - `artifact: <schema>` — the step's produced payload must validate against a declared
    JSON Schema (the same schema-validated answer path as [[0013-ask-and-resume-instead-of-bail]]).
  - `checks: [scripts]` — mechanical gates the conductor may run (e.g. EXIT 0, a file
    exists, an exact pattern/count check).
- **Judgment, owned by the workflow:**
  - `approve: human | agent` — an optional ratification the step parks on (a gate). Absent =
    nobody looks; the conductor advances on the deterministic layer alone.

The conductor owns schema; the workflow owns judgment. ADR 0008 is preserved because the
conductor's checks are deterministic; substantive verification (is this *really* matching the
spec? is it failing for the *right* reason?) is **more workflow content** — a verifier step,
or a human `approve` — never the conductor's call.

### 3. Authority mode: per-job default, per-step narrowing

[[0014-answer-authority]]'s modes (`human_only` / `interactive` / `orchestrator`) remain the
per-job default for answering parked runs. A per-step `done.approve` may only **narrow** the
job default (e.g. a job in `orchestrator` mode may declare one step human-only) — it may
never widen it beyond the mode's charter bounds. **Supersedes 0014** on this point.

### 4. Execution-mode per step is a per-run choice (resolve-once, not per-step rigidity)

`execution` declares **eligibility**. A menu value (`[agent, human]`) freezes the *actual*
holder on the run at claim time (the [[config-resolve-once]] discipline). This honors the
charter-workflow observation that "the same logical step can run either way" — execution mode
is a per-run choice, framed by what the step declares eligible.

### 5. Open stations and the claim rule

- An open station parks as `awaiting_input` with a **claim surface**: who may claim, and
  the artifact/schema required. The supervisor never auto-dispatches a station an agent
  could claim while a human is available — claiming is pull, not push. (Whether an agent
  may claim an open station after a human grace period is deferred — see open questions.)
- A claim is a **lease with identity**, recorded on the run. Claim identities make
  **builder≠verifier enforceable deterministically** — a workflow may declare that the
  entity which produced an artifact (claimed, ran, or verified a prior step) may not claim
  a verifying step. This is the general, engine-safe form of independent verification.
- Abandonment: a claimed station that goes silent has no hard deadline by default; a lease
  may be revoked by the operator. Whether a `fallback_after` (timeout → eligible agent
  reclaims) ships is deferred ([[research/open-questions]]).

### 6. Surfacing — stations are parked runs, period

No new bus, API, or MCP surface: a claimable station **is** a parked run
([[run-lifecycle-and-slots]]), exposed through the existing parked list
([[daemon-api]]), TUI (parked headline), and MCP `list_parked` / `answer_parked`
([[0007-agent-controllable-mcp-server]]). The event bus already emits
`run.awaiting_input` / `run.answered` ([[event-observability]]). The only new Service verbs
are Claim and Return (station-specific); the work bus is untouched.

## Consequences

- **Assembly line without new machinery.** "Station" = a step with `execution` + `done`;
  the full human↔agent spectrum (headless agent → park-and-handoff → open station) is a
  single mechanism with zero new run states.
- **Gates fall out of `done.approve`.** No separate gate concept in the engine; a gate is
  a step whose only `done` entry is approval.
- **ADR 0008 honored by construction.** The conductor advances on deterministic checks
  only; judgment is always a step (verifier) or a human `approve`.
- **0017 honored.** No engine primitive says "charter"; the same attributes run a
  dependency-update step, an approval-gated deploy, anything.
- **One worktree per run by default**; steps switch branches within it via scripts. The
  two-level PR stack (charter branch → test/implement children) is **git topology + script
  steps**, not engine state. GitHub's issues/PRs are a **projection** of Trigger's state,
  not a store of it — the engine is wedded to git, not GitHub.
- **Fan-out stays out of the engine.** Workflow 1 (charter setup: ground → gates → red →
  verify → ratify) is linear; the implementation fan-out is the **orchestrator's move**,
  fired as separate runs once workflow 1 completes (see D2/D6). The engine never spawns
  child runs.

## Superseded / affected

- **Supersedes [[0014-answer-authority]]** on authority placement: job-level default mode
  + per-step narrowing (0014's charter-envelope logic for *what* an orchestrator may answer
  stays in force). Mark 0014 `superseded_by: 0021`.
- Resolves the authority half of [[open-questions]] D6; the execution-mode-ratio question
  stays emergent per D6.

## Open questions

- `fallback_after`: should an open station let an *agent* claim it after a human grace
  period expires? (Abandonment of human-claimed stations — see
  [[0022-station-completion-contract]].)
- Whether a claimed station must hold a checkout lease (refire/parallel-station collision on
  worktrees) or the one-worktree-per-run model already excludes it.
- Whether `done.checks` is finite (declared scripts) in v1 or grows a tiny expression
  language.
- Where `execution` merges into [[config-schema]] R3 (per-step fields), and whether the
  eligibility menu deserves its own Claim/Return verbs on the Service before the TUI needs
  them.