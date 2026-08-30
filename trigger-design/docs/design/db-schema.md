---
title: "Database schema (design)"
status: draft
date: 2026-08-29
tags: [design, schema, sqlite, runs, events]
related: [[0002-local-sqlite-source-of-truth]], [[run-lifecycle-and-slots]], [[event-observability]], [[config-resolve-once]], [[agent-runs-table]], [[event-log-store-first]], [[fable-expedition-crosswalk]]
---

# Database schema (design)

Consolidates the columns every other design doc implies. SQLite, WAL, foreign keys,
single-writer (`SetMaxOpenConns(1)`) — harvest [[sqliteutil-wal-recovery]]. Draft; the schema
evolves through migrations, so this doc churns.

## Principle: config is not in the database

Job **definitions** live in the config YAML ([[config-schema]]) — that is their source of
truth. The database holds only **runtime state**: runs, their steps, the event log, and the
scheduler bookkeeping needed to survive restarts. This is a deliberate departure from
agent-minder (which stored deployments/jobs as rows). Consequence: `GET /jobs` reads config,
not the DB; a run **freezes** its resolved config on its rows (resolve-once,
[[config-resolve-once]]) so it is self-describing even after the config file changes.

## Tables

### `runs` — one row per job execution instance
```sql
CREATE TABLE runs (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  job_name      TEXT NOT NULL,
  trigger_kind  TEXT NOT NULL,            -- cron | github | webhook | manual
  cause_json    TEXT,                     -- .trigger template context (ADR 0010)
  dedup_key     TEXT,                     -- idempotency; NULL = always run
  status        TEXT NOT NULL,            -- queued|running|succeeded|failed|blocked|awaiting_input|cancelled
  current_step  TEXT,                     -- resume cursor at the run level (ADR 0008)
  resolved_json TEXT,                     -- frozen job-level resolved config (resolve-once)
  attempts      INTEGER NOT NULL DEFAULT 0,
  failure_class TEXT,                     -- infra | logic (kept distinct, ADR 0008/0012)
  failure_detail TEXT,
  -- parking payload (current park only; history lives in events) — ADR 0012/0013
  parked_reason TEXT,                     -- failure | question | scope_request
  request_json  TEXT,                     -- question schema / typed scope request
  answer_json   TEXT,
  input_timeout DATETIME,
  within_charter TEXT,                    -- true | false | unknown — deterministic render field
                                          -- for the current park; 'unknown' must NOT read as
                                          -- approval (ADR 0014/0021 authority verdict)
  -- phase + review intent (ADR 0020) — GENERAL fields, not charter-specific
  phase         TEXT,                     -- general run phase; `expected_red` is one value
  review_intent TEXT,                     -- none | agent | human — explicit, NOT inferred from branch
  -- ratified-contract protection (ADR 0018) — GENERAL ratified-artifact capability
  checkpoint_ref TEXT,                    -- immutable git object (commit/tree SHA) at a ratification gate
  manifest_json TEXT,                     -- protected-artifact identities + digests (drift-checked)
  -- liveness / scheduling
  owner         TEXT,                     -- daemon instance id while running
  heartbeat_at  DATETIME,
  next_attempt_at DATETIME,               -- backoff gate for requeue
  queued_at     DATETIME NOT NULL,
  claimed_at    DATETIME,
  started_at    DATETIME,
  completed_at  DATETIME
);
CREATE INDEX idx_runs_status ON runs(status);
CREATE INDEX idx_runs_dedup  ON runs(dedup_key);
CREATE INDEX idx_runs_job    ON runs(job_name);
```

### `run_steps` — per-step, per-attempt records (harvest [[agent-runs-table]])
```sql
CREATE TABLE run_steps (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id       INTEGER NOT NULL REFERENCES runs(id),
  step_name    TEXT NOT NULL,             -- identity within the run
  attempt      INTEGER NOT NULL DEFAULT 1,
  kind         TEXT NOT NULL,             -- agent | script
  -- station execution + claim (ADR 0021) — who actually did the work
  execution    TEXT,                      -- resolved holder kind: agent | human (frozen at claim; config declares eligibility)
  claimed_by   TEXT,                      -- claim identity; makes builder≠verifier enforceable deterministically
  artifact_json TEXT,                     -- the step's produced artifact/output (done.artifact, .steps.<name> in templating,
                                          -- AND the home for workflow-specific payloads: charter draft, verifier verdict,
                                          -- charter_version, scenarios — kept OUT of columns to honor ADR 0017)
  -- agent provenance + basis honesty (Expedition V, [[fable-expedition-crosswalk]])
  agent        TEXT, runtime TEXT, runtime_version TEXT,
  model_requested TEXT,                   -- what config asked for
  model_resolved  TEXT,                   -- runtime-OBSERVED; NEVER written from config; NULL if unconfirmed
  model_source    TEXT,                   -- which precedence rank won
  cost_basis   TEXT,                      -- exact | estimated | unavailable | runtime-defined
  turn_basis   TEXT,                      -- cli_turns | completed_turns | message_steps
  limits_enforced TEXT,                   -- JSON: which limits the adapter actually enforced
  session_id   TEXT,                      -- enables resume (ADR 0008/0013)
  -- script (harvest script-execution-config)
  command      TEXT, shell TEXT, work_dir TEXT, env_json TEXT,
  status       TEXT NOT NULL DEFAULT 'running',
  stop_reason  TEXT, failure_detail TEXT,
  step_count   INTEGER DEFAULT 0,         -- live progress (may over-report)
  final_turns  INTEGER DEFAULT 0,         -- final truth from parsed result
  cost_usd     REAL DEFAULT 0.0,
  max_turns    INTEGER, max_budget_usd REAL,  -- effective limits, frozen (resolve-once)
  final_text   TEXT, log_path TEXT,
  started_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
  last_activity_at DATETIME,              -- liveness / stall detection
  completed_at DATETIME
);
CREATE INDEX idx_run_steps_run ON run_steps(run_id);
```
Keep the harvested decisions: **progress vs. final-truth split** (`step_count`/`final_turns`),
**frozen effective limits**, **`session_id`+`log_path` for resume/forensics**. Script steps
reject agent fields at config load ([[script-execution-config]]). Basis honesty
([[fable-expedition-crosswalk]] §V): `model_resolved` is **runtime-observed, never config**;
never sum `exact` with `estimated` cost; never compare `turns` across runtimes; warn when
`model_requested ≠ model_resolved`. **Every resume attempt is its own row**
(`stop_reason = "resumed_from:<id>"`), so resume forensics are per-attempt.

