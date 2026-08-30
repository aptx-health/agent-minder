---
title: "ADR 0001 — Purpose and minimal scope"
status: accepted
date: 2026-08-29
tags: [product, scope, purpose]
---

## Context

agent-minder grew autopilot-first over a year of exploration. It works, but it is a
hodgepodge: the design records the learning path, not a target. Dustin now knows the
target and it is smaller. pr-triage supplies the discipline (charter, ADRs, local-state
invariant). The question was whether to refactor agent-minder in place or start fresh.

## Decision

Trigger is a **new, minimal tool**, not agent-minder v2. Its purpose: **run declarative
scheduled and triggered agent jobs from one local daemon, against swappable runtimes,
with local state as the source of truth.** It leads with the scheduler/trigger core,
not with autopilot. Everything agent-minder accreted that is not in that core is out of
scope by default (review, auto-merge, dependency graphs, lessons, GitHub-centric state).

Starting fresh is justified because: the scope shrank (not grew); the target is now
known; the local-state-authoritative invariant is hard to retrofit into agent-minder's
GitHub-first design and free greenfield; and the rebuild is the first dogfood of the
pr-triage methodology on a domain Dustin knows cold.

## Consequences

- Smaller surface, fewer moving parts, easier to reason about and maintain.
- The rewrite risk (losing hardened edge-case handling) is mitigated by **harvesting**
  proven agent-minder/pr-triage packages rather than rewriting them — see
  [[harvest-map]].
- agent-minder keeps running untouched until Trigger reaches parity on the pieces
  actually used (spikes, cron, GitHub one-offs). No flag day.
- Autopilot fan-out is not abandoned, only deferred; whether it lands in v1 or as a
  fast-follow is an open decision ([[open-questions]]).
