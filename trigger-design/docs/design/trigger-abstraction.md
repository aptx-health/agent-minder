---
title: "Trigger abstraction (design)"
status: draft
date: 2026-08-29
tags: [design, triggers, daemon, architecture]
related: [[0005-trigger-source-agnostic]], [[0002-local-sqlite-source-of-truth]], [[0004-daemon-interface-split]], [[0008-workflows-deterministic-steps]]
---

# Trigger abstraction (design)

Elaborates [[0005-trigger-source-agnostic]]: how a trigger actually fires a workflow. The
whole tool turns on this concept, so it is specified carefully.

## The core model: one Fire event, one supervisor

Every trigger, however it originates, produces the **same `Fire` event** onto the **work bus**
([[0011-internal-pubsub-two-buses]]). A **single supervisor** consumes fires and starts workflow
runs. The supervisor does not know or care which kind of trigger produced a fire. That is the
abstraction: the currency is the `Fire`, not the source.

The work bus is **store-first**: publishing a fire means committing a `queued` run row to
SQLite (the commit is the publish); a live notification wakes the supervisor and a fallback tick
re-scans queued rows so nothing is lost. The `chan Fire` below is the live-notification layer
over that durable queue, not the source of truth.

```
 scheduled sources ─┐
   cron, github      ├─► chan Fire ─► Supervisor ─► (dedup) ─► workflow run (ADR 0008)
 endpoint triggers ─┘
   webhook, manual
```

## Two ingress families (pull vs push)

A trigger is physically one of two things; both emit the identical `Fire`.

- **Scheduled / pull source** — owns a goroutine and emits on its own schedule.
  - `cron` — a clock fires at the cron expression.
  - `github` — a poll loop checks for matching issues/events (ETag-cached, harvest
    agent-minder's `internal/github`).
- **Endpoint trigger** — arrives at the daemon's API surface ([[0004-daemon-interface-split]],
  [[0007-agent-controllable-mcp-server]]); no background loop of its own.
  - `webhook` — an inbound HTTP request on a configured path.
  - `manual` — an explicit CLI / API / MCP invoke.

This split is why ADR 0005 calls webhook/manual "callers, not new transports": they enter
through the one API door and emit a fire, rather than each running its own listener.

## Types (sketch)

```go
// Fire is the universal currency: one workflow run should start.
type Fire struct {
    JobName   string         // which job (config) to run
    Cause     Cause          // what fired it — becomes .trigger in templates (ADR 0010)
    DedupKey  string         // idempotency key; empty = always run (e.g. manual)
    FiredAt   time.Time
}

// Cause carries source-kind + structured data for templating and the run record.
type Cause struct {
    Kind string                 // cron | github | webhook | manual
    Data map[string]any         // e.g. {"issue": {"number": 634, "labels": [...]}}
}

// Source is a scheduled/pull trigger. It runs until ctx is cancelled, emitting fires.
type Source interface {
    Kind() string
    Start(ctx context.Context, out chan<- Fire) error
}
```

Endpoint triggers are not `Source`s — the API/MCP handler builds a `Fire` and sends it on
the same channel. One channel, two producers-of-origin.

## Per-kind adapters

| Kind | Ingress | Cause data | Dedup key |
| --- | --- | --- | --- |
| `cron` | scheduled goroutine | scheduled time | `job + scheduled-slot` (one run per slot) |
| `github` | poll loop (ETag) | issue/PR/event payload | `job + event-id` (e.g. issue number + action) |
| `webhook` | HTTP path on the API | request body/headers | caller delivery-id, else body hash |
| `manual` | CLI / API / MCP call | caller-supplied args | empty (always runs) unless caller sets one |

## Idempotency — dedup before run

The supervisor checks local state ([[0002-local-sqlite-source-of-truth]]) for `DedupKey` before
starting a run. If a run for that key already exists (running or completed within a window),
the fire is dropped. This makes the two most common bugs impossible:

- a `github` poll firing the same issue twice across poll cycles;
- a retried `webhook` delivery double-running.

Harvest agent-minder's dedup concept (recent_run / branch_exists) but key it off the durable
run record, not GitHub artifacts.

## Lifecycle

1. **At daemon start** (and on config reload): read config jobs. For each job whose trigger
   is a scheduled kind, instantiate its `Source` and `Start` a goroutine. For each endpoint
   kind, register its API route (webhook path) or make it invocable (manual).
2. **Config reload reconciles**, one-directionally from config: stop sources for removed
   jobs, start sources for added ones, leave unchanged ones running. Single-writer daemon,
   so no race ([[0004-daemon-interface-split]]).
3. **Shutdown** cancels the context; sources stop; in-flight runs are recorded as
   interrupted and resume on next start (crash-resume, ADR 0008).

## Skip-missed policy (scheduled sources)

If the daemon was down when a `cron` slot elapsed, the missed slot is **skipped**, not
replayed — fire forward only. This prevents a stampede when the foreground daemon is
Ctrl-C'd and restarted (a frequent workflow). One-shot `at:`/`in:` schedules that already
fired are marked done and never refire (harvest agent-minder's one-shot disable). Catch-up
is not a v1 feature; if a job needs it later, it is an explicit per-job opt-in.

## From Fire to workflow run

1. Supervisor receives `Fire`, checks dedup.
2. Creates a run record (job + resolved config via [[config-resolve-once]]); stores `Cause`
   for the `.trigger` template context.
3. Executes the ordered steps (agent or script) per [[0008-workflows-deterministic-steps]],
   with crash-resume and `on_failure` routing.
4. Emits events to the **event bus** (durable event log) for the TUI/MCP/sinks to observe —
   best-effort fan-out, never in the control path ([[0011-internal-pubsub-two-buses]]).

## Open questions

- **github ingress: poll vs. webhook.** v1 polls (simple, no public endpoint). A GitHub
  webhook is a later `webhook`-kind binding. Confirm poll interval defaults.
- **Backpressure / concurrency limits.** A max-concurrent-runs cap (global and per-job) —
  harvest agent-minder's slot model. Where the cap lives (supervisor) needs specifying.
- **Webhook auth/transport** — see R2/R6; the endpoint binding depends on it.
- Whether `manual` dedup should ever be non-empty (e.g. an operator supplying an idempotency
  token to make a retry safe).
