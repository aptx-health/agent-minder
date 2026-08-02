# Control Plane Milestone Task Breakdown

Status: proposed
Last updated: 2026-08-01

Companion to [`control-plane-and-tui-plan.md`](control-plane-and-tui-plan.md). This
document decomposes milestones M1–M3 (GitHub milestones 20, 21, 22) into
issue-sized tasks with sequencing and dependencies. No issues are created from
this yet; it is the decomposition map.

## Grounding — confirmed code seams

- `cmd/deploy.go` — `runForeground` (~L303) and `runDaemon` (~L406) each
  independently assemble scheduler + supervisor + `daemon.NewServer`. That
  duplication is the Coordinator extraction target.
- `internal/daemon.Server` — existing routes (`/status`, `/jobs`, `/jobs/{id}`,
  `/jobs/{id}/log`, `/dep-graph`, `/metrics`, `/lessons`, `/stop`, `/resume`)
  become compatibility wrappers under `/api/v1`.
- `Supervisor.emitEvent` uses a single bounded `chan Event, 64` with
  drop-on-full — confirms the "no fan-out / resumable stream" gap.
- `RunInfo.CurrentTool/ToolInput/StepCount` is live-only, cleared after the tool
  completes — the durable-activity gap.
- `triggerRoutes` live in Supervisor memory (`watch.go`); cron schedules persist
  in `job_schedules` (name-only PK) — the automation-state + schedule-scoping
  gaps.
- Schema is at v8; `cmd/tui.go` is a stub, `internal/tui/` does not exist yet.

---

## M1 — Coordinator & Observability (milestone 20)

The spine of the project. Hard rule: characterize before you refactor, refactor
before you extend.

### Wave 0 — Characterization (blocks the extraction; do first)

| Task | Scope | Deliverable |
|---|---|---|
| M1-01 Foreground char tests | `cmd/deploy.go` foreground path | Golden tests pinning jobs.yaml load, subscription-map print, event-stream output, Ctrl-C shutdown of deployment + agents |
| M1-02 Daemon + route char tests | `internal/daemon` | Snapshot current `/status`, `/jobs`, `/jobs/{id}/log` response shapes so v1 cannot silently break them |

### Wave 1 — Coordinator extraction (structural, behavior-preserving)

| Task | Scope | Deps |
|---|---|---|
| M1-03 `internal/coordinator` package | New Coordinator type owning jobs.yaml snapshot, scheduler, supervisor, trigger routes, lifecycle (`Start/Run/Snapshot/Stop`). No behavior change | 01,02 |
| M1-04 Rewire foreground onto Coordinator | `runForeground` constructs+runs Coordinator; char tests stay green | 03 |
| M1-05 Rewire daemon onto Coordinator | `runDaemon` hosts the same Coordinator | 03,04 |

### Wave 2 — Automation correctness (mostly parallel once Coordinator lands)

| Task | Scope |
|---|---|
| M1-06 Install `milestone:*` triggers | Currently validate but never become trigger routes (`watch.go`) |
| M1-07 Carry trigger budget/turn overrides into jobs | Overrides dropped at activation today |
| M1-08 Scope schedules to `(deployment_id, name)` + preserve last-run on replace | Schema migration; fixes global-name identity |
| M1-09 Consistent disable-on-removal in reconciliation | Removed defs must be disabled, not orphaned |
| M1-10 Loaded-automation snapshot model | Reconcile in-memory triggers + persisted cron into one durable `automations` table (kind, expr, effective settings, enabled/error, last eval/match/activation, next run, config revision). Schema migration |

### Wave 3 — Durable domain model (additive schema; feeds the API)

| Task | Scope |
|---|---|
| M1-11 Job provenance | `source_type/source_name/source_ref` columns + write at activation |
| M1-12 Agent-run/attempt records | Per stage/attempt: agent, runtime, model, session, timestamps, status, stop reason, steps-vs-turns, cost, final text, log id. Schema + supervisor writes |
| M1-13 Durable recent activity | Persist sanitized current/recent tool+input instead of clearing after tool completes |
| M1-14 Normalized final usage/results | Extract exact turns, final text, sessions from raw logs into normalized fields |
| M1-15 Typed deliverables model | PR/issue/comment/report/review/branch/worktree/file/summary records. Schema + writes |
| M1-16 Consistent log identity | Run-scoped log addressing; unify local/remote follow behavior |

### Wave 4 — Event system (prereq for SSE + durable events)

| Task | Scope | Deps |
|---|---|---|
| M1-17 `internal/eventbus` fan-out | Replace bounded drop-channel with fan-out + monotonic cursor + subscriptions; terminal/persistence/API each subscribe | 03 |
| M1-18 Persist typed job events | cursor, ts, deployment/job/run, severity, summary, structured data | 17 |

### Wave 5 — Versioned read API

| Task | Scope | Deps |
|---|---|---|
| M1-19 `internal/controlapi` scaffold | v1 resource types, stable envelopes, cursor pagination, `/api/v1/meta` (version/mode/capabilities) | 10–16 |
| M1-20 Read endpoints | deployments, deployment/{id}, automations, jobs, job/{id}, runs, deliverables, logs | 19 |
| M1-21 `/api/v1/events` SSE | event IDs, heartbeats, `after`/`Last-Event-ID` resume, snapshot resync | 18,19 |
| M1-22 Local Unix socket transport | Permission-scoped, default-on for worker; no TCP/`--serve` required | 19 |
| M1-23 Legacy route compat + ownership checks | Old routes wrap v1; every job/log lookup verifies deployment ownership | 20 |

### Wave 6 — Exit gate

| Task | Scope |
|---|---|
| M1-24 Contract/compat + exit-gate integration | API golden tests; prove an external read-only client observes a running Coordinator and foreground is unchanged |

