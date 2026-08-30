---
title: "ADR 0014 — Answer authority: human or charter-bounded orchestrator"
status: accepted
date: 2026-08-29
tags: [architecture, authority, autonomy, human-in-the-loop, charter, mcp]
superseded_by:
---

## Context

A parked run — `awaiting_input` (a question or scope request, [[0013-ask-and-resume-instead-of-bail]])
or `blocked` (a failure, [[0012-failure-handling-blocked-and-release]]) — must be answered or
released. **Who** is allowed to answer depends on how much autonomy the operator has granted.
Dustin described two working modes:

- **"You have authority, don't bother me."** An external **orchestrator** (his PM agent over
  MCP — see [[glossary]]) decides autonomously, and only comes to the human for things outside
  the agreed bounds.
- **Passenger seat.** The human is watching; the orchestrator proposes and they discuss; the
  human stays in the decision.

The agreed bounds are the **charter** laid out at the start of the work
([[0009-cross-tool-boundary-shared-conventions]]). The orchestrator's authority must not exceed
it.

## Decision

**A parked run is answered by a human or an orchestrator, governed by a per-deployment /
per-job authority mode, and the charter bounds what an orchestrator may decide autonomously.**

1. **Authority modes:**
   - `human_only` — only the human answers/grants.
   - `orchestrator` — the orchestrator may answer questions and grant scope **autonomously,
     within the charter**; the human is not interrupted except for escalations.
   - `interactive` — passenger seat: the orchestrator *proposes*; the human confirms. Questions
     are surfaced for discussion rather than auto-answered.
2. **The charter is the authority envelope.** In `orchestrator` mode, a decision or scope grant
   is permitted only if it stays within the charter. **A request beyond the charter always
   escalates to the human**, regardless of mode — the orchestrator cannot self-expand its own
   envelope.
3. **The human can always intervene.** Human authority is available in every mode; the operator
   may answer, override, or re-scope at any time.
4. **Answering is authenticated and authorized, fail-closed.** Trigger must know who is
   answering (the human, or which orchestrator identity over MCP) and verify their authority for
   that run before applying it. An unverified or over-scope answer is refused, not applied
   ([[0006-secrets-and-agent-permissions]]).
5. **Every decision is recorded.** The run stores who answered, under which mode, whether it was
   within or beyond charter, and the note — a durable audit trail (store-first, evidence over
   conclusions).

## Consequences

- Both working styles are first-class: fully autonomous within bounds, or co-pilot discussion.
- Autonomous mode stays safe because the charter is a hard envelope the orchestrator cannot
  exceed — beyond-charter escalates to the human by construction.
- The orchestrator can be the reasoning layer without the human in the loop, yet the human is
  never locked out.
- Requires MCP identity/authorization for orchestrators (folds into R6 and
  [[0006-secrets-and-agent-permissions]]).
- Requires the charter to be a machine-readable artifact Trigger can check a request against —
  a concrete tie to the shared charter convention with pr-triage
  ([[0009-cross-tool-boundary-shared-conventions]]). How strictly "within charter" is evaluated
  (deterministic rules vs. an LLM judgment) is an open design question.
