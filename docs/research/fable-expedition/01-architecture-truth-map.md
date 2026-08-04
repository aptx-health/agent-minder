# Expedition I — Architecture Truth Map

Status: complete
Reviewed baseline: `origin/main` @ `10bf29a2741c3b78b52fa4587cd00a3d611f6f93` (merge of PR #580, 2026-08-03 20:03 -0600)
Verification at baseline: `go build ./...` clean, `go test ./...` fully green.
Evidence reviewed: `docs/control-plane-and-tui-plan.md`, `docs/control-plane-milestone-breakdown.md`, `docs/integration-target-mvp-plan.md`; `internal/coordinator`, `internal/daemon`, `internal/supervisor`, `internal/eventbus`, `internal/db`, `internal/scheduler`, `internal/runtime`, `cmd/deploy.go`, `cmd/checkout.go`, `cmd/tui.go`; GitHub milestones 19–23; merged PRs #546–#587 and their driving issues.

Claims below are cited to file:line at the reviewed SHA, or to an issue/PR. Statements derived by reading code paths rather than executing them are marked *(inference)*.

---

## 1. Ownership and lifecycle map

Who owns each kind of state today, as built — not as planned.

### Desired state (what should happen)

| State | Owner | Evidence |
|---|---|---|
| Automation config (cron + trigger jobs) | `.agent-minder/jobs.yaml`, loaded once at Coordinator construction | `internal/coordinator/coordinator.go:83-94` |
| Deployment settings (agents, budgets, base branch, watch filter, runtime) | CLI flags → `deployments` row, fixed at deploy time | `internal/db/schema.go:22-43` |
| Agent behavior | `.claude/agents/*.md` → user-level → built-in registry | `internal/supervisor/templates.go` |
| Per-issue intent (future: integration targets) | GitHub issue body — proposed only, nothing parses it yet | `docs/integration-target-mvp-plan.md` |

There is no record of *which* config revision a Coordinator loaded: no source SHA-256, load time, validation error, or drift flag. `coordinator.Coordinator` holds `cfg *scheduler.Config` privately with no revision metadata (`coordinator.go:60-62`).

### Active state (what the running process believes)

| State | Owner | Durability |
|---|---|---|
| Loaded config snapshot | `Coordinator.cfg` (memory) | Lost on restart; re-derived from file |
| Trigger routes | `Supervisor` memory via `SetTriggerRoutes` (`internal/supervisor/watch.go:49`) | None — no durable identity for a trigger automation |
| Cron schedules | `job_schedules` table, upserted by `SyncSchedules` (`internal/scheduler/scheduler.go:37-94`) | Durable, scoped `(deployment_id, name)` (`internal/db/schema.go:161`) |
| Daemon-mode flag | Derived in `coordinator.New` (`coordinator.go:96-97`) | Re-derived |

The asymmetry the plan flagged — triggers in memory, cron persisted — is still the live shape. `Coordinator.Snapshot()` (`coordinator.go:118-120`) recomputes a merged view on demand, best-effort (DB errors silently yield no cron entries).

### Live state (what is happening right now)

| State | Owner | Notes |
|---|---|---|
| Running jobs, elapsed, current tool, step count | `Supervisor.running` map / `RunInfo` (`internal/supervisor/supervisor.go:61-73`) | `CurrentTool` is cleared when the tool completes (`internal/supervisor/runtime.go:65`); no recent-activity history survives |
| Event stream | `eventbus.Bus[Event]` inside Supervisor (`supervisor.go:140,199`) | Cursor-based fan-out; in-memory only; log grows without bound (`internal/eventbus/eventbus.go:38,66`) |
| Budget pause state | Supervisor, exposed to the HTTP server only through function callbacks (`cmd/deploy.go:369-370,485-486`) | Not reachable by any other client |
| Worker liveness | PID + heartbeat files (`internal/daemon`) | Basis for local discovery, unused for discovery today |

None of the live state crosses the process boundary. `internal/daemon.Server` reads SQLite plus three callbacks (`internal/daemon/server.go:25-31`); it never touches `Supervisor.RunningJobs()`, so current-tool/step/elapsed data is invisible to `status --remote`, SwiftBar, and any future TUI.

### Durable state (what survives restart)

Schema v11 (`internal/db/schema.go:13`). Owners:

| Table | Role |
|---|---|
| `jobs` | Backward-compatible aggregate: status machine, cost, PR, provenance columns (v10), per-job overrides |
| `agent_runs` (v11) | Per stage/attempt truth: agent, runtime, model, session, timestamps, status, stop reason, steps, cost, final text, log path (`schema.go:164-202`) |
| `job_schedules` | Cron identity + last/next run |
| `dep_graphs`, `lessons`, `job_lessons`, `repo_onboarding` | Unchanged from pre-M1 |

Not present, despite being in the M1 durable model: an `automations` table, typed job events, deliverables, durable recent activity. Raw agent logs (JSONL on disk) remain the only record of tool-level activity and are addressed by path, with `agent_runs.log_path` (v11) as the first run-scoped pointer.

### Historical state (what happened across deployments)

SQLite is host-wide; `jobs` + `agent_runs` accumulate across deployments. The strongest existing consumer is the checkout picker, which queries by repo across deployments (`cmd/checkout.go:105`, `GetJobsByRepo`). It reads the DB directly in local mode and the daemon client in remote mode — two divergent code paths in one command (`cmd/checkout.go:56-143` vs `:356-431`), which is the M2-01 extraction target.

### Lifecycle summary

- Deployment: `deploy` → `deployments` row → `coordinator.New` in both `runForeground` (`cmd/deploy.go:332`) and `runDaemon` (`cmd/deploy.go:445`) → `Start/Run` → signal → `Stop` drains and calls `runtime.ShutdownAll()` (`supervisor.go`, `Stop`).
- Job: `queued → running → review → reviewing → reviewed → done | bailed | blocked`, all transitions written to `jobs`.
- Agent run: `beginAgentRun` at stage start, `finishAgentRun` with final text/turns/cost/session at stage end (`internal/supervisor/jobmanager.go:365-427`). Best-effort: a failed insert never fails the stage.
- Event: `emitEvent` → bus publish → subscribers (foreground stdout is one subscriber, `cmd/deploy.go:377-386`). Events die with the process.

---

## 2. M1 exit-criterion audit

Milestone 20 defines four exit criteria. GitHub shows the milestone as 11 closed / 0 open issues, which is misleading: the milestone-breakdown task list has ~24 tasks and the remaining ones were deliberately never filed ("dispatch paused — budget", `docs/control-plane-milestone-breakdown.md:56-59`).

### Criterion-level verdicts

**EC1 — "`minder deploy --foreground` behaves as it does today and requires no UI or server process."** **Satisfied.** Both entry points construct the same Coordinator (`cmd/deploy.go:332,445`); the green anchor suite (#559) pinned subscription-map output, cron persistence, clean shutdown, and legacy route shapes before the refactor, and the full suite is green at the reviewed SHA.

**EC2 — "An external read-only client can observe the exact automations, jobs, agents, activity, usage, logs, and deliverables known to a running Coordinator."** **Not satisfied.** No `/api/v1`, no `internal/controlapi`, no Unix socket (only two `net.Listen` sites exist: opencode's loopback server and the daemon's opt-in TCP listener). The legacy API exposes DB-backed jobs and status only; live activity, automations, runs, and deliverables are all unreachable. `GET /jobs/{id}` does not verify the job belongs to the served deployment (`internal/daemon/server.go`, `handleJob` calls `store.GetJob(id)` with no deployment check), so what *is* observable is not correctly scoped.

**EC3 — "API state survives client disconnects and can be resynchronized without relying on uninterrupted event delivery."** **Partially satisfied at the wrong layer.** The eventbus provides exactly the right semantics in-process — monotonic cursor, `Subscribe(afterCursor)` with gap-free catch-up, no silent drops (`internal/eventbus/eventbus.go:84-109`) — but no API transport exposes it, and the log is memory-only, so a *process* restart loses everything.

**EC4 — "Existing workflows remain compatible, with contract tests covering the versioned API."** **Compatibility: satisfied** (anchors + legacy routes unchanged, `/tasks` aliases retained, `server.go:55-56`). **Contract tests: not started**, deliberately deferred until M1-19 lands the envelope (Resolved decision, plan doc).

### Task-level audit

| Task | Filed | Verdict | Evidence |
|---|---|---|---|
| M1-01 anchors | #559 | Done | PR #562; suite green |
| M1-02 red specs | #560 | Done | PR #564; specs now green, no remaining `t.Skip` in `automation_correctness_spec_test.go` |
| M1-03/04 Coordinator + foreground | #569 | Done | `internal/coordinator`; `cmd/deploy.go:332` |
| M1-05 daemon rewire | #582 | Done | PR #585; `cmd/deploy.go:445` |
| M1-06 milestone triggers | #566 | Done | `coordinator.go:160-170`; `watch.go:141-186` |
| M1-07 trigger overrides | #567 | Done | `watch.go:259-260` |
| M1-08 schedule scoping | #570 | Done | v9; `schema.go:161` |
| M1-09 disable removed automations | #568 | **Done with a hole** | Removal-by-absence works (`scheduler.go:75-92`); a job *converted* from `schedule:` to `trigger:` keeps its enabled cron row firing, because reconciliation checks name presence, not kind *(inference — see §3, C8)* |
| M1-10 automation snapshot | not filed | Not started | No `automations` table; `Coordinator.Snapshot()` is memory-computed, no revision/error/last-eval |
| M1-11 job provenance | #571 | **Issue satisfied; plan intent ~¼ delivered** | Only trigger activation writes provenance (`watch.go:267-269`). Cron jobs (`scheduler.go:162-181`), `RunOnce`, and targeted `Prepare` (`depgraph.go:54-71`) write none; `BulkCreateJobs` doesn't even include the source columns in its INSERT (`queries.go:96-108`). Watch-filter jobs are mislabeled `trigger` with NULL `source_name` (`watch.go:116-124` funnels into the same `createJobForIssue`) |
| M1-12 agent runs | #583 | Done | v11; writes at `jobmanager.go:382,421` |
| M1-13 durable recent activity | not filed | Not started | `runtime.go:65` still clears `CurrentTool`; only `step_count`/`last_activity_at` persist (`queries.go:347`) |
| M1-14 normalized final usage/results | not filed | **Mostly subsumed by M1-12** | `CompleteAgentRun` records session, final text, exact turns, cost for every new run (`jobmanager.go:404-421`). Residual scope is verification breadth across the three runtimes, not new plumbing |
| M1-15 deliverables | not filed | Not started | No table; PR/branch/worktree live as `jobs` columns |
| M1-16 log identity | not filed | Partial | `agent_runs.log_path` exists; no run-scoped API addressing; local vs remote follow still diverge |
| M1-17 eventbus | #584 | Done | PR #587; foreground consumes via subscription (`deploy.go:377`) |
| M1-18 persist typed events | not filed | Not started | `supervisor.Event` is still `{Time, Type string, Summary string, TaskID}` (`supervisor.go:55-59`) |
| M1-19–23 `/api/v1` wave | not filed | Not started | No `internal/controlapi`, no socket, no ownership checks |
| M1-24 exit gate | not filed | Not started | — |

Net: the completed half is the *foundation* half (correctness, extraction, durable runs, bus). The entire externally observable surface — the thing the milestone is named for — is in the unfiled half. By exit-criteria weight, M1 is roughly one-third done, not half.

---

## 3. Contradictions and stale assumptions

C1. **GitHub milestone 20 reads as complete (0 open / 11 closed) while most exit criteria are unmet.** The unfiled remainder exists only in the breakdown doc. Anyone triaging from GitHub alone will draw the wrong conclusion. *(fact)*

C2. **`docs/control-plane-and-tui-plan.md` "Current architecture and seams" is now half stale.** Of its listed correctness gaps, five are fixed (milestone triggers, trigger overrides, schedule scoping, event fan-out, provenance-for-triggers) and the doc still carries `Status: proposed`, last updated 2026-08-01. The breakdown doc's own grounding section says "Schema is at v8" while its status block says v11. *(fact)*

C3. **The integration-target plan's migration slot is gone.** It plans its migration as v10→v11 (`integration-target-mvp-plan.md`, Planning baseline and Data model sections); v11 was consumed by `agent_runs` (#583). Its baseline (`bfbc705`, schema v9) and its caution about the then-open #569 are both superseded. The plan is architecturally intact — its claim that target parsing/resolution sits below the Coordinator boundary holds at the current code — but every version number and sequencing note in it needs re-baselining to v12+. *(fact)*

C4. **Issue #571's own text says "migration v8→v9"; it landed as v9→v10** (PR #578) because M1-08 took v9 first. Harmless, but a reminder that issue bodies are proposals, not records. *(fact)*

C5. **Provenance taxonomy is collapsed.** The plan defines `source_type ∈ {explicit deploy, watch, trigger, cron, manual}`. Reality: `trigger` is written for both trigger-route and watch-filter activations; nothing else writes anything. The plan's stated payoff — "reliable automation-to-job navigation without inferring ancestry from a job-name prefix" — has not arrived: the scheduler still prefix-matches job names to decide whether a schedule is already active (`scheduler.go:196-214`). *(fact)*

C6. **Milestone 19 (opencode) shows #540 and #547 open although their implementing PRs (#554, #556) merged and the code is present** (`internal/runtime/opencode/opencode.go:145` `Resume`, `bail.go` `ExtractBailReport`). The PR titles referenced issues as "(#540)" rather than with closing keywords. Either residual scope exists or the issues are bookkeeping debt — a human should close or re-scope them; this expedition does not. *(uncertain)*

C7. **The daemon rewire (#582) can be misread as API modernization.** It re-hosted the same legacy `daemon.Server` on the Coordinator. The server remains single-deployment, TCP-only, callback-wired, optionally-authenticated (#524), with `Access-Control-Allow-Origin: *` (`server.go:88-92`) and unscoped job lookups. Nothing about the observability contract changed. *(fact)*

C8. **Schedule kind-conversion leak.** `SyncSchedules` upserts only defs where `IsScheduled()`, and disables only names *absent* from `config.Jobs`. A def renamed in place from `schedule:` to `trigger:` is skipped by both branches, so its old cron row stays enabled and keeps firing. *(inference from `scheduler.go:40-92`; no test covers it)*

C9. **`BulkCreateJobs` and `CreateJob` have silently diverged.** `CreateJob` inserts provenance and per-job override columns; `BulkCreateJobs` (targeted deploys) omits `max_turns`, `max_budget_usd`, and all three source columns (`queries.go:79-108`). Any future field added to one and not the other becomes a path-dependent bug. *(fact)*

C10. **The event vocabulary is still presentation-shaped.** `Event.Type` is an untyped string of eight ad-hoc values and `TaskID` survives "for API backward compat" (`supervisor.go:55-59`). The plan's event model (deployment/job/run identity, severity, structured data) exists nowhere in code. The bus made delivery correct; the *payload* is still the old stdout line. *(fact)*

---

## 4. Ten ranked architectural risks

Ranked by (probability of forcing rework) × (cost of that rework), for the M1-remainder → M2 read-only TUI path.

1. **Persisting today's `Event` shape.** If M1-18 writes `{type string, summary string}` rows, that becomes the `/api/v1/events` contract and the TUI's data model — prose over the wire, unfilterable, unredactable, renamed only via a breaking change. Every downstream wave (SSE, TUI activity, attention items) then binds to it. Mitigation is cheap now (Decision memo DM-2) and expensive after golden tests pin it.

2. **`internal/controlapi` binding to `*Supervisor` instead of a Coordinator-owned surface.** The precedent is already set: the HTTP server is wired with `sup.ResumeBudget` / `sup.IsBudgetPaused` callbacks reaching around the Coordinator (`cmd/deploy.go:369-370`). If the v1 handlers grow the same way, M2's host aggregation and M3's command envelope must each re-abstract the Supervisor — the exact drift the Coordinator was extracted to stop. (DM-5)

3. **Provenance holes poison the Automations→Jobs navigation the TUI is designed around.** Cron jobs are unattributable, watch jobs are mislabeled, targeted jobs are blank, and the scheduler's own dedup still trusts name prefixes (C5, C9). Backfilling `source_type` after months of rows is possible but lossy; writing it correctly at activation is a small, autopilot-safe fix today. (DM-1)

4. **Unbounded eventbus log.** `b.events` grows forever in a process that is explicitly long-lived (supervisor survives task completion; deployments run for days). Wiring more subscribers and chattier typed events accelerates it. The fix (bounded retention + explicit truncation error + snapshot resync) touches the subscription contract, so it should land before SSE freezes that contract. (DM-4)

5. **Daemon API security debt compounds with surface area.** Optional auth (#524), wildcard CORS, TCP-only, and unscoped `/jobs/{id}` are tolerable on a single-user LAN tool with four routes. `/api/v1` multiplies routes and adds an event stream; M2 adds multi-deployment aggregation, making cross-deployment leakage structural rather than cosmetic. Retrofit cost grows with every endpoint shipped under the old posture.

6. **Dual truth between `jobs` aggregate and `agent_runs`.** Cost, status, and final results now live in both (e.g. `TotalSpend` sums `jobs.cost_usd`, `queries.go:299-308`, while per-run cost accrues in `agent_runs`). Multi-stage and retried jobs will diverge the two unless one is declared derived. The plan says `jobs` stays the backward-compatible aggregate; nothing yet enforces or tests aggregate-equals-sum-of-runs.

7. **`SlotContext.BaseBranch` is one string doing three jobs — and two plans now contend for it.** Worktree start point, rebase instruction target, and PR base are all `sc.BaseBranch` (`jobmanager.go:85,136`; `context.go:286-288`). The integration-target MVP needs it split (its issue 4); M1's run/deliverable modeling reads it. Whichever lands second inherits a rebase through the supervisor's most behavior-sensitive seam. Sequencing decision needed, not code.

8. **Trigger automations have no durable identity.** `jobs.source_name` references a jobs.yaml key that exists only in Supervisor memory while the process runs. Rename a trigger and its job history orphans silently; the API can never serve "last activation for automation X" reliably. This is the real argument for some durable automation record (DM-3) — not the TUI's display needs.

9. **Single-connection SQLite meets a growing writer set.** `SetMaxOpenConns(1)` serializes supervisor, scheduler, and API today. M1-18 (event rows at chat-message frequency) and durable activity would multiply write volume, and v1 read endpoints would queue behind them — latency the TUI will render as jank. WAL supports concurrent readers; the single-writer discipline could move to a write-connection + read-pool split, but that is an invariant change requiring its own care.

10. **Normalizing activity/usage before the runtime-semantics audit (Expedition V).** Three runtimes report events, cost, sessions, and turns with different fidelity (opencode computes real USD; codex and claude-code differ on session/resume semantics). M1-13/14 schema decisions taken before Expedition V's audit risk encoding claude-code's shape as the schema. Cheap to sequence correctly now.

---

## 5. Decision memos

### DM-1 — Complete the provenance taxonomy before any API work

**Decide now.** Write `source_type` at every activation site: `explicit` in `Prepare`/`BulkCreateJobs`, `cron` (+ `source_name`=schedule, `source_ref`=fire time) in `fireSchedule`/`RunOnce`, `watch` for watch-filter discoveries, keep `trigger` for route matches; switch `jobAlreadyActive` to query provenance instead of name prefixes. No migration — columns exist since v10.
**Strongest alternative:** defer until the API wave needs it, avoiding churn in `watch.go`/`scheduler.go`. Rejected because every day of deferral writes more unattributable rows, and the fix is autopilot-sized today.
**Confidence:** high. **Revisit trigger:** if Expedition III changes activation flow (integration targets add a pre-dispatch phase), re-check the write sites, not the taxonomy.

### DM-2 — Freeze a typed event envelope before persisting or streaming events

**Decide the envelope now; implement after Expedition IV.** Minimum: cursor, timestamp, deployment/job/run IDs, a closed event-type enum, severity, human summary, and a structured `data` JSON field. Today's `Event` becomes a rendering of it, and `TaskID` dies at the API boundary. M1-18 and M1-21 must not land against the current string-pair shape.
**Strongest alternative:** persist the current shape now, evolve additively later. Rejected: event rows and SSE golden tests are the two hardest contracts to change; "additive" cannot fix a summary-string data model.
**Confidence:** high on sequencing, medium on the exact field set — that is precisely Expedition IV's remit (snapshot/event consistency), so the field set is an input to it, not a preemption of it. **Revisit trigger:** Expedition IV's guide.

### DM-3 — Serve the automation snapshot computed, defer the durable table

**Decide now, cheaply reversible.** v1's automations endpoint should serve `Coordinator.Snapshot()` extended with config revision (path, SHA-256, load time, validation error) and per-automation enabled/error state; derive "last activation" by querying job provenance (`source_name`), which DM-1 makes reliable. Defer the `automations` table (M1-10 as specced) until either M3-08 reload makes config revisions durable-worthy or last-evaluation/match telemetry is genuinely needed.
**Strongest alternative:** the plan's durable `automations` table now. It buys durable identity across renames (risk 8) and last-eval history, at the cost of a migration plus a reconciliation loop that can itself drift (C8 shows reconciliation is where bugs live). For a read-only TUI, computed truth from the live Coordinator is *more* honest than a table that can lag it.
**Confidence:** medium. **Revisit trigger:** the first real need for last-evaluation/last-match history, or M3-08 scoping — whichever comes first. Adding the table later is purely additive.

### DM-4 — Cap eventbus retention with explicit truncation + snapshot resync

**Decide now.** Bounded in-memory retention (count or byte cap); `Subscribe(afterCursor)` below the floor returns a typed truncation error; the documented client response is snapshot-then-resubscribe — which the plan already mandates for reconnecting clients, so this adds no new client burden. Preserves the "never silently drop" invariant by making loss loud and recoverable.
**Strongest alternative:** leave unbounded until M1-18's persistence makes the memory log a cache. Rejected: M1-18 is unscheduled and gated on Expedition IV, while the unbounded log ships in every daemon today; and retrofitting truncation *after* SSE resume semantics are golden-tested changes a frozen contract.
**Confidence:** high. **Revisit trigger:** when M1-18 lands, revisit whether the memory log shrinks to a small tail over the durable store.

### DM-5 — `internal/controlapi` consumes a Coordinator-owned provider interface, never `*Supervisor`

**Decide now — this is the import-graph decision that prevents M2 rework.** Before M1-19, fold the existing callback trio (stop, budget-resume, budget-paused) plus `RunningJobs`, `Snapshot`, event subscription, and store reads behind a state-provider interface defined next to the Coordinator. Handlers see the interface; only the Coordinator sees the Supervisor. The legacy `daemon.Server` can adopt it opportunistically.
**Strongest alternative:** hand `*Supervisor` and `*db.Store` straight to v1 handlers — faster, and matches how `daemon.Server` grew. Rejected: it re-creates exactly the fragmentation (callbacks reaching around the Coordinator, `cmd/deploy.go:368-370`) that #569 was cut to remove, and M2's host aggregator would then need to fake a Supervisor.
**Confidence:** high. **Revisit trigger:** Expedition II's worker-topology decision may move where the interface is *served* (in-process vs socket), but not its existence.

**Now vs reversible, in one line each:** DM-1, DM-2, DM-5 are decide-now (they shape durable data and the import graph). DM-3 and DM-4 are recommended-now but cheaply reversible. Worker/process topology, host aggregation, event field semantics, runtime normalization, and the TUI interaction model are explicitly *not* decided here — they belong to Expeditions II, IV, V, and VII.

---

## 6. System invariants and acceptance gates

### Invariants (violating any of these is a bug, not a tradeoff)

1. `jobs.yaml` is the only owner of desired automation config; no DB- or API-writable config store exists or may be introduced before M3-08's validated reload.
2. Exactly one Coordinator per active deployment; `runForeground` and `runDaemon` construct it only via `coordinator.New` — no third assembly path may appear.
3. `minder deploy --foreground` runs with no server, socket, or TUI, and its stdout (startup summary + event lines) stays byte-stable through refactors; the green anchors are the enforcement mechanism.
4. SQLite: WAL, foreign keys, single-writer discipline (`SetMaxOpenConns(1)`); migrations are additive, never edit an existing migration constant, exactly one `schemaVersion` bump per PR, `TestClaudeMDSchemaVersion` kept in sync. Next free migration slot at the reviewed SHA: **v12**.
5. Events are never silently dropped. Delivery may lag (bounded per-subscriber buffers) or be explicitly truncated (post-DM-4), but a subscriber can always detect loss.
6. `jobs` remains the backward-compatible aggregate; `agent_runs` is per-attempt truth; any derived aggregate (cost, final result) must be reconcilable against its runs.
7. Identity keys are fixed: jobs UNIQUE `(deployment_id, name)`; schedules PK `(deployment_id, name)`; agent runs `(job_id, stage, attempt)`.
8. Legacy HTTP routes (including `/tasks` aliases) remain compatibility wrappers for the life of M1–M3; `/api/v1` is additive.
9. Presentation never imports orchestration: the TUI and any API client may not import `internal/supervisor`, read the DB directly, or parse `jobs.yaml`. (`cmd/checkout.go` currently violates the spirit of this locally; M2-01 resolves it by extraction, not by exception.)
10. Ctrl-C/SIGTERM stops the deployment cleanly: cancel → drain in-flight jobs → `runtime.ShutdownAll()`; client disconnects never alter Coordinator behavior.

### Acceptance gates for the remaining M1 → read-only TUI path

- **G-provenance:** every activation path yields a non-NULL, correctly valued `source_type`; a SQL spec asserts one row per path (`explicit`, `watch`, `trigger`, `cron`); scheduler dedup passes with a renamed job-name format.
- **G-events:** kill a subscriber mid-stream, resubscribe with its last cursor → no gap, no duplicate (exists today, keep); subscribe below the retention floor → typed truncation error and documented resync path (post-DM-4).
- **G-api-scope:** with two deployments in one DB, each server's `/jobs/{id}` and log lookups return 404 for the other's jobs — asserted for legacy routes *and* v1.
- **G-api-live:** an external process, given only the endpoint, reads the same automations list the startup summary printed, and sees a running job's stage, agent, step count, and cost within one poll/SSE interval.
- **G-foreground-neutrality:** foreground stdout is byte-identical with the API surface enabled vs disabled; anchors stay green.
- **G-runs:** after a multi-stage job with one retry, `agent_runs` contains one row per attempt with terminal status, and `jobs.cost_usd` equals the sum of its runs' costs.
- **G-exit (M1-24):** a read-only client observes a live Coordinator across a client restart (snapshot + cursor resume) while `go test ./...` and the anchor suite stay green.

---

## 7. Recommended route forward

The honest critical path to a useful read-only TUI, in dependency levels. Levels are ordered; work within a level parallelizes.

**Level 0 — correctness completion (now; no expedition gates; mostly autopilot-safe)**
- L0.1 Provenance completion per DM-1, including `BulkCreateJobs` column parity (C9). Small, no migration.
- L0.2 Schedule kind-conversion fix (C8) + red spec.
- L0.3 Eventbus retention cap per DM-4. Concurrency-subtle: solo/interactive, not autopilot.
- L0.4 Deployment-ownership checks on the *legacy* routes now (`handleJob`, `handleJobLog`). Do not wait for v1 to fix a live leak.

**Level 1 — contracts (gated on expeditions II/IV outputs where noted)**
- L1.1 Coordinator state-provider interface per DM-5 (refactor, no behavior change; anchors guard it).
- L1.2 Typed event envelope per DM-2 — field set finalized by Expedition IV, then M1-18 persistence.
- L1.3 Automations snapshot endpoint data per DM-3 (config revision on the Coordinator + provenance-derived last-activation).

**Level 2 — the read API (the actual M1 exit)**
- L2.1 M1-19 `internal/controlapi` scaffold: v1 resources, envelopes, cursor pagination, `/api/v1/meta`, golden-JSON tests (the deferred contract tests start here).
- L2.2 M1-20 read endpoints over the provider interface; M1-23 legacy wrappers + scoping parity.
- L2.3 M1-21 SSE (needs L1.2); M1-22 Unix socket. The per-worker local socket is topology-neutral enough to proceed regardless of Expedition II's host-server verdict; only host *aggregation* waits.

**Level 3 — exit gate**
- L3.1 M1-24 integration proof per G-exit, with two concurrent Coordinators.

**Level 4 — M2 entry (can begin once L2.1 stabilizes shapes)**
- M2-01 checkout service extraction has no API dependency and could start any time; sequence it against integration-target issue 4, since both touch the checkout/worktree seam and `SlotContext` split (risk 7).

**Simplify / combine / defer / remove**
- *Combine:* M1-14 into M1-12's verification — the plumbing exists; write cross-runtime specs instead of a new task.
- *Simplify:* M1-13 to "persist a bounded recent-activity tail per agent run" (a JSON column on `agent_runs` or the typed event stream itself once M1-18 lands) rather than a new table; decide after Expedition V reports on per-runtime activity fidelity.
- *Defer:* M1-15 typed deliverables table. v1 can serve a derived deliverables view from existing columns (`pr_number`, `branch`, `worktree_path`, review fields) — every deliverable type beyond those is currently hypothetical.
- *Defer:* the `automations` table (DM-3) and all of M3.
- *Remove (from mental models, not code):* the idea that the daemon rewire modernized the API (C7), and GitHub milestone counts as a progress signal for M1 (C1). Also re-baseline the integration-target plan's version numbers (C3) before writing its issues.
- *Sequencing rule that stands:* integration-target work is compatible with all of the above but its migration goes in the single-file migration queue (v12+), and its `SlotContext` split (its issue 4) must not run concurrently with M1-13/16 supervisor edits.

---

## 8. Cheaper-agent handoff

### Fixed decisions (do not re-litigate)

- API contract source of truth is Go types in `internal/controlapi` with golden-JSON tests; OpenAPI only if an out-of-repo consumer appears (plan doc, Resolved 2026-08-01).
- One Coordinator per deployment; foreground stays first-class; presentation/orchestration separation; legacy routes stay as wrappers (invariants §6).
- Provenance taxonomy: `explicit | watch | trigger | cron | manual` (DM-1).
- Events: typed envelope precedes persistence/streaming; never-silent-drop stands (DM-2/DM-4).
- controlapi consumes a Coordinator-owned interface, never `*Supervisor` (DM-5).

### Open decisions and their owners

- Worker/process topology, host server, registry → Expedition II (#589).
- Event field semantics, snapshot/event consistency, retention limits → Expedition IV (#591).
- Runtime activity/usage normalization, config resolution → Expedition V (#592).
- Integration-target adversarial review → Expedition III (#590); re-baseline to ≥v12 first.
- TUI operator model → Expedition VII (#594), consuming this map + II/IV/V.
- Redaction rules, remote credential storage, browser-UI packaging → still open per plan doc; none block Levels 0–2.

### Prerequisites and safe sequencing

Level 0 items are dependency-free and can be filed immediately. L0.1/L0.2/L0.4 are autopilot-suitable; L0.3 (eventbus) and L1.1 (provider extraction) are interactive/solo work per the established dispatch rule. Migration-bearing tasks (M1-18 persistence, deliverables if revived, integration targets) go one at a time through the v12+ queue. Nothing in Level 0–1 conflicts with the paused-budget state: all are small.

### Likely traps and prohibited shortcuts

1. Editing an existing migration constant, batching two schema bumps in one PR, or forgetting the `CLAUDE.md` version line (`TestClaudeMDSchemaVersion` will catch the last one — do not "fix" the test).
2. Touching foreground stdout formatting while doing any of this. The anchors are the contract; if an anchor fails, the change is wrong, not the anchor.
3. Adding a job-creation field to `CreateJob` but not `BulkCreateJobs` (or vice versa) — C9 is exactly this trap already sprung once. Prefer unifying them while in there.
4. "Fixing" the eventbus by adding a drop policy. Silent drop is the bug that was just removed; retention must be explicit truncation + resync.
5. Writing red specs against Go symbols that don't exist yet. The discipline is assert-on-serialized-surfaces: SQL, JSON bodies as untyped maps, stdout, `GetSchedules` rows (breakdown doc, behavioral test plan).
6. Wiring any new HTTP handler directly to `*Supervisor` or `*db.Store` "temporarily". The callback trio in `cmd/deploy.go:368-370` shows how temporary becomes structural.
7. Dispatching Coordinator-gated tasks to autopilot before their dependency lands — the supervisor does not sequence prose dependencies; blocked issues get picked up and bail. Label only dependency-free roots `agent-ready`.
8. Closing milestone-19 issues #540/#547 as a drive-by. Verify residual scope with a human first (C6).
9. Trusting version numbers in any plan doc or issue body over `schemaVersion` in `internal/db/schema.go:13`. Three documents disagreed with the code during this expedition alone (C2, C3, C4).

### Verification checklist (per landed task)

- [ ] `go test ./...` green; green anchors specifically re-run.
- [ ] If schema changed: fresh-create and migrate-from-previous both tested; `CLAUDE.md` schema section updated; exactly one version bump.
- [ ] If activation paths changed: one SQL assertion per path proving `source_type/source_name/source_ref` (G-provenance).
- [ ] If event code changed: fan-out, resume-after-cursor, slow-subscriber, and (post-DM-4) truncation-resync tests pass.
- [ ] If HTTP surface changed: two-deployment scoping test (G-api-scope) and legacy-route golden shapes unchanged.
- [ ] Foreground byte-neutrality spot-check (G-foreground-neutrality) for anything touching Coordinator, Supervisor events, or deploy.go.
- [ ] PR body uses `Closes #N` — not `(#N)` — so GitHub state stays truthful (C6 is the cautionary tale).

### Suggested issue boundaries (executable without re-litigating architecture)

1. **Provenance completion** — DM-1 + C9 column parity + scheduler dedup by provenance. Autopilot-safe. Red specs: four activation paths.
2. **Schedule kind-conversion disable** — C8 + red spec. Autopilot-safe.
3. **Legacy-route deployment scoping** — L0.4 + two-deployment test. Autopilot-safe.
4. **Eventbus retention + truncation contract** — DM-4. Solo/interactive; primer required.
5. **Coordinator state-provider interface** — DM-5; behavior-preserving refactor guarded by anchors. Solo.
6. **Typed event envelope + M1-18 persistence** — after Expedition IV; migration (queue slot); solo.
7. **controlapi scaffold + meta + golden tests (M1-19)** — after issue 5; first contract tests land here.
8. **v1 read endpoints + legacy wrappers (M1-20/23)** — after 7; includes automations endpoint per DM-3.
9. **SSE + Unix socket (M1-21/22)** — after 6 and 7.
10. **M1 exit-gate integration (M1-24)** — G-exit; last.

Issues 1–3 are the only ones that should carry `agent-ready` today.

---

*Unresolved disagreements to disclose: whether the durable `automations` table can really wait (DM-3 argues yes; the plan doc says land it in M1 — risk 8 is the counterweight), and whether the per-worker Unix socket should wait for Expedition II (this map says no; a topology-maximalist reading says yes). Both are called out with revisit triggers rather than settled silently.*
