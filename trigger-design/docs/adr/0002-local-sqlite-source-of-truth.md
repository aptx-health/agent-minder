---
title: "ADR 0002 — Local SQLite is the single source of truth"
status: accepted
date: 2026-08-29
tags: [state, sqlite, architecture, portability]
---

## Context

agent-minder leaned on GitHub artifacts (labels, checks) as de-facto app state. pr-triage
ADR-0006 identified that as the weakest part of that design and fixed it: local state is
authoritative, GitHub is a projection, and a GitHub-derived value must never flow back and
mutate the app's own state. Trigger adopts that lesson from day one instead of learning it
the expensive way.

## Decision

**Local SQLite is the single source of truth for everything Trigger manages and every
decision it makes.** External systems play exactly two roles, neither of which is app
state:

- **Inbound signals** — events Trigger ingests (a webhook, a cron tick, a GitHub event).
  Trigger records its own interpretation as local state; it does not treat the external
  artifact as state.
- **Outbound projections** — anything Trigger writes to an external system to communicate
  with people or tools not running Trigger. These are reconciled one-directionally from
  state, idempotently.

Reconciliation is one-directional: state → external. On divergence, local state wins.

SQLite specifics inherit agent-minder's hardening: WAL mode, foreign keys, single-writer
(`SetMaxOpenConns(1)`), and WAL-recovery on open. Harvest `sqliteutil`.

## Consequences

- Whole classes of feedback-loop bugs become impossible by construction.
- Restart-safety is a first-class invariant: killing the daemon loses no state.
- Provider portability follows for free — every external system is one adapter, so
  GitLab, a plain git remote, or a message bus are additional adapters later.
- Design rule for every feature: decide first what local state changes; treat any
  external read as an inbound signal to interpret, any external write as an outbound
  projection. See [[0005-trigger-source-agnostic]].
