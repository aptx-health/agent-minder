---
title: "Config resolve-once"
status: accepted
date: 2026-08-29
tags: [config, architecture, correctness]
source: pr-triage (docs/config-resolve-once.md), originally agent-minder
---

# Config resolve-once

Build a single ranked config resolver — **default → job → step**, most-specific wins —
resolve it **once per run**, and store the resolved values on the run record. Never
re-derive configuration in a display path or at read time.

agent-minder's worst config bug (a per-agent model silently dropped) came from not having
this: without one resolve-once point, different code paths re-derived different answers for
"what model is this run using." pr-triage adopted resolve-once to close that class of bug;
Trigger inherits it.

## Rules

1. **One resolver.** Exactly one function computes the effective config for a step run,
   ranking `default → job → step`.
2. **Resolve once, at run start.** Compute the effective `{runtime, model, agent, timeout,
   secrets, permissions, …}` when the step run begins.
3. **Store the resolved values on the run record.** The `agent_runs`-style row records what
   was actually used. Logs, the TUI, and MCP read the stored values.
4. **Never re-derive at read/display time.** No display path recomputes config. If it is not
   on the run record, it did not happen.
5. **No display-only config.** Effective config is what got resolved and stored. Avoid
   top-level fields that look effective but are not (the ambiguity resolve-once removes).

## Explicit-empty semantics

An empty `model` at a level means "inherit / let the runtime or agent choose its own
default," not "set model to empty string." A set value overrides; an empty value defers.
This mirrors pr-triage's opencode adapter (omit `-m` when the model is empty). Empty means the
**runtime** default, never a specific vendor's default (Expedition V, [[fable-expedition-crosswalk]]).

**Resolved ≠ observed for `model`.** The *requested* model comes from resolve-once and is
stored as `model_requested`. The *resolved* model (`model_resolved`) is what the runtime
actually reports it ran — it is **never written from config** and may be NULL if unconfirmed.
Warn when they disagree. This is the one field where storing the resolved config value would
lie ([[db-schema]]).
