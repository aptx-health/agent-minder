---
title: "Harvest: config loader pattern (jobs.yaml)"
status: accepted
date: 2026-08-29
tags: [harvest, pattern, config, scheduler]
source: agent-minder internal/scheduler/config.go
related: "[[0005-trigger-source-agnostic]], [[config-resolve-once]], harvest/script-execution-config.md"
---

# Harvest: config loader — lift the pattern, not the code

agent-minder's `jobs.yaml` is the right *shape*: one YAML file declaring named jobs,
each of which is exactly one of **cron** (`schedule:`), **one-shot** (`at:`/`in:`), or
**trigger** (`trigger_expr`), and exactly one execution kind (**agent** or **script**).
Trigger redesigns this around the trigger abstraction ([[0005-trigger-source-agnostic]])
— don't copy the file format.

## What the shape gets right

- **A job def is exactly one of cron / one-shot / trigger** — validated, not inferred.
  `IsScheduled()` / `IsOneShot()` / `IsScheduledOrOneShot()` make the classification
  explicit.
- **Execution config is exclusive by validation**: `kind: script` requires `command`
  and rejects agent fields (`runtime`/`model`/`budget`/`max_turns`); vice versa. Mixed
  declarations are config errors, not silent precedence.
- **Sync reconciles declared config → persisted rows** (`SyncSchedules`): upsert each
  job, **disable (never delete) schedules no longer declared** — removal must not
  orphan history or `last_run_at`. In-place conversion (cron→at→trigger or agent→script)
  replaces the execution config wholesale; stale fields are cleared.
- **One-shots are marked fired, not deleted** — `enabled=false` + `last_run_at` reuses
  existing columns. Critical because an `in: 2h` duration re-resolves on every daemon
  restart; persistence is what makes "never refire" true.
- **Triggers are declared but not persisted** — they're watch-mode's job. The config
  loader deliberately doesn't fake them into schedules.

## Rewrite guidance for Trigger

Keep: declarative named jobs; mutually exclusive trigger kinds validated at parse
time; sync-with-disable reconciliation; one-shot fire-once persistence.
Replace: the cron/trigger/at field split with the trigger abstraction from
[[0005-trigger-source-agnostic]] and [[design/trigger-abstraction]]; schema from
[[design/config-schema]]. The subtle bugs (refiring one-shots, stale execution fields
after conversion, zombie schedules after removal) are all *reconciliation* bugs —
design the sync step first, the YAML second.
