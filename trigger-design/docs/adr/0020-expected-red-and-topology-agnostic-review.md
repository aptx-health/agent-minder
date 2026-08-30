---
title: "ADR 0020 — Expected-RED is an explicit run phase; review intent is decoupled from branch topology"
status: accepted
date: 2026-08-30
tags: [architecture, charter, workflow, state, topology, review]
superseded_by:
---

<!-- Ratified 2026-08-30. Mirrors pr-triage ADR 0010 (topology-agnostic workflow state).
     General schema fields (phase incl. expected_red, review_intent) in [[db-schema]]. -->

## Context

The front-of-loop dogfood ([[front-of-loop-dogfood-crosswalk]]) surfaced two coupling bugs
that both trace to leaning on GitHub state as workflow truth:

1. **Expected RED looked like breakage.** The charter workflow deliberately produces a red
   test suite at ratification (red-first), then drives it green. In a draft-PR projection
   that failing state is a *useful* cue — but a workflow that reads CI/GitHub as truth cannot
   tell "expected RED at the ratified checkpoint" from "the build is broken." The phase must
   be explicit durable state, not inferred from a red check.
2. **Review intent was inferred from the base branch.** In pr-triage, a PR opened against
   `main` was auto-classified `chunk_completion` and force-escalated; a clean implementation
   PR could not be routed to a reviewer without retargeting branches. Review *role* was
   coupled to branch *topology*, and the destination-base selector allowed only one base per
   repo — so the dogfood PR was invisible until the base filter was hand-changed.

pr-triage recorded the durable lesson as its ADR 0010 (topology-agnostic workflow state).
Trigger already holds the same principle at the storage layer
([[0002-local-sqlite-source-of-truth]]) — but the *charter-phase* concept and the
*decoupling of review intent from topology* are new and need fixing before the charter
workflow is built, so they do not get re-learned.

Per [[0017-engine-is-workflow-general]], neither may special-case "charter": expected-RED is
one value of a general run/step phase, and review-intent-as-data is a general workflow
property.

## Decision

**Chunk/charter lifecycle phase — including an explicit `expected_red` phase — is
authoritative in Trigger's own state, independent of GitHub branches, PRs, and CI; and a
run's review intent is carried as explicit data, never inferred from branch topology.**
*(Direction; state fields drafted in [[charter-workflow]] and [[db-schema]].)*

Direction:

1. **Expected-RED is a first-class, durable phase.** A charter run records that it is
   *expected* to be red at ratified checkpoint X. A red result at that phase is conformance,
   not failure; a green result where red was expected is itself a signal. This is a general
   run/step phase value, usable by any test-first workflow.
2. **GitHub is a projection, not the state.** Branch stacks, draft PRs, and CI are optional,
   swappable *views* and evidence. The durable truth — charter version, ratification events,
   protected checkpoint ([[0018-ratified-contract-protection]]), expected phase, verifier
   verdict, review outcome — lives in Trigger and survives with no GitHub at all
   ([[0002-local-sqlite-source-of-truth]], [[db-schema]]).
3. **Review intent is explicit data.** Whether a run wants an agent review, a human gate, or
   nothing is a declared field on the run/workflow, not a deduction from its base branch. The
   charter travels with the PR as the reviewer's grading oracle regardless of topology.
4. **Trigger sources may watch multiple bases.** The single-base limitation is a known gap;
   the direction is explicit support for multiple base selectors at the GitHub trigger
   boundary, without making "watch everything" the default ([[0005-trigger-source-agnostic]],
   [[trigger-abstraction]]). Exact syntax deferred.

## Consequences

- The charter workflow's red→green lifecycle is legible and restart-safe without depending on
  any Git host; killing GitHub loses evidence, not truth.
- Any team's branch workflow (trunk, stacked PRs, feature branches) becomes a projection the
  same durable state maps onto — Trigger is not wed to one topology.
- Requires run/step state to carry a phase (incl. `expected_red`) and a review-intent field —
  a concrete addition to [[db-schema]] and [[run-lifecycle-and-slots]].
- Requires GitHub trigger sources to support multiple base selectors — a real change to the
  trigger config schema ([[config-schema]]) and poller.
- Trade-off: more explicit state to maintain than "just read GitHub," bought deliberately for
  durability and topology-independence.
- Open: multi-base selector syntax; how `expected_red` renders in CI/status without weakening
  ordinary merge gates; whether phase is a run column or a step column.
