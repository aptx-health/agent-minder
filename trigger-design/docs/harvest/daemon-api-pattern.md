---
title: "Harvest: daemon HTTP API + client pattern"
status: accepted
date: 2026-08-29
tags: [harvest, pattern, daemon, api, sse]
source: agent-minder internal/daemon (server.go, v1.go, events.go, client.go)
related: "[[0004-daemon-interface-split]], [[0011-internal-pubsub-two-buses]], harvest/event-log-store-first.md"
---

# Harvest: daemon HTTP API — lift the pattern, not the code

## The architecture that worked

- Daemon owns the DB (single writer); CLI/TUI are **stateless API clients** — the
  client never opens the database, even on the same machine.
- Two generational surfaces in agent-minder: a legacy `/status`-style mux and a v1
  `/api/v1/...` mux behind middleware. For Trigger, build only the v1-style surface —
  narrow, deployment-scoped, explicit ([[0004-daemon-interface-split]]).

## v1 endpoint shape (reference)

```
GET  /api/v1/meta
GET  /api/v1/events                          (SSE, honors Last-Event-ID)
GET  /api/v1/deployments[/{id}]              full state + SnapshotMarker
GET  .../automations | /jobs | /jobs/{id}    scoped reads
GET  .../jobs/{id}/runs | /jobs/{id}/logs    run records, log tail
POST /stop | /resume                         the only writes
```

## Hardening worth rebuilding

1. **Snapshot-then-stream**: a deployment read returns complete state *plus*
   `SnapshotMarker{watermark, epoch}` in one transaction; the client then follows the
   event stream (SSE) from its cursor. Epoch mismatch ⇒ discard cursor and resync —
   never guess. (Per Expedition IV, snapshots also carry the worker incarnation so
   clients can refresh *live-only* state without discarding their durable cursor.)
2. **SSE resume via `Last-Event-ID`** maps directly onto the durable event log's
   cursor semantics (truncated/epoch-mismatch/cursor-ahead refusals from
   harvest/event-log-store-first.md). Reconnect = replay from the log, not "miss
   everything while disconnected".
3. **API key auth on every v1 request**: `X-API-Key` header, constant-time compare;
   log only a SHA-256 *identity* of the key, never the key.
4. Read endpoints are deployment-scoped all the way down (including joins) so one
   client can never read another tenant's rows.
5. `POST /stop`/`/resume` are the only mutations — external triggers enter through the
   same door as interfaces (ADR-0004 consequence).
6. The typed client (`daemon/client.go`) is the only consumer of the wire types;
   CLI commands (status/stop/checkout --remote) all go through it.

## Rewrite guidance for Trigger

Rebuild narrow: meta + snapshot + events + job/run reads + stop/resume, versioned from
day one. The wire models are `controlapi`'s job (DB models are explicitly *not* wire
models — mapping layer between Coordinator and JSON). Keep the snapshot-marker +
epoch contract identical to the event log, or the SSE resync story collapses.
