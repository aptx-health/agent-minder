---
title: "ADR 0003 — ACP-first runtime seam"
status: accepted
date: 2026-08-29
tags: [runtime, acp, agents, portability]
superseded_by:
---

## Context

agent-minder maintains hand-rolled runtime adapters (claudecode, codex, opencode), each a
separate integration to keep working as the CLIs change. The Agent Client Protocol (ACP)
is emerging as the standard interface between a controller and a coding agent; OpenHands,
Zed, and others speak it. Standardizing on ACP shrinks Trigger's maintenance surface to
the part that is actually its own — the scheduler, triggers, and control plane — and lets
the runtime adapters be someone else's standard.

## Decision

Trigger reaches agent runtimes **through ACP as the primary seam.** A runtime is selected
by config (`runtime`/`model` per job); the daemon speaks ACP to it. opencode and Claude
Code are the first two targets. To get moving, **copy pr-triage's proven opencode (and
Claude) runtime code as the starting adapter**, then migrate toward a clean ACP client as
the seam solidifies.

Model rules follow pr-triage's opencode discipline: `provider/model` form; provider auth
lives in the runtime's own config (e.g. `opencode auth`), not Trigger's env; an empty
model lets the agent's own default stand.

## Consequences

- Adding a runtime becomes "it speaks ACP," not "write a new adapter."
- Early velocity: the copied pr-triage opencode adapter is known-good, so dogfooding can
  start before the ACP client is fully general.
- Open risk: the maturity and Go support of ACP is unverified. Whether a usable Go ACP
  client exists or must be written is a **research task** — see [[open-questions]]. If ACP
  is not ready, fall back to thin per-runtime adapters behind the same internal interface,
  and revisit; this ADR would then be superseded.
- The seam must be an internal interface Trigger owns, so ACP-vs-adapter is an
  implementation detail the scheduler never sees.
