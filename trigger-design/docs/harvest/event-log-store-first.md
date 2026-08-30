---
title: "Harvest: durable event log (store-first publish)"
status: accepted
date: 2026-08-29
tags: [harvest, events, durability, pubsub, transplant]
source: agent-minder internal/db (events.go, schema.go: events + event_log_meta)
related: "[[0011-internal-pubsub-two-buses]], [[0004-daemon-interface-split]]"
---

# Harvest: durable event log

**This code is the implementation of an adversarially-reviewed design** — Expedition IV,
the snapshot/event consistency contract ([[fable-expedition-crosswalk]] has the summary;
the full R-1–R-10 rules, identity model, client state machines, and rejected
alternatives live in `agent-minder/docs/research/fable-expedition/04-…md`). Read it
before changing any of the invariants below.

The core invariant (R-1): **commit is the publish**. An event row is appended in the
*same transaction* as the state change it describes — there is no separate publish
step that can fail, be skipped, or double-fire. Any TUI/GUI/live view replays from
the table; nothing else is authoritative. Companions: R-2 (publish to live channels
only *after* commit) and the identity trio — durable cursor (ID-1), log epoch (ID-2),
worker incarnation (ID-3, scopes live-only state like tool/step ticks, which are
ephemeral: never persisted, delivered id-less).

## Table shape (`events`)

```sql
id INTEGER PRIMARY KEY AUTOINCREMENT,   -- load-bearing (see below)
time DATETIME NOT NULL,
deployment_id TEXT NOT NULL,            -- replay scoping (R-9)
job_id INTEGER NOT NULL DEFAULT 0,      -- 0 = not attributable; NO FK constraints
run_id INTEGER NOT NULL DEFAULT 0,
type TEXT NOT NULL, severity TEXT NOT NULL, summary TEXT NOT NULL, data TEXT
```

Plus single-row `event_log_meta (id=1, epoch, truncated_through)`.

## The invariants that were each paid for

1. **AUTOINCREMENT is load-bearing** (ID-1/V-6): retention deletes rows; plain
   `INTEGER PRIMARY KEY` would release rowids back into circulation and silently
   corrupt every client holding an old cursor.
2. **Retention = deletion + explicit floor, never compaction** (R-7): `PruneEvents`
   deletes `id <= cutoff` and advances `truncated_through` in one tx. Surviving rows
   are never mutated.
3. **Watermark never regresses**: `MAX(max(events.id), truncated_through)` — without
   the floor term, watermark drops when retention deletes every row.
4. **Cursor validation is typed and atomic** (`ReadEventBatch`): floor/head/epoch read
   in one tx with the events; refusals are `ErrEventsTruncated` (cursor < floor),
   `ErrEventCursorAhead`, `ErrEventEpochMismatch`, `ErrEventEpochRequired` (nonzero
   cursor without epoch). The batch stays populated on refusal so transports can send
   a complete resync signal.
5. **Epoch rotates only when history is destroyed** (ID-2) — WAL recovery, schema
   reseed. Clients compare epoch + cursor; mismatch ⇒ discard cursor, resync.
6. **Snapshot consistency** (§6): snapshot state and its `SnapshotMarker{Watermark,
   LogEpoch}` are read in one tx, so the marker provably covers exactly the events
   ≤ watermark visible in that snapshot.
7. **Scoping filter on replay AND live delivery is identical** (R-4/R-9):
   `WHERE deployment_id = ? AND id > ?`. Cursors are host-global ids; deployment-local
   sequences have holes — only host-global head is authoritative for cursor-ahead.

## API surface worth lifting

`AppendEventTx` (in-caller's-tx) vs `AppendEvent` (own tx, for decision-only events);
`WithTx`; `EventWatermark`; `SnapshotMarker`; `SnapshotControl(deploymentID)` (one
deployment's whole durable state + marker in one tx); `EventsAfter`; `ReadEventBatch`;
`PruneEvents(maxRetained)` — default retention 10000, host-global (deliberate
simplification; revisit only on proven starvation).

## Transplant note

This is a *pattern with exact invariants*, not just a table. Lift code + tests
(`events_test.go` covers epoch rotation, truncation, cursor-ahead, scoping) and adapt
naming only. For Trigger, `deployment_id` maps to whatever the top-level scoping
entity becomes.
