---
title: "Event and observability model (design)"
status: draft
date: 2026-08-29
tags: [design, events, observability, cursor, sse, tui]
related: [[0011-internal-pubsub-two-buses]], [[daemon-api]], [[sqliteutil-wal-recovery]], [[0002-local-sqlite-source-of-truth]]
---

# Event and observability model (design)

The event bus made concrete: the event record, the cursor, retention, and the taxonomy that
feeds the TUI, the MCP stream, and external sinks. Elaborates [[0011-internal-pubsub-two-buses]]
(store-first, commit-is-publish, best-effort fan-out).

## Event record

Harvest agent-minder's `events` table shape:

`id` (autoincrement — the wire cursor), `epoch`, `time`, `run_id`, `job`, `step`, `type`,
`severity` (info | warn | error), `summary`, `data` (JSON).

- **Store-first:** an event is committed **in the same transaction** as the state change it
  describes ([[0011-internal-pubsub-two-buses]]). The commit is the publish; live fan-out
  happens after commit. If it is not committed, it did not happen and no one sees it.
- **`data`** carries the structured detail (which secrets are never present — [[0006-secrets-and-agent-permissions]]).

## The cursor is `(epoch, id)` — not just `id`

`id` is a monotonic autoincrement, the single wire cursor. But WAL-recovery can **truncate
committed history** ([[sqliteutil-wal-recovery]]); when that happens the log rotates `epoch`.
A client's cursor is therefore `(epoch, id)`:

- On reconnect with `?since={epoch}:{id}` ([[daemon-api]] SSE), the daemon replays forward.
- **If the client's `epoch` is older than the current epoch, its cursor is void** — the daemon
  returns a resync signal; the client discards its cursor and re-reads current state, then
  resumes live. This prevents the silent-missed-events bug the harvest note warns about.
- A retention floor (`truncated_through`) means a `since` below it cannot be served — replay
  returns a truncated error and the client resyncs from current state.

This is the concrete reason the event log needs `epoch` and a meta row — harvest agent-minder's
`event_log_meta` (epoch, truncated_through).

## Event taxonomy (v1)

Keyed to the run state machine ([[run-lifecycle-and-slots]]) and the trigger/daemon lifecycle.
`type` values, grouped:

| Group | Types |
| --- | --- |
| Daemon | `daemon.started`, `daemon.stopping`, `config.reloaded` |
| Trigger | `fire.received` (a source/endpoint produced a Fire), `fire.deduped` (dropped) |
| Run | `run.queued`, `run.claimed`, `run.succeeded`, `run.failed`, `run.cancelled` |
| Step | `step.started`, `step.succeeded`, `step.failed`, `step.retrying` |
| Parking | `run.blocked`, `run.awaiting_input`, `run.answered`, `run.released` |
| Authority | `authority.escalated` (beyond-charter → human), `authority.granted` |
| Contract | `contract.checkpoint_registered`, `ratified_contract_changed` (drift → escalate, ADR 0018) |
| Station | `station.claimed`, `station.returned` (ADR 0021 claim/return) |

`severity` drives attention: `error` for failures/blocked, `warn` for retries/escalations,
`info` for normal progress. The TUI and sinks filter on it.

## Subscribers

Per [[0011-internal-pubsub-two-buses]], all downstream and best-effort:

- **TUI** — SSE stream ([[daemon-api]]); renders live run/step progress and highlights the
  parking family. This is the observability surface Dustin cares about; the `(epoch, id)` cursor
  makes it crash-safe.
- **MCP stream** — the orchestrator's `subscribe_events`; the same feed, so an agent watches
  what a human watches ([[0007-agent-controllable-mcp-server]]).
- **External event sinks** — harvest agent-minder's `internal/eventsink`: webhook/exec delivery
  of matching events, bounded per-sink queue + timeout, best-effort. A slow sink never blocks
  the committer.

## What observability must never do

- Never carry control flow — a missed event must not change what runs
  ([[0011-internal-pubsub-two-buses]] guard).
- Never expose secrets in `summary`/`data` ([[0006-secrets-and-agent-permissions]]).
- Never let a subscriber's backpressure block the commit — sinks have bounded queues and drop
  or lag, they do not stall the supervisor.

## Two refinements from the TUI design

- **Firehose in v1.** The event stream is a firehose; clients filter locally (the TUI's `/`
  filter). Server-side `?type=`/`?run=` filters on the SSE endpoint are a later addition if a
  busy daemon outruns client-side filtering — not v1 ([[tui-mockups]] §6.1, [[daemon-api]]).
- **`run.answered` always emits, even when auto-answered.** In `orchestrator` authority mode the
  human is not interrupted, but the decision must still be visible: `run.answered` (with who,
  mode, `within_charter`) flows through the stream so a passenger-seat operator sees what was
  decided ([[0014-answer-authority]], [[tui-mockups]] §6.3).

## Open questions

- Event retention policy (age/size cap) and when `truncated_through` advances.
- `data` schema per event type — start loose (JSON), tighten as the TUI consumes specific
  fields.
