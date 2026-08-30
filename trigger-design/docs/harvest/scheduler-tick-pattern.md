---
title: "Harvest: scheduler tick pattern"
status: accepted
date: 2026-08-29
tags: [harvest, pattern, scheduler, jobs]
source: agent-minder internal/scheduler/scheduler.go
related: "[[0005-trigger-source-agnostic]]"
---

# Harvest: scheduler tick — lift the pattern, not the code

The loop is trivial and sound: initial tick, then `time.Ticker(interval)` (30s),
exit on ctx cancel. Reimplement against Trigger's job store.

## The pattern

```
tick():
  schedules = load enabled schedules for this deployment
  now = time.Now().UTC()
  for each schedule:
    due? (cron: next_run_at <= now | one-shot: at_time <= now)
    skip if a job from this source is already active   ← the load-bearing line
    create queued job row (provenance: source_type/source_name/source_ref)
    cron → advance next_run_at = NextAfter(now)
    one-shot → mark fired (disable)
```

## Hardening that must survive the rewrite

1. **Idempotence via provenance, not job name**: `jobAlreadyActive` counts jobs by
   `source_type + source_name` in active statuses (queued/running/blocked/reviewing).
   Job names are timestamped (`<schedule>-YYYYMMDD-HHMM`), so name-based checks would
   double-fire on clock drift or fast ticks; provenance-based counting can't.
2. **Due-check against persisted `next_run_at`**, recomputed *after* firing from the
   cron spec — a missed tick never loses a firing, a crash mid-tick re-fires idempotently
   (the active-job check catches the duplicate).
3. **UTC everywhere** in cron math.
4. **Panic recovery around the loop** — a panicking schedule must not kill the daemon.
5. **Errors in one schedule don't stop the loop** — log and continue.
6. Config sync (disable removed schedules) runs outside the tick loop; the loop only
   ever fires what's enabled.

## What NOT to carry

The 30s interval itself is arbitrary; the refill-on-ticker shape matters, not the
number. agent-minder's `RunOnce` (manual immediate fire) shares the same job-creation
path — keep manual and automatic firing on **one** code path with the same provenance
recording, or dedup breaks.
