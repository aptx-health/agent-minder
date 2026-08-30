---
title: "Glossary — Trigger terminology"
status: accepted
date: 2026-08-29
tags: [glossary, terminology, reference]
---

# Glossary

Level-set on terms so the docs and code stay consistent. The most important distinction:
**supervisor** (internal, mechanical) vs. **orchestrator** (external, an agent acting for the
human).

| Term | Meaning |
| --- | --- |
| **Trigger** | The tool itself; also the abstraction ([[0005-trigger-source-agnostic]]) — a source that produces a fire. |
| **Trigger source** | A concrete trigger implementation: `cron`, `github`, `webhook`, `manual`. Scheduled sources own a goroutine; endpoint triggers arrive at the API. |
| **Fire** | The universal event "one workflow run should start." Published onto the work bus. |
| **Work bus** | Store-first durable queue of pending runs; publish = commit a `queued` row. The **supervisor** subscribes ([[0011-internal-pubsub-two-buses]]). |
| **Event bus** | Store-first durable event log; best-effort live fan-out to observers (TUI, MCP, sinks). Never in the control path. |
| **Supervisor** | **The internal, mechanical engine inside Trigger.** Claims fires within slot limits, drives the run state machine, dispatches steps, handles retry/blocked/awaiting_input. This is the piece earlier loosely called "runner" or "orchestrator (internal)"; the settled name is **supervisor**. It makes no LLM decisions — it is deterministic ([[0008-workflows-deterministic-steps]]). |
| **Runtime** | The agent process behind the ACP seam ([[0003-acp-runtime-seam]]) — Claude Code, opencode, etc. A `runtime` field selects it per step. |
| **Orchestrator** | **An EXTERNAL interactive agent (cloud Claude / Codex / opencode) acting as the human's project manager**, consuming Trigger's MCP endpoints ([[0007-agent-controllable-mcp-server]]). It fires jobs, watches events, and answers `awaiting_input`/`blocked` runs under a configured authority ([[0014-answer-authority]]). NOT the internal supervisor. |
| **Job** | A named unit in config: exactly one trigger + an ordered list of steps. |
| **Step** | One ordered unit of a job: a deterministic `script` or an `agent` invocation ([[0008-workflows-deterministic-steps]]). |
| **Run** | One execution instance of a job, with its own durable record and state. |
| **Charter** | The behavioral contract/spec for a piece of work, authored up front (shared convention with pr-triage, [[0009-cross-tool-boundary-shared-conventions]]). Also **bounds an orchestrator's autonomous authority** ([[0014-answer-authority]]). |
| **Parking family** | The two suspend states: `blocked` (reactive failure) and `awaiting_input` (proactive ask). Same durable payload and release path ([[0012-failure-handling-blocked-and-release]], [[0013-ask-and-resume-instead-of-bail]]). |
| **Human** | The operator (Dustin). Answers/releases parked runs via CLI/TUI; sets the authority mode. |
