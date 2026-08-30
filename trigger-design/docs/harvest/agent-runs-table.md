---
title: "Harvest: agent_runs table shape"
status: accepted
date: 2026-08-29
tags: [harvest, schema, runs, resumability, transplant]
source: agent-minder internal/db schema.go:179-218
related: "[[0008-workflows-deterministic-steps]], harvest/event-log-store-first.md"
---

# Harvest: `agent_runs` table shape

Durable per-step/per-attempt run records — the resumability basis for deterministic
workflows and the unit of audit ("what exactly did the agent do this time?"). One row
per `(job_id, stage, attempt)`.

```sql
CREATE TABLE agent_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id INTEGER NOT NULL REFERENCES jobs(id),
    stage TEXT NOT NULL,              -- identity within the job
    attempt INTEGER NOT NULL DEFAULT 1,
    agent TEXT NOT NULL, runtime TEXT, model TEXT,
    runtime_version TEXT, session_id TEXT,   -- session_id enables resume
    status TEXT NOT NULL DEFAULT 'running',
    stop_reason TEXT, failure_detail TEXT,
    step_count INTEGER DEFAULT 0,     -- live progress (may over-report)
    final_turns INTEGER DEFAULT 0,    -- final truth from the result event
    cost_usd REAL DEFAULT 0.0,
    max_turns INTEGER, max_budget_usd REAL,  -- effective limits, resolved once
    final_text TEXT, log_path TEXT,
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_activity_at DATETIME,        -- liveness signal (stall detection)
    completed_at DATETIME
);
CREATE INDEX idx_agent_runs_job ON agent_runs(job_id);
```

## Design decisions worth keeping

1. **`step_count` vs `final_turns` are separate columns** — live progress (updated
   during the run, may be wrong) is never conflated with final truth (written once
   from the parsed result). UI reads progress; billing/audit reads final.
2. **Effective limits are frozen on the row** (`max_turns`, `max_budget_usd`) —
   the resolve-once rule ([[config-resolve-once]]): display and diagnostics read what
   the run *actually had*, not what config says *now*.
3. **`session_id` + `log_path` on the run row** — resume and log forensics need
   nothing but the row. `final_text` also lands here (bail reports live there).
4. **`runtime`, `model`, `runtime_version` recorded per run** — per-run provenance;
   answers "which model actually ran" months later.
5. **`last_activity_at` as liveness** — stall detection compares wall-clock against
   this, not `started_at`.
6. **No FK on `run_id` in events** — events reference runs loosely (`run_id 0` when
   unattributable) so event writes never fail on ordering.

## Interplay with the event log

`SnapshotControl` joins `agent_runs → jobs` to stay deployment-scoped — the join
through the owning job is what enforces scoping; keep it.

## Transplant note

Rename freely (`runs`), keep: stage+attempt identity, the progress/final-truth split,
frozen effective limits, session/log pointers. The failure_reason taxonomy lives on
the job (see harvest/agentutil-log-parsing.md); `failure_detail` here is per-attempt.
