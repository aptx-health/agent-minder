---
title: "ADR 0005 — Trigger-source-agnostic (GitHub is one adapter)"
status: accepted
date: 2026-08-29
tags: [architecture, triggers, github, portability]
---

## Context

agent-minder is GitHub-centric: issues drive the work, labels route it, and GitHub is
woven into the core. pr-triage taught that this is a lock-in trap — the moment the tool
assumes a GitHub-only artifact, it is a GitHub App with extra steps. The name Trigger says
the firing mechanism is the core concept, and firing should not mean "a GitHub event."

## Decision

**A trigger is an abstraction.** The daemon defines one internal `Trigger` concept — a
source that produces a job to run. GitHub is one implementation, not the center. v1
implementations:

- **cron** — the clock fires a scheduled job (declarative config).
- **manual** — a CLI/API call fires a one-off.
- **webhook / external** — an inbound HTTP call fires a job (the "spun up from an external
  trigger" case).
- **github** — a GitHub event (issue, label, comment) fires a job — one adapter among the
  above, treated as an inbound signal per [[0002-local-sqlite-source-of-truth]].

Message-bus and file-watch triggers are future adapters behind the same abstraction. An
**orchestrating agent** firing a job is not a new transport — it reaches the manual/external
path over the MCP surface ([[0007-agent-controllable-mcp-server]]).

## Consequences

- GitHub is decoupled from the core; Trigger runs usefully with zero GitHub involvement.
- Adding a trigger source is writing one adapter to a known interface, not touching the
  scheduler.
- The declarative config describes triggers uniformly (a job says what fires it), so cron,
  webhook, and GitHub jobs read the same way.
- Guardrail: no trigger adapter may write app state directly; it emits a signal the daemon
  interprets. The day an adapter's artifact becomes state, the portability claim is gone.
