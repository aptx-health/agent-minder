---
title: "ADR 0018 — Ratified-contract protection: immutable checkpoint + digest manifest + drift signal"
status: deferred
date: 2026-08-30
tags: [architecture, charter, governance, conformance, workflow, checkpoint, deferred]
superseded_by:
---

<!-- deferred: records the decision direction; mechanism detail lives in [[charter-workflow]].
     Awaiting Dustin's ratification — flip to accepted on sign-off. -->

## Context

The pr-triage front-of-loop dogfood (codex-runtime, issue #129 — see
[[front-of-loop-dogfood-crosswalk]]) exposed the one governance hole that the charter
methodology cannot survive without: **an implementer can launder a bug by editing the very
tests a human ratified, or by rewriting a golden fixture to match buggy output.** Once the
red tests are the agreed oracle, nothing mechanical stops a later step from quietly
weakening them.

The dogfood confirmed the hole is real in pr-triage today: its scanner flags deleted test
files and new `t.Skip`, but **not** modified assertions or changed expected values, and its
pre-scan deliberately excludes every `testdata/` path — so golden changes are invisible.
Ratification was therefore *social/manual*: the human took a separate checkpoint commit and
diffed protected files by hand. That worked once but does not scale and is not durable.

A single watched path is also insufficient. A normal PR diff (merge-base → head) cannot tell
that a test was ratified at an intermediate commit and rewritten later in the same PR. The
protection needs an **immutable reference point** plus **per-artifact identity**, not a path
glob.

Two forces pull against each other, and both must be honored:

- **Freezing kills learning.** The dogfood *added* two legitimate negative tests during
  smoke; blocking all test changes would have blocked a real improvement.
- **Silent weakening is the whole threat.** Removing, weakening, renaming, or skipping a
  ratified binding — or changing a golden oracle — must not pass unnoticed.

ADR 0017 forbids privileging "charter" in engine primitives. So the protection cannot be a
charter-special case: it must be a **general engine capability** ("a human ratified some
artifacts at a checkpoint; later steps must not silently mutate them") that the charter
workflow is simply the first and primary consumer of.

## Decision

**Trigger provides a general ratified-checkpoint capability: a workflow step may register an
immutable checkpoint plus a manifest of protected-artifact identities; a later step or gate
deterministically detects drift against it and escalates. The charter workflow uses this to
protect ratified scenarios, test bindings, and golden fixtures — it is not charter-specific
machinery.** *(Direction; mechanism drafted in [[charter-workflow]].)*

Direction the mechanism will follow:

1. **Immutable checkpoint outside the mutable diff.** At a ratification gate the workflow
   records an immutable object (commit/tree SHA, or a stored digest set) that the
   implementer cannot rewrite. Comparison is checkpoint-object → current head, not
   merge-base → head.
2. **A protected manifest of identities, not a path glob.** The manifest lists each
   protected artifact by a stable id plus a content digest — every ratified test body and
   every golden oracle. File-level hashes alone are too coarse: they cannot tell laundering
   from a useful additive test.
3. **A deterministic drift signal.** Weakening, changing, skipping, renaming, or removing a
   protected binding — or changing a golden, the manifest, or its checkpoint pointer — emits
   one signal (working name `ratified_contract_changed`) carrying evidence. Protected
   goldens override any generic `testdata/` exclusion.
4. **Drift escalates; it does not freeze.** The signal maps to human escalation by default
   (the parking/authority path, [[0012-failure-handling-blocked-and-release]],
   [[0014-answer-authority]]). The contract is allowed to *evolve* — the change is surfaced
   for a human, never silently accepted and never absolutely frozen.
5. **Additions traced to an existing ratified scenario are allowed and recorded**, not
   blocked. A new observable requirement, a changed expected value/oracle, or any
   weakening/removal returns to the human. This is the freezing-vs-learning resolution.

Deferred here (belongs to design/implementation, not this ADR): the manifest's storage
location and owner, the exact digest scheme, and how "traces to an existing scenario" is
decided mechanically vs. by an agent.

## Consequences

- The charter methodology gains the mechanical backstop it was missing; ratification stops
  being an honor system.
- Because it is a general capability, any workflow with a human-approved artifact (not only
  charters) can protect it — 0017 stays satisfied.
- Cost: the workflow must commit an immutable checkpoint at each ratification gate and carry
  a manifest through to the drift-check step; that is real state and a real comparison step,
  not free.
- Ties directly to [[db-schema]] (where the checkpoint/manifest state lives),
  [[event-observability]] (drift is an event), and [[0014-answer-authority]] (a drift
  escalation is answered under the authority model).
- Open: whether the manifest is a run artifact, a file in the repo, or both; who computes
  and verifies digests; and whether the "additive vs. weakening" classification is
  deterministic or delegated to the verifier agent.
