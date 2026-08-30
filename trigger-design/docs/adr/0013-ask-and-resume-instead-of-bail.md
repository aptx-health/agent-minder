---
title: "ADR 0013 — Ask-and-resume: agents pause for clarification or scope instead of bailing"
status: accepted
date: 2026-08-29
tags: [architecture, agents, human-in-the-loop, mcp, acp, failure, resume]
superseded_by:
---

## Context

agent-minder's hard **bail** was expensive: an agent that hit missing information or a
scope wall either gave up (throwing away all its work and burning the tokens spent) or
guessed and produced the wrong thing. Often a single clarifying question would have saved
the run. The better behavior: the agent **pauses**, asks a question or requests expanded
scope, and **resumes in place** once answered — no restart, no blind guess.

Prior-art research (2026) established the mechanics and one hard constraint:

- **No protocol Trigger speaks natively supports full ask-and-resume.**
  - **MCP elicitation** (upward, to orchestrators) *is* the native fit: a JSON-Schema request
    for input mid-operation, with accept/decline/cancel outcomes.
  - **ACP** (downward, to runtimes) has only a yes/no permission gate
    (`session/request_permission`) — no free-form mid-turn question. An agent that asks in ACP
    just ends its turn.
- The strongest implementations (OpenAI Agents SDK, Temporal) model the pause as **persisted
  run state resumed in place**, which maps onto Trigger's `blocked` parking
  ([[0012-failure-handling-blocked-and-release]]) plus crash-resumable steps
  ([[0008-workflows-deterministic-steps]]).

## Decision

**A run may pause to ask, not only to fail. Trigger models "asking" as a parking state that
suspends and resumes in place — never a bail.**

1. **New parking state `awaiting_input`.** A sibling of `blocked`, sharing the same durable
   request/answer payload and release path. `blocked` = something failed (reactive);
   `awaiting_input` = the agent proactively paused (a question or a scope request). Both are
   distinct status values so a human can tell "needs diagnosis" from "has a question."
2. **Downward signal, not a bail.** Because ACP cannot carry a mid-turn question, the agent
   emits a **structured `needs_input` result** as its turn output — a typed payload (a
   question with a JSON Schema, or a scope request). Trigger detects it (harvest
   agent-minder's structured-output/bail detection in `agentutil`, as a *distinct* signal) and
   parks the run instead of terminating it.
3. **Resume in place, not restart.** Trigger stores the resume cursor (`session_id` + step),
   and on answer **continues the same runtime session** with the answer injected — not a fresh
   run. Pre-pause work is preserved (idempotent step boundary at the question).
4. **Upward surfacing over MCP elicitation.** Trigger-as-MCP-server surfaces an
   `awaiting_input` run to an orchestrator via **MCP elicitation**, the native channel; a human
   answers via CLI/TUI. Both write `answer_json` and resume — the same mechanism as `blocked`
   release ([[0012-failure-handling-blocked-and-release]], [[0007-agent-controllable-mcp-server]]).
   **Who** may answer (human, or a charter-bounded orchestrator) is governed by
   [[0014-answer-authority]] — a beyond-charter request always escalates to the human.
5. **Scope requests mutate policy, then resume.** A scope/permission request is a **typed**
   request; granting it updates the run's permission record ([[0006-secrets-and-agent-permissions]])
   for the approved continuation before resuming. Not free text, and scoped to this run.
6. **Every wait is bounded.** An `awaiting_input` run carries a durable timeout; on expiry it
   takes a configured default (fall to `blocked`, or a declared fallback) — a question never
   hangs forever.
7. **The answer channel is untrusted.** The injected answer is validated against the declared
   JSON Schema and treated as **data, not instructions** (prompt-injection guard). Questions per
   run are capped to prevent over-asking.

Behavior is contract-driven: the agent definition / house style instructs the agent to **ask
instead of bail or guess** when blocked on missing information or scope. Trigger provides the
channel; the contract tells the agent to use it.

## Consequences

- The expensive bail is replaced by a cheap early clarification — the core token-waste fix.
- Ambiguity gets attention-routed the same way failure does: `awaiting_input` is an
  inspectable place where a small human/agent input unblocks the run.
- Reuses the parking/release machinery — one release path serves both `blocked` and
  `awaiting_input`.
- An orchestrating agent can answer questions natively over MCP elicitation, so the reasoning
  layer can be an agent, not only a human (the human path always remains).
- ACP's missing question channel is handled by session-resume, so no protocol change is
  needed; if ACP later adds elicitation, it slots under the same signal.
- New run fields: `request_json` (schema + question or typed scope request), `answer_json`,
  `input_timeout`, and the reuse of the resume cursor (`session_id` + step).
- Risk: over-asking degrades autonomy. Mitigated by the per-run question cap and by contract
  guidance to ask only when a guess would be materially wrong.
