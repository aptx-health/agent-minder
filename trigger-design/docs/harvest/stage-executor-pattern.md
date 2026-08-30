---
title: "Harvest: stage executor pattern (deterministic step conductor)"
status: accepted
date: 2026-08-29
tags: [harvest, pattern, stages, workflows]
source: agent-minder internal/supervisor/jobmanager.go Run() (701-983), contract.go
related: "[[0008-workflows-deterministic-steps]], [[0013-ask-and-resume-instead-of-bail]]"
---

# Harvest: stage executor — lift the pattern, not the code

`DefaultJobManager.Run` is the reference implementation of the deterministic step
conductor [[0008-workflows-deterministic-steps]]. The pipeline shape is right; the
autopilot/review/lessons specifics are out of scope.

## The skeleton to reimplement

```
one-time setup (branch-collision guard → worktree → setup hook → agent defs → log)
stages = declared stages, or [default single stage] if none
for i in 0..len(stages):
  emit durable "stage started" (paired with job.current_stage update, one tx)
  result = execute stage (runtime owns process/parse/classify; supervisor routes)
  check user stop between stages
  record cost from the runtime's own reported total (max-across-stages)
  handle usage-limit: bounded wait + session resume, fallback to fresh run
  if success → next stage
  route on_failure (default "bail"):
    "skip"  → durable event, next stage
    "retry" → bounded (default 1); re-run previous stage with feedback
    "bail"  → finalize bail
finalize pipeline
```

## Decisions worth transplanting

1. **Durable stage events, atomic with state**: each stage transition emits a durable
   event in the same tx as the `current_stage` update (store-first publish) — the TUI
   can render stage progress purely from the event log.
2. **Routing keys off agent type, never stage name** — stage names are user-defined
   ("review"/"verify"/"audit"); only the agent identifies execution semantics.
3. **Branch-collision guard before worktree setup**: two jobs deriving the same branch
   (`agent/issue-<N>`) must not silently destroy each other's worktrees — the second
   job blocks with `branch_in_use` instead.
4. **Retry = jump back with feedback**: `i -= 2` re-runs the previous stage with the
   review assessment injected as a feedback prompt, bounded by per-stage retry counts.
   The feedback-passing mechanism (assessment → formatted prompt) is the reusable idea;
   the reviewer wiring is not.
5. **Usage-limit recovery is a stage-level concern**: bounded wait → resume via
   `session_id` → fall back to fresh run if resume unsupported → fail with
   `usage_limit` when exhausted. Never loops unbounded.
6. **Cost is monotonic max across stages**, sourced from the runtime's own result
   (never log scraping — closes cost undercounting for non-claude runtimes).
7. **User stop is checked between stages**, not mid-stage; mid-run cancellation is the
   process signal's job.

## v1 limits for Trigger ([[0008-workflows-deterministic-steps]])

Linear-first: `on_success`/`on_failure` routing only, `retry` with explicit bounds,
`skip` for optional steps. Do NOT copy: reviewer-stage special-casing, lesson capture,
auto-appended review stages, usage-limit session resume (Trigger's ADR-0012/0013
define the failure story differently — `blocked` + ask/resume).
