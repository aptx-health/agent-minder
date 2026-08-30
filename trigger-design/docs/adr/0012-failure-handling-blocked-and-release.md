---
title: "ADR 0012 — Failure handling: bounded retry, then park as blocked for reasoning"
status: accepted
date: 2026-08-29
tags: [architecture, failure, retry, blocked, state-machine, human-in-the-loop]
superseded_by:
---

## Context

A run can fail. The naive response — put it straight back on the queue — produces the worst
failure mode: a broken job retried forever, burning budget and drowning the event log, while
the real problem goes undiagnosed. Dustin's requirement: a failing job must **stop hammering
the queue**, surface for **higher-level reasoning (human or agent)**, and only re-enter the
flow once something has actually understood and addressed the cause.

This depends on the distinction already drawn in [[0008-workflows-deterministic-steps]]:
infrastructure failure (crash) is not the same as step-logic failure (the work failed).

## Decision

**Automatic retry is bounded and only for transient/infra failures. Everything else parks in a
`blocked` state that is never auto-retried and is released only by a human or an agent.**

1. **Infra failure → requeue, bounded.** A run interrupted by a crash/kill (no logic verdict)
   is requeued with backoff, up to a small attempt cap. This is safe because runs are
   idempotent-claimed and steps are crash-resumable.
2. **Step-logic failure → follow `on_failure`.** `stop` ends the run `failed` (terminal).
   `escalate` moves the run to `blocked`. A named-step jump reroutes within the workflow.
3. **Retry exhaustion → blocked.** When the infra-retry cap is hit, the run does not fail
   silently and does not loop — it moves to `blocked`.
4. **`blocked` is a parking state, not a terminal one.** It is **never auto-retried**. It
   carries the failure detail (why it parked) and waits. It exists precisely so higher-level
   reasoning can happen before the work runs again.
5. **Release is explicit, by human or agent.** A `blocked` run returns to `queued` only via an
   explicit release action, available to a human (CLI/TUI) and to an agent (MCP,
   [[0007-agent-controllable-mcp-server]]). Release may reset the attempt count and carries a
   note. An orchestrating agent may subscribe to `blocked` events, diagnose, fix the cause,
   and release — closing the loop without a human when appropriate.

The slot model ([[run-lifecycle-and-slots]]) bounds concurrency independently, so even
mis-tuned retry cannot stampede.

## Consequences

- A broken job costs a *bounded* amount before it parks — no infinite retry, no event-log
  flood.
- `blocked` is the single, inspectable place where attention (human or agent) is owed —
  attention routing applied to failure, consistent with pr-triage's thesis.
- The state machine gains one deliberate manual/agent edge (`blocked → queued`); every other
  transition is automatic and deterministic.
- Release being available over MCP means an agent can be the reasoning layer, not only a
  human — but the human path is always there.
- Requires the run record to store failure classification (infra vs. logic), attempt count,
  and failure detail — folded into [[run-lifecycle-and-slots]].
