---
title: "ADR 0022 — Station completion contract: a workflow may declare a machine-checkable 'done and fan-out may begin' contract"
status: superseded
date: 2026-08-30
tags: [architecture, workflow, completion, contract, fan-out, orchestrator, deferred, stations]
superseded_by: "0023-handoff-packet-and-pickup-verb"
---

<!-- deferred: recorded at close of the 2026-08-30 station design conversation. Direction
     decided; mechanism deferred — the hole intentionally remains open for future noodling.

     SUPERSEDED 2026-08-30 by [[0023-handoff-packet-and-pickup-verb]]: the machine-to-machine
     completion *signal* direction is dead. The seam is the operator ("me deciding") and the
     answer is a packet + a pickup/handoff verb, not an engine event. Kept as history; the
     grammar/signal shape discussion below is now moot. -->

## Context

The assembly-line / station conversation (see [[0021-step-execution-and-done]]) closed with
a deliberate split:

- **Workflow 1** (e.g. charter setup: ground → gates → red tests → verify red → ratify) is
  **linear**, driven by the dumb conductor ([[0008-workflows-deterministic-steps]]).
- **Workflow 2** (e.g. the implementation fan-out: N independent sub-workstreams in
  parallel, on issues/sub-issues of the chunk) is **not engine machinery** — it is the
  orchestrator's move ([[0014-answer-authority]], [[daemon-api]]), fired as separate runs
  after workflow 1 completes, with fan-out explicitly out of v1 ([[open-questions]] D2).

That split leaves one hole: **how does workflow 2 know workflow 1 is done?** Today the
engine's only completion signal is a run status (`succeeded`). "The conduit finished" is not
the same as "the charter is ratified, the red tests are accepted, the artifacts are in
place, and the fan-out may begin." The orchestrator (or a human) must currently eyeball the
state and *decide* the handoff. That decision is exactly the kind of thing conventions are
made of — and conventions need a home, machine-checkable, or they quietly drift.

The general question, workflow-agnostic ([[0017-engine-is-workflow-general]]): should a
workflow be able to declare a **completion contract** — a small machine-checkable statement
of "this workflow is done, and downstream consumers (people or orchestrators) may begin" —
that the engine can evaluate and surface as one durable signal?

## Decision (direction)

**A workflow may declare an optional, machine-checkable completion contract: the set of
conditions that are true when the workflow's outcome is ready for downstream consumers. The
engine evaluates it deterministically ([[0008-workflows-deterministic-steps]]) after the
final step and surfaces a single durable signal; downstream consumers subscribe instead of
eyeballing state.** *(Direction; mechanism deferred.)*

Shape in mind — the conditions are exactly the deterministic half of each step's `done`
([[0021-step-execution-and-done]]), gathered over the workflow: required artifacts present
and schema-valid, required scripts/checks green, required recordings (`branch`, `pr`,
`charter_version`…) present in the run record. Because it is composed of already-checked
deterministic pieces, it adds no new judgment to the engine — it is a **summary**, evaluated
by the conductor like any other deterministic check, not a new authority.

The hole that remains open for future noodling:

- **The grammar.** Is the contract a static config block on the job (declarative list of
  must-be-true conditions), or a derived snapshot ("all `done` blocks satisfied over the
  final step set")? Standing tension: a static block is transparent but duplicated config; a
  derived snapshot risks becoming an implicit side condition the flow never rechecks.
- **Who reads the signal.** The orchestrator over MCP (`list_runs` / `subscribe_events`),
  the human in the TUI (a "ready" badge), and any future consumer. Should the signal be a
  run status (new terminal-ish value), an event (`workflow.ready`), or both?
- **Whether it is *required* for workflow 2, or workflow 2 simply reads the artifacts
  itself.** The orchestrator already can: "charter.md + ratified + red tests accepted" is
  inspectable state. The contract earns its keep only if it makes that inspection one
  durable signal instead of a per-consumer re-derivation.
- **Fallibility.** A contract that asserts more than the final step checked adds its own
  failure mode (false "ready"); a contract that asserts less is theater. The discipline must
  be: the contract **never adds checks the workflow does not already run**; it only
  aggregates them.

## Consequences

- Closes the "when may the fan-out begin" ambiguity with one durable, deterministic signal
  (once the mechanism lands).
- Gives the orchestrator a native handoff hook — instead of eyeballing run state, it
  subscribes for `ready`.
- Engine-general: any workflow with a downstream consumer (a deploy gate, a report handoff)
  benefits; the charter fan-out is the first heavy consumer, not a special case.
- Costs: a contract block is new config; evaluation is a new deterministic step type;
  `ready` needs a place in the run state machine and the event taxonomy.
- Deliberately **deferred**: the mechanism, the grammar, and the event shape are future
  noodling. This ADR records that the direction is agreed and the hole is open.

## Open questions

- Grammar: static block vs. derived snapshot (above).
- Signal shape: new run status vs. `workflow.ready` event vs. both.
- Relationship to [[run-lifecycle-and-slots]] transitions and
  [[0019-human-attention-budget-conformance-layers]] presentation (a "ready" badge is
  attention-routing for the human and the orchestrator alike).
- Interaction with [[0018-ratified-contract-protection]] (a completion contract that
  includes protected-artifact integrity becomes a strong handoff gate).