### `events` — durable event log (harvest [[event-log-store-first]])
```sql
CREATE TABLE events (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,   -- load-bearing; never recycle rowids
  time     DATETIME NOT NULL,
  run_id   INTEGER NOT NULL DEFAULT 0,          -- 0 = unattributable; NO FK
  job_name TEXT,
  step     TEXT,
  type     TEXT NOT NULL,                        -- taxonomy in event-observability
  severity TEXT NOT NULL,                        -- info | warn | error
  summary  TEXT NOT NULL,
  data     TEXT                                  -- JSON; never secrets (ADR 0006)
);
CREATE INDEX idx_events_id ON events(id);

CREATE TABLE event_log_meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  epoch            INTEGER NOT NULL DEFAULT 1,   -- rotates only when history destroyed
  truncated_through INTEGER NOT NULL DEFAULT 0,  -- retention floor
  created_at       DATETIME NOT NULL
);
```
Invariants harvested verbatim: **commit is the publish** (append in the same tx as the state
change); **retention = deletion + floor, never compaction**; **watermark =
MAX(max(id), truncated_through)**; **epoch rotates only on destroyed history**; cursor is
`(epoch, id)` with typed refusals (truncated / cursor-ahead / epoch-mismatch). No deployment
scoping in v1 (single daemon); the cursor is `(epoch, id)` alone.

### `trigger_state` — scheduler bookkeeping across restarts
```sql
CREATE TABLE trigger_state (
  job_name        TEXT PRIMARY KEY,
  last_fired_slot DATETIME,     -- cron dedup + skip-missed (ADR 0005 / trigger-abstraction)
  one_shot_fired  INTEGER NOT NULL DEFAULT 0,  -- at:/in: never refire after firing
  next_run_at     DATETIME,
  updated_at      DATETIME NOT NULL
);
```
This is what makes **skip-missed** and **one-shot-no-refire** survive a Ctrl-C/restart — the
only scheduler state that must persist (harvest agent-minder's `job_schedules` last-run/enabled
semantics, minus the definition columns, which now live in config).

## The state is SQLite + Git; GitHub is a projection

The authoritative state of any run is **this database plus Git commit hashes** — `checkpoint_ref`
and the branch/commit refs recorded per step are Git objects, not GitHub artifacts. GitHub
issues/PRs/CI are an **optional projection** of this state ([[0002-local-sqlite-source-of-truth]],
[[0020-expected-red-and-topology-agnostic-review]], [[0021-step-execution-and-done]]); kill
GitHub and the truth survives. Git is a dependency; GitHub is not.

## Not in the database

- **Job/step definitions** — config YAML.
- **Secrets** — macOS Keychain ([[0006-secrets-and-agent-permissions]]); the DB stores only
  secret *names* a step references, never values.
- **Authority audit history** — the event log (`run.answered`, `authority.escalated/granted`
  carry who/mode/within-charter). The run row holds only the *current* park payload.
- **Charter-specific data** — `charter_version`, verifier verdicts, ratified scenarios, and the
  like are **not columns** (that would privilege charter, ADR 0017). They ride in
  `run_steps.artifact_json` as produced-artifact payloads. The engine columns above
  (`phase`, `review_intent`, `checkpoint_ref`, `manifest_json`, `execution`, `claimed_by`) are
  all **general** capabilities the charter workflow is merely the first consumer of.

## Migrations

Harvest agent-minder's discipline: a `schema_version` constant, one migration guard per bump,
never edit an existing migration. A test asserts the documented version matches the constant.
Start at v1.

## Open questions

- Whether `runs.resolved_json` is enough or per-step resolved config should also freeze on
  `run_steps` (leaning: per-step, since steps carry their own runtime/model).
- Retention default (harvest 10000) and when `truncated_through` advances.
- Whether a run that parks multiple times needs a `run_parks` table or the events history
  suffices (leaning: events suffice for v1).
