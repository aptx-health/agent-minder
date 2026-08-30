---
title: "ADR 0006 — Secrets and agent permissions"
status: accepted
date: 2026-08-29
tags: [security, secrets, permissions, sandboxing, macos]
superseded_by:
---

## Context

Trigger runs multiple agents against multiple services and trigger sources. Two distinct
safety problems fall out, and they are NOT the same problem:

1. **Secret custody** — Trigger must hold and supply credentials for many services and
   trigger sources (GitHub tokens, webhook signing secrets, provider API keys where the
   runtime does not own them). Leaking or mishandling these is Trigger's own risk. This is
   squarely Trigger's responsibility.

2. **Agent containment** — preventing an agent from "running amok": writing outside its
   worktree, reaching the network it should not, or taking destructive actions. Much of
   this belongs to the underlying runtime/harness (Claude Code / opencode permission modes,
   sandboxing), not Trigger. Trigger's role is to decide and pass *policy* — what an agent
   is allowed to do for a given job — and to choose safe defaults, not to reimplement a
   sandbox.

The mechanism direction is now decided (below). Remaining detail work — confirming the
exact library and mapping each runtime's permission model — is a research task, see
[[open-questions]] R5.

## Decision

**Secret custody is Trigger's job; agent containment is the runtime's job.**

- **Secrets — OS keychain, macOS-only for v1.** Store credentials in the **macOS Keychain**
  via `go-keyring` (harvest agent-minder's `internal/auth` — it already uses go-keyring),
  never in plaintext config. Scoped per service and per trigger source. A job references a
  secret **by name**; the daemon resolves it at run time. Secrets are **daemon-side only** —
  no interface ever receives them, consistent with [[0004-daemon-interface-split]]. v1
  targets **macOS only** (Dustin and coworkers are all on Mac); multi-OS/arch keychain
  backends are explicitly out until a real need appears — extract-on-force.
- **Containment — delegated, not built.** Trigger owns *policy* only: least-privilege per
  job (which tools/paths/network a step may use), passed to the runtime, with safe
  fail-closed defaults (an unspecified permission denies). Trigger **does not implement a
  sandbox** — enforcement is the runtime/harness's responsibility (Claude Code / opencode
  permission modes) plus worktree isolation. Trigger must not pretend to contain what it
  cannot.

## Consequences

- Clear trust boundary: Trigger guards secrets and *declares* policy; the runtime *enforces*
  containment. No duplicated, half-built sandbox.
- Least-privilege-per-job means the config schema (R3) must carry a per-step
  secrets/permissions block ([[0008-workflows-deterministic-steps]]) — fold in early.
- macOS-only is a deliberate v1 narrowing, not an oversight; revisit only when a non-Mac
  user is real. See the platform note in [[charter]].
- R5 now narrows to detail: confirm the go-keyring choice (vs. alternatives) and map what
  each runtime's permission mode actually enforces, so Trigger knows exactly what it can
  delegate.
