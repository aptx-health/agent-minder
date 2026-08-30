---
title: "ADR 0016 — Trigger owns the proactive loop; the charter is a Trigger workflow"
status: accepted
date: 2026-08-29
tags: [architecture, boundary, charter, workflow, front-of-loop, pr-triage]
superseded_by:
---

## Context

Earlier the boundary put the front-of-loop (charter authoring, red-first tests) in pr-triage
and made Trigger the execution engine that *consumes* a charter. But Trigger's execution model
matured to the point where the front-of-loop **is** a Trigger workflow — the charter
directly leads into the implementation Trigger already runs, with no natural handoff between
them. Keeping charter authoring in a different tool would split one continuous pipeline across a
repo boundary.

The charter kickoff pipeline —
`charter-agent → tdd-agent (red tests) → [human ratifies] → implementer (green) → verifier → open PR` —
maps onto primitives Trigger already has:
- ordered deterministic steps, agent steps with per-step runtime/model ([[0008-workflows-deterministic-steps]]);
- **the ratification gates are `awaiting_input`** — charter and red tests pause for the human, then resume ([[0013-ask-and-resume-instead-of-bail]]);
- who may ratify is the authority model ([[0014-answer-authority]]).

pr-triage's own front-of-loop spike (§7) rejected a separate companion app and chose to bundle
the front-of-loop with the execution substrate because they share ~80% of it. **Trigger is that
substrate.** So bundling the charter with the engine is what the spike argued for — it just
lands in Trigger.

## Decision

**Trigger owns the whole proactive loop. Charter authoring is a first-class Trigger workflow,
not a separate tool's job.**

- The charter front-of-loop is expressed as a **workflow definition + three agent defs**
  (charter, TDD, verifier) on Trigger's existing primitives — an *application* of the engine,
  not new machinery. Ratification gates are `awaiting_input`; authority is ADR 0014.
- **Trigger authors and consumes the charter.** It is now an internal artifact, so the
  "charter contract" is internal, not a cross-repo interface. ADR 0009/0014 charter references
  become internal to Trigger.
- **Sequencing: fast-follow, not v1 core.** Ship the engine first (scheduler, triggers,
  workflows, runtimes, parking, ask-and-resume). The charter workflow is then the **flagship
  first real workflow** — and the ultimate dogfood, since Trigger runs its own front-of-loop.
- **Trigger core stays charter-free.** A cron/script/agent job needs no charter; `within_charter`
  is `unknown` when none is present, and `unknown` never reads as approval
  ([[db-schema]]/[[0014-answer-authority]]). The charter is a workflow you opt into, not a
  requirement of the engine.

## pr-triage after this

- **pr-triage narrows to the reactive gate** — it reviews the PR Trigger opens. That half is
  built and working, so it stays **standalone for now** and dogfoods Trigger's build by
  reviewing Trigger's own PRs. The charter travels with the PR as the grading oracle.
- **Future option (not now):** absorbing the reactive gate into Trigger as a (complex) review
  workflow may make sense once Trigger is mature — deferred, decide on evidence.

## Grounding the design

The charter workflow's design will be grounded in a **real run-through**, not invented: the
codex-runtime implementation currently being done in pr-triage is a **manual test of the charter
process**. Its learnings and design docs are saved and become the input spec for Trigger's
charter workflow ([[open-questions]] D5).

## Consequences

- One continuous pipeline (charter → tests → implement → verify → PR) with no mid-loop repo
  handoff; the only seam left is the loose PR → review over GitHub.
- Supersedes the earlier "pr-triage takes the front-of-loop" boundary. Trigger = proactive
  engine + charter; pr-triage = reactive gate.
- Charter-dependent features (authority envelope, `within_charter`, scope grants) now have a
  producer in the same tool — no cross-repo contract to keep in sync.
- Scope risk (Trigger growing beyond a minimal engine) is bounded by the fast-follow rule and
  by charter authoring being content-on-primitives, not a new subsystem. If it ever needs its
  own machinery, that is a new ADR.
- The charter workflow needs its own design doc (deferred until the codex exercise learnings
  land); this ADR fixes only *where it lives* and *that it is a workflow*.
- The engine must not be designed *around* the charter — it is one workflow pattern, not the
  organizing principle ([[0017-engine-is-workflow-general]]).
