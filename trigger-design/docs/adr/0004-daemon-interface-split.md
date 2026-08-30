---
title: "ADR 0004 — Daemon / interface split"
status: accepted
date: 2026-08-29
tags: [architecture, daemon, tui, api, interface]
---

## Context

Dustin wants one daemon he can hit with a TUI now and a GUI later, without the interface
becoming a second brain. agent-minder already learned that two independent poll loops go
out of sync; pr-triage's app-github-boundary work commits to a server (state authority +
reconciler + local API) with thin interfaces that read the API.

## Decision

**The daemon is the only authority.** It owns the job store, the scheduler tick, trigger
handling, runtime invocation, and all reconciliation. It exposes a **local API** (HTTP
over localhost, or a local socket). **Every interface — TUI now, GUI later, CLI — reads
and writes only through that API, never the database directly.**

No interface makes its own external calls or holds its own state. The daemon is the single
writer to SQLite; interfaces are stateless views over the API.

## Consequences

- New interfaces are cheap: a GUI, a web view, or a remote client are all just API
  clients. This is where a future "watch red turn green" surface would live.
- One writer, one poll loop, one reconciler — no split-brain, consistent with the
  single-source-of-truth invariant ([[0002-local-sqlite-source-of-truth]]).
- The API is a real contract that must be versioned as interfaces multiply. Keep it
  narrow and explicit.
- External spin-up (starting the daemon or a job from an outside trigger) enters through
  the same API surface, so the trigger path and the interface path share one door.
