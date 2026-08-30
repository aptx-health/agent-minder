---
title: "ADR 0019 — Human attention budget: three conformance layers, gates present scenarios not bindings"
status: accepted
date: 2026-08-30
tags: [architecture, charter, attention, conformance, gates, human-in-the-loop]
superseded_by:
---

<!-- Ratified 2026-08-30. Presentation detail lives in [[charter-gate-presentation]]. -->

## Context

The front-of-loop dogfood ([[front-of-loop-dogfood-crosswalk]]) found the charter process's
sharpest usability failure at the red-test ratification gate: the human was asked to ratify
**32 executable test bindings as if each were a separate product decision.** That exceeded
the attention budget — the human reasonably spot-checked rather than reading all of them,
which quietly defeats the point of a ratification gate.

The diagnosis was not "too many tests." The 32 bindings bound only **nine** charter
scenarios; the error was **conflating two different surfaces** — the human-decision surface
and the executable-test surface — and presenting the machine's resolution (bindings) where
the human's resolution (scenarios) belonged.

This is the same attention thesis pr-triage is built on (route attention *to* what matters
and *away* from what doesn't). The charter gates must obey it or they recreate the very
overwhelm the tool exists to remove — and they must obey it without privileging "charter" in
any engine primitive ([[0017-engine-is-workflow-general]]): this is a property of how gates
present, which is workflow-general.

## Decision

**Conformance lives in three distinct layers with distinct owners and count policies, and a
human ratification gate presents the human's layer (scenarios), never the machine's layer
(bindings).** *(Direction; presentation mechanics in [[charter-gate-presentation]].)*

| Layer | Primary owner | Count policy | Governance |
| --- | --- | --- | --- |
| **Charter scenarios** | Human | Target ~12 at most | Human ratifies each observable must / must-not behavior |
| **Contract-test bindings + goldens** | TDD / verifier agents | As many as needed | Changes to an existing binding/golden escalate; traced additive bindings are recorded and allowed ([[0018-ratified-contract-protection]]) |
| **Unit / regression / implementation tests** | Implementer / reviewer agents | Unbounded | Evolve freely, outside the protected manifest |

Direction:

1. **A ratification gate is scenario-first.** It presents at most ~12 scenario cards, each
   with its red evidence and a scenario→binding count/map. Full test code is
   progressive-disclosure detail, available on demand — never the primary decision surface.
2. **Bindings are agent-owned, not human-ratified individually.** The human ratifies the
   scenario and its observable contract; the machine owns how many bindings realize it.
   "34 bindings for 9 scenarios" is fine and is not 34 human decisions.
3. **A gate asks for a bounded number of decisions** (target ≤5 distinct questions), phrased
   as behaviors and boundaries, not code review.
4. **This is a general gate property.** Any Trigger workflow that parks on `awaiting_input`
   for human ratification presents at the human's resolution, not the machine's. The charter
   workflow is the first heavy user, not a special case.

## Consequences

- Ratification gates stay cheap and high-leverage; the human's scarce attention is spent on
  the ~dozen behaviors that carry meaning, not on hundreds of assertions.
- Requires the workflow/UI to carry a scenario→binding map and a progressive-disclosure view
  (a concrete demand on [[charter-gate-presentation]], the TUI [[tui]], and
  [[event-observability]]).
- Interacts with [[0018-ratified-contract-protection]]: the machine layer is protected
  mechanically precisely *because* the human does not read every binding — the two ADRs are
  complementary (human ratifies scenarios; machine guards bindings).
- Trade-off: a scenario-first gate can hide a bad binding behind a good scenario. Mitigated
  by builder≠verifier ([[charter-workflow]]) and by the drift protection of 0018 — not by
  asking the human to read everything.
- Open: the exact "scenario card" shape, and whether the ~12 scenario / ≤5 decision targets
  are hard caps or soft guidance.
