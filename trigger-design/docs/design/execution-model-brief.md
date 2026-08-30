---
title: "Execution and handoff model (design)"
status: draft
date: 2026-08-30
tags: [design, stations, assembly-line, execution, handoff, workflow, definition-of-done]
related: "[[0021-step-execution-and-done]], [[0022-station-completion-contract]], [[0008-workflows-deterministic-steps]], [[0013-ask-and-resume-instead-of-bail]], [[0014-answer-authority]], [[0017-engine-is-workflow-general]], [[charter-workflow]], [[run-lifecycle-and-slots]], [[config-resolve-once]], [[db-schema]], [[daemon-api]]"
---

# Execution and handoff model (design)

The engine's view of a step: **who does it, and what "done" means.** This is the design
doc for [[0021-step-execution-and-done]] (the step attributes) and the station/assembly-line
conversation it came out of. It is engine-general ([[0017-engine-is-workflow-general]]); the
charter workflow is the worked example throughout.

## The model in one screen

A workflow is a conveyor of stations. Each step declares:

- **`execution`** — who may do the work: `agent` (headless dispatch, today's behavior),
  `human` (park-and-handoff to a human or human+agent pair in the human's own tool), or a
  menu `[agent, human]` (an open station, claimed by whoever is available). Badge absent →
  `agent`. The actual holder is frozen per-run at claim time (resolve-once).
- **`done`** — what must be true to advance:
  - `artifact: <schema>` — produced payload validates (deterministic).
  - `checks: [scripts]` — mechanical gates (deterministic).
  - `approve: human | agent` — optional ratification park (a gate). Absent = nobody looks.

A **gate** is a step with no execution and only `approve`. A **station** is a step with a
producer — same parking mechanics, at produce-an-artifact richness. No new run states: a
claimable station *is* a parked run ([[run-lifecycle-and-slots]]), exposed via the existing
parked list, TUI, and MCP surfaces ([[daemon-api]]).

## The two layers of "done" (who owns what)

| Layer | Owner | Example |
| --- | --- | --- |
| Artifact schema + mechanical checks | **the conductor** (deterministic, ADR 0008) | "verify-report.yaml present + valid; counts tests vs. scenarios" |
| Substantive judgment (matches the gherkin, failing for the *right* reason) | **the workflow** — a verifier step, or a human `approve` | "verifier confirms red-for-the-intended-reason" |

The conductor advances on the deterministic layer alone. Judgment is always a step or a
human, never the conductor's call. This is what keeps "station" from turning the engine
into a quality judge.

## Worked example: the charter flow

Annotated from [[charter-workflow]] with the two attributes:

| # | Step | execution | done (deterministic) | done (approve) | Spaces / notes |
| --- | --- | --- | --- | --- | --- |
| 1 | Ground | `[agent, human]` | charter draft artifact | — | "You may grab it interactively" |
| 2 | Charter gate | — | — | **human, always** | The envelope; not delegatable |
| 3 | Author red tests | `[agent, human]` | test PR artifact | — | The doc's own "per-run choice" example |
| 4 | Verify red | agent | verdict: counts + gherkin match | — | Verifier produces a *verdict artifact*; no human ok |
| 5 | Red gate | — | — | **human** | Scenario packet presentation (ADR 0019) |
| 6 | Implement to green | `[agent, human]` | green + PR artifact | — | Scope escalations inside governed by job authority |
| 7 | Verify green | agent | verdict + contract-integrity | — | No human ok before PR opens |
| 8 | Open PR | script | PR opened | — | The PR opens with zero human look (human may close it later) |

## What the walk teaches

1. **The gates do what the old design wanted — if presentation earns it.** Step 5 already
   puts a human-eyes checkpoint after verify-red. The historical weakness was evidence
   delivery (ADR 0019), not authority. The gate packet carries the verifier's findings +
   scenario-first evidence ([[charter-gate-presentation]]); the human's "ok" is a review,
   not a ritual.
2. **Open stations need a claim-uniqueness rule.** If verify steps become grab-able, the
   entity that implemented must not be the entity that verifies — builder≠verifier as a
   deterministic claim rule off claim identities. This is engine-general (any flow with an
   independent check) and is the anti-cheat that makes open markets safe.
3. **The "mech" idea dies into existing machinery.** A third worker kind was rejected: the
   conductor already runs `script` steps. The badges only matter on `agent` steps. "Mech" as
   a *gate approver* was rejected entirely — an authority holder that auto-advances is just
   skipping the gate.
4. **Git is truth-adjacent, not the engine.** One worktree per run; steps switch branches
   within it via scripts. The two-level PR stack (charter branch → test/implement children)
   is git topology + script steps, not engine state. GitHub's issues/PRs are a *projection*
   of Trigger's state, not a store of it — the engine is wedded to git, not GitHub. (This
   keeps the engine usable for non-GitHub workflows entirely.)
5. **Fan-out is a separate workflow, not engine machinery.** Workflow 1 (charter setup:
   ground → gates → red → verify → ratify) is linear. Workflow 2 (the implementation fan-out:
   N independent sub-workstreams in parallel) is the orchestrator's move, fired as separate
   runs once workflow 1 completes ([[open-questions]] D2, and the gap recorded in
   [[0022-station-completion-contract]]). The engine never spawns child runs.

## The two holes deliberately left open

- **Resume = push-button, not reconcile (v1).** When a human gate returns, the station
  presents the artifact ("charter.md ready", "red tests ready") and the human hits **READY**.
  No fragile read-back validation in v1 (`git verify` existence checks are a later addition,
  on evidence — a mechanical validation we don't yet know enough to build non-fragile).
- **Claim abandonment has no deadline by default.** A claimed station that goes silent
  simply waits; a lease may be revoked by the operator. Whether a `fallback_after` (grace
  period → eligible agent reclaims) ships is an open question
  ([[0021-step-execution-and-done]]).

## Design-rule recap (the 0017 filter, applied)

- Anything that only serves charters → belongs in the charter workflow's step list.
- Anything genuinely general → belongs here, in the engine.
- Stations, `execution`, `done`, gates-as-steps, claim-uniqueness: engine-general.
- The chunk-charter lifecycle, "expected_red", ratified scenarios: charter-workflow content.