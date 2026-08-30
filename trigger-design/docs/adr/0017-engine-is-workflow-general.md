---
title: "ADR 0017 — The engine is workflow-general; the charter is not privileged"
status: accepted
date: 2026-08-29
tags: [architecture, scope, workflow, charter, principle]
superseded_by:
---

## Context

ADR 0016 makes charter authoring a first-class Trigger workflow. Because the charter is the
marquee use case — the validated process Dustin intends to use *most* of the time — there is a
real risk of over-fitting the whole app to it, letting "charter" leak into engine primitives.
That would make Trigger a "charter tool" rather than a general engine, and would couple the
engine's durability to one methodology that will itself evolve.

## Decision

**Trigger is a general declarative agent-workflow engine. The charter workflow is one workflow
pattern among many — first-class as a *use case*, never privileged in the *architecture*.**

- No engine primitive — triggers, steps, parking, authority, schema, API, events
  ([[0005-trigger-source-agnostic]], [[0008-workflows-deterministic-steps]],
  [[0011-internal-pubsub-two-buses]], [[db-schema]], [[daemon-api]]) — may special-case the
  concept "charter." The charter workflow uses the same primitives any workflow uses.
- Workflows with nothing to do with charters (a nightly dependency update, a scheduled report,
  a webhook-fired script) are **equally first-class**. The engine does not know or care whether
  a workflow is a charter workflow.
- The charter lives as a **workflow definition + agent defs + config**, on top of the engine —
  the same content-on-primitives rule that made [[0016-trigger-owns-proactive-loop]] safe.

**The test:** if a proposed engine feature only makes sense for charter work, it belongs in the
charter workflow's agent defs/config, not the engine. If deleting every charter concept would
break the engine, something leaked — fix the leak.

## Consequences

- **Durability:** the engine outlives the charter methodology. As the charter process evolves
  (or is swapped, or is used only sometimes), the engine is untouched.
- **Reinforces 0016:** the charter workflow stays honest content-on-primitives; it cannot
  quietly become the engine's center of gravity.
- **Breadth of use:** Trigger stays valuable for the large class of scheduled/triggered work
  that has no charter at all — which is most of what a scheduler does.
- **Design-review lever:** "does this privilege the charter?" is a standing question for any new
  engine feature. A yes means it belongs one layer up.
