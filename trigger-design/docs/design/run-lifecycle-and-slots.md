---
title: "Run lifecycle and slot model (design)"
status: draft
date: 2026-08-29
tags: [design, state-machine, slots, concurrency, queue, failure]
related: [[0011-internal-pubsub-two-buses]], [[0012-failure-handling-blocked-and-release]], [[0008-workflows-deterministic-steps]], [[0002-local-sqlite-source-of-truth]]
---

# Run lifecycle and slot model (design)

How a run moves through states, how the supervisor claims work within slot limits, and how
failures park and release. This is the state machine that rides the work bus
([[0011-internal-pubsub-two-buses]]). Kept in SQLite behind the `WorkQueue` backend interface
— low throughput (dozens of concurrent runs), so SQLite is ample; the interface leaves room
for another backend later.

## States

| State | Meaning | Auto-retried? |
| --- | --- | --- |
| `queued` | waiting for a slot; has an optional `next_attempt_at` | — |
| `running` | claimed by the supervisor; has an owner + heartbeat | — |
| `succeeded` | workflow completed | no (terminal) |
| `failed` | step-logic failure with `on_failure: stop` | no (terminal) |
| `blocked` | parked after a *failure*; needs diagnosis ([[0012-failure-handling-blocked-and-release]]) | **no** |
| `awaiting_input` | agent *proactively paused* to ask a question or request scope ([[0013-ask-and-resume-instead-of-bail]]) | **no** |

`blocked` and `awaiting_input` are the **parking family** — same durable request/answer
payload, same release path, same bounded wait — differing only in why the run parked
(reactive failure vs. proactive ask). Kept as distinct status values so a human can tell them
apart at a glance.

## Transitions

```
                 claim (slot free)         success
   queued ───────────────────────► running ─────────► succeeded
     ▲  ▲                             │
     │  │      infra crash/kill       │  on_failure: stop
     │  └──── (attempts < cap) ───────┤ ─────────────► failed
     │        requeue w/ backoff      │
     │                                │  on_failure: escalate
     │                                │  OR attempts >= cap
     │                                └─────────────► blocked
     │
     └───────── release (human/agent) ──────────────── blocked
```

- **queued → running:** atomic claim (below), only if a global slot and the job's per-job slot
  are free.
- **running → succeeded:** all steps done.
- **running → failed:** a step failed with `on_failure: stop` (terminal).
- **running → blocked:** a step failed with `on_failure: escalate`, or the infra-retry cap was
  reached (ADR 0012).
- **running → queued:** infra failure (crash/kill, no logic verdict) with `attempts < cap`;
  requeued with backoff via `next_attempt_at`.
- **blocked → queued:** explicit release by a human (CLI/TUI) or agent (MCP); attempt count
  reset; carries a note.
- **running → awaiting_input:** the agent emitted a structured `needs_input` result (question
  or scope request) instead of bailing ([[0013-ask-and-resume-instead-of-bail]]); the run
  suspends with its resume cursor stored.
- **awaiting_input → running/queued:** an answer arrives (human CLI/TUI or agent via MCP
  elicitation); it is schema-validated, injected as data, and the runtime session resumes in
  place. A scope grant updates the permission record first.
- **awaiting_input → blocked:** the input timeout expired with no answer (configurable default).

Step-internal progress (which step, resume point) lives on the run per
[[0008-workflows-deterministic-steps]] — crash-resume picks up the interrupted step; the
transitions above are the run-level machine.

## Atomic claim

Claiming must be race-free. Single-writer SQLite ([[0002-local-sqlite-source-of-truth]]) plus a
conditional update gives it for free:

```sql
UPDATE runs SET status='running', owner=?, claimed_at=?, heartbeat_at=?
WHERE id = (
  SELECT id FROM runs
  WHERE status='queued'
    AND (next_attempt_at IS NULL OR next_attempt_at <= :now)
    AND job_name NOT IN (SELECT job_name FROM runs WHERE status='running' GROUP BY job_name
                         HAVING COUNT(*) >= :per_job_cap)
  ORDER BY queued_at
  LIMIT 1
)
RETURNING *;
```

One writer, one claim — no double-pick. The `WorkQueue` interface exposes this as
`Claim(ctx, limits) (Run, ok)`.

## Slot model

- **Global cap:** max concurrent `running` runs across the daemon (harvest agent-minder's
  `max_agents`). The supervisor claims only while `running < global_cap`.
- **Per-job cap:** default **1** (a job does not run twice at once); overridable in config.
  Encoded in the claim query above.
- **Fairness:** claim in `queued_at` order; a later refinement can weight by job. Not v1.
- The supervisor is a loop: on a work-bus notification or the fallback tick, claim up to available
  slots, start each run, repeat.

## Liveness and crash reconcile

- A `running` row carries `owner` (daemon instance id) and `heartbeat_at`, updated as steps
  progress (harvest agent-minder's heartbeat + `last_activity_at`).
- **On daemon start**, reconcile: any `running` row with a stale heartbeat and no live owner is
  treated as an infra failure → requeue (attempts++) or resume the interrupted step. This is
  the crash-resume path; it never routes through `on_failure` (that is for logic failures).

## Blocked: inspect and release

`blocked` is the one place attention is owed. The interface (CLI, TUI, API, MCP) must:

- **List** blocked runs with `failure_detail`, failing step, attempt count, and cause.
- **Release** a run: `blocked → queued`, reset attempts, attach a note (who/what released and
  why). Idempotent.
- Emit `blocked` and `released` events on the event bus so a subscribed agent or a human view
  is notified the moment a run parks.

## Run record fields (additions implied)

`status`, `owner`, `queued_at`, `claimed_at`, `heartbeat_at`, `next_attempt_at`, `attempts`,
`failure_class` (infra | logic), `failure_detail`, the parking payload `request_json` /
`answer_json` / `input_timeout` (shared by `blocked` and `awaiting_input`, ADR 0013), plus the
step/resume fields (`session_id`, step cursor) from ADR 0008.

## Open questions

- Backoff curve and the infra-retry cap default (small — 2–3).
- Whether per-job cap > 1 is ever wanted in v1, or strictly serial-per-job.
- Global cap default (harvest agent-minder's default) and whether it is per-runtime as well.