Waves 2 and 3 parallelize heavily once the Coordinator lands (04/05). Critical
path: 01 → 03 → 04 → 17 → 19 → 20/21 → 24.

---

## M2 — Multi-Deployment TUI (milestone 21)

Pure consumer of M1's API. Views can't start until M1-20/21 exist, but shared
services and registry extraction can begin in parallel with late M1.

### Wave A — Shared services + aggregation

| Task | Scope |
|---|---|
| M2-01 `internal/checkout` service extraction | Move worktree/session-launch/URL/log formatting out of `cmd/checkout.go` into shared services used by both CLI and TUI |
| M2-02 `internal/registry` local discovery | Discover local workers via PID/heartbeat/socket; stable `(server_id, deployment_id, resource_id)` identity |
| M2-03 Host-level aggregate API | Combine worker snapshots + host-wide durable history; identical resource shapes to singleton endpoint |

### Wave B — TUI foundation

| Task | Scope | Deps |
|---|---|---|
| M2-04 API-only client + attach model | Replace `cmd/tui.go` stub; connect to socket/host, no DB/log/supervisor imports | A |
| M2-05 Host/repo/deployment selection | Discovery + scope picker (all deployments / one repo / one deployment) | 04 |
| M2-06 Resumable activity + log following | Consume SSE with snapshot resync + formatted live-log tail, history catch-up | 04 |

### Wave C — Views

| Task | Scope |
|---|---|
| M2-07 Overview | health, capacity, budget, last trigger poll, next cron, active agents, attention items, recent deliverables |
| M2-08 Automations | loaded triggers/watches/cron, revision, drift/error, effective agent/runtime/limits, last/next, linked jobs |
| M2-09 Runs | evolve checkout history into live cross-deployment master-detail |
| M2-10 Run Detail | activation source, stage/agent timeline, current+recent activity, usage/limits, logs, results, reviews, failure info, deliverables |

### Wave D — Interaction + robustness

| Task | Scope |
|---|---|
| M2-11 Checkout action menu + shortcuts | Enter-driven menu preserved; contextual shortcuts (logs, worktree, interactive continue, issue/PR, copy, filter); capability-aware local/remote gating |
| M2-12 Dirty/unpushed-work protection | Confirm before force worktree removal / branch replacement |
| M2-13 Edge states + help | empty/disconnected/degraded/narrow-terminal/multi-deployment; keyboard help; filtering |
| M2-14 TUI model tests + exit-gate integration | Pure model tests (nav/resize/filter/disconnect/degrade); prove one TUI monitors ≥2 concurrent deployments incl. foreground with zero Coordinator effect |

"Steps vs turns" display rule (assistant-message count as steps, labeled
estimates) lives inside 09/10. Critical path: A → 04 → 06 → 09/10 → 14.

---

## M3 — Safe Operations (milestone 22)

Only starts after the read model is proven. Build the command envelope before
any actual mutation — every control shares the same authorization / idempotency
/ audit spine.

### Wave I — Command infrastructure (blocks all mutations)

| Task | Scope |
|---|---|
| M3-01 Command framework | Scoped server-side commands: capability declaration, deployment/job scope, idempotency keys, command-state events (pending/accepted/completed/failed/timed-out), audit trail |
| M3-02 AuthZ + trust + capability negotiation | Local/remote trust rules, authorization, capability advertisement via `/meta` |

### Wave II — Job & automation controls (each = coordinator method + endpoint + TUI confirm)

| Task | Scope |
|---|---|
| M3-03 Run an automation now | `POST …/automations/{name}:run` |
| M3-04 Pause/resume dispatch | deployment-level |
| M3-05 Stop job / stop agent | graceful cancellation + timeout semantics |
| M3-06 Retry a job | re-activation with provenance |
| M3-07 Resume / extend budget | explicit, approved budget extension |

### Wave III — Config reload

| Task | Scope |
|---|---|
| M3-08 Validated `jobs.yaml` reload | `Coordinator.Reload` with diff + error reporting; applies validated file state, no second config store |

### Wave IV — Managed deployment lifecycle

| Task | Scope |
|---|---|
| M3-09 Managed worker start/stop/restart | Child-process lifecycle (pending open decision — see below) |
| M3-10 Health, recovery, reconcile | Crash recovery for commands interrupted by server/worker restart |

### Wave V — TUI + hardening

| Task | Scope |
|---|---|
| M3-11 TUI confirmations | Clear target + impact summaries for every command |
| M3-12 Command hardening tests | Idempotency, cancellation, crash-recovery, failure-injection; exit-gate proof that commands reach only the intended Coordinator and managed mode is never mandatory |

Critical path: 01 → 02 → (03–08 parallel) → 12. Managed lifecycle (09/10) is
separable and gated on the child-process open decision.

---

## Cross-cutting notes

**Schema migrations.** M1 alone needs roughly v9–v13 (schedule scoping,
automations, provenance, agent-runs, events, deliverables). Use one migration
per additive model rather than a batch — keeps each reviewable and matches the
"never edit an existing migration" rule. Each of M1-08/10/11/12/15/18 carries
its own migration + `TestClaudeMDSchemaVersion` bump.

**Open decisions that gate scoping.** Resolve before writing the affected
issues:

- API contract as Go types vs OpenAPI vs both → shapes M1-19.
- Managed workers child-process vs in-process → shapes M3-09/10.

**Autopilot sequencing.** Minder's own supervisor does not sequence prose
dependencies, so if these run through autopilot, only dependency-free roots
should be labeled agent-ready: M1-01, M1-02, and the Wave-2 correctness fixes
(06/07/09) that do not depend on the Coordinator. Everything gated on M1-03 (the
Coordinator) must stay unlabeled until it lands, or blocked issues get picked up
and bail.
