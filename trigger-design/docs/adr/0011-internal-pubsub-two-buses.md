---
title: "ADR 0011 — Internal pub/sub: a work bus and an event bus, both store-first"
status: accepted
date: 2026-08-29
tags: [architecture, pubsub, bus, events, daemon, correctness]
superseded_by:
---

## Context

Trigger's internals are naturally publish/subscribe: trigger sources produce fires, a supervisor
consumes them, and many observers (TUI, MCP, external sinks) want to watch progress. A bus is
the natural shape. The risk is modeling it as *one* bus with one delivery contract — because
two different concerns ride it, with opposite guarantees, and conflating them creates
lost-work and lost-visibility bugs.

The backend must also be **replaceable**. SQLite is the right bus for v1 (dozens of
concurrent runs, not thousands — no Redis/BullMQ-scale throughput needed), but the design
must not weld the daemon to it.

## Decision

**The daemon has two in-process buses, both store-first (commit is the publish), backed by
SQLite behind a replaceable interface. No external broker in v1.**

Both buses sit behind a narrow backend interface — a `WorkQueue` (enqueue, claim, requeue,
set-status) and an `EventLog` (append, subscribe). SQLite is the one implementation today. A
future backend (e.g. Redis/BullMQ-style) is a new implementation of the same interface, not a
daemon rewrite — the store-first `commit-is-publish` contract is what any backend must honor.
This is extract-on-force: build only the SQLite backend now, but keep the seam clean.

### 1. Work bus (control plane) — causes work; must not drop
- Trigger sources (and API/MCP endpoints) publish a `Fire` by **committing a `queued` run
  row** to SQLite. That commit is the publish.
- A live in-memory notification wakes the supervisor immediately; a **fallback tick** re-scans
  for `queued` rows so a missed notification never loses work.
- The supervisor **claims** a row (single-writer, [[0004-daemon-interface-split]]) and applies
  dedup ([[trigger-abstraction]]) before starting. Delivery is at-least-once, made safe by
  idempotent claim + dedup.
- Consumed **once**: one queued run → one claim → one workflow run.

### 2. Event bus (observability) — reports work; best-effort fan-out
- The supervisor, sources, and each step publish events by **committing to the durable event
  log** (harvest agent-minder's store-first event log). Commit is the publish.
- After commit, events **fan out live** to many subscribers: the TUI, the MCP stream
  ([[0007-agent-controllable-mcp-server]]), and external event sinks (harvest
  `internal/eventsink`). External delivery is best-effort with bounded queues.
- Consumed **by many**; a slow or absent subscriber never blocks the committer.

### The guard
**Control flow rides the work bus, never the event bus.** A subscriber missing an event must
never mean a workflow does not run. Queued run rows and run records are authoritative
([[0002-local-sqlite-source-of-truth]]); the event bus is a projection of that truth.

## Consequences

- Durable pub/sub with no broker: SQLite + store-first gives at-least-once work delivery and
  fan-out observability in one local process.
- The two hardest bugs are excluded by construction: **lost work** (work bus is durable, not
  a bare channel) and **lost visibility blocking progress** (event bus is best-effort and
  downstream of commit).
- Trigger sources and the supervisor are decoupled — new source kinds or new observers are added
  without touching each other.
- **Internal bus vs. external bus adapter are distinct.** An external message bus is a future
  *trigger source* that publishes fires *into* the work bus ([[0005-trigger-source-agnostic]])
  — not a replacement for this internal architecture.
- No external broker until a real force (multi-process, multi-host, or throughput SQLite
  cannot hold) — extract-on-force. Because both buses sit behind the backend interface, such a
  broker slots under the same commit-is-publish seam without touching the supervisor.
