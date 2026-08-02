# Minder Control Plane and Operator UI Plan

Status: proposed
Last updated: 2026-08-01

## Summary

Minder will separate orchestration from presentation without replacing the
workflow that exists today.

`minder deploy --foreground` remains a first-class way to run Minder. It will
continue to load `.agent-minder/jobs.yaml`, evaluate triggers and cron
schedules, coordinate agents, enforce budgets, manage reviews and worktrees,
and print subscriptions and live events to the terminal. Neither a host server
nor a TUI will be required.

The implementation will formalize that behavior behind one reusable
per-deployment `Coordinator`. Foreground mode, the existing background daemon,
and a future managed server will all host the same Coordinator rather than
implementing scheduling or supervision separately.

A versioned control API will expose Coordinator state. A focused,
checkout-centered TUI will consume that API and may monitor several
deployments. Safe start, stop, retry, pause, and reload operations will be added
only after the read-only contract is proven.

## Goals

- Preserve all current CLI and foreground behavior.
- Keep `jobs.yaml` as the canonical desired configuration.
- Make the configuration actually loaded by a Coordinator observable.
- Provide a coherent, versioned API for deployments, automations, jobs, agent
  runs, activity, logs, usage, and deliverables.
- Allow one TUI to monitor one or many local or remote deployments.
- Retain and improve the existing checkout, log, issue, PR, worktree, and
  interactive-session workflows.
- Keep presentation replaceable so TUI and future browser experiments do not
  affect Coordinator behavior.
- Add operational controls only through explicit, scoped, auditable commands.

## Non-goals

- Requiring a TUI or host server to run Minder.
- Moving orchestration decisions into a UI.
- Replacing `jobs.yaml` with database- or UI-owned configuration.
- Rewriting the scheduler, supervisor, runtime boundary, or checkout behavior
  solely for presentation concerns.
- Shipping a browser UI in these three milestones.
- Making managed-server mode mandatory.

## Compatibility contract

The following behaviors are release gates throughout the project:

1. `minder deploy --foreground` can run without a host server or TUI.
2. Foreground mode continues to load and enforce `jobs.yaml`.
3. Foreground mode continues to print its subscription map and event stream.
4. Ctrl-C continues to stop the foreground deployment and its agents.
5. Existing commands including `deploy`, `status`, `jobs`, `logs`, `checkout`,
   `stop`, and `resume` remain supported.
6. Existing SQLite and JSONL data remain readable. Schema changes are additive
   migrations.
7. Existing HTTP routes remain compatibility wrappers while `/api/v1` is
   introduced.
8. Disconnecting, restarting, or replacing a client cannot change Coordinator
   behavior.

## Current architecture and seams

The current implementation already contains most of the required pieces, but
their ownership is fragmented:

- `runForeground` and `runDaemon` in `cmd/deploy.go` separately assemble the
  same scheduler, trigger routes, supervisor, signals, and optional API.
- `internal/daemon.Server` is optional, tied to one deployment ID, and receives
  SQLite plus a few callbacks rather than a full live-state provider.
- `Supervisor.RunningJobs()` already knows elapsed time, current tool, input
  summary, review state, and step count, but that data is not available through
  the API.
- SQLite is already host-wide and contains deployments and cross-deployment
  job history.
- PID and heartbeat files already provide a basis for local worker discovery.
- The checkout picker already queries repository history across deployments
  and provides the strongest operator workflow in the application.

Several correctness gaps must be addressed before an automation or activity
API can be authoritative:

- Trigger definitions are held in Supervisor memory while cron definitions are
  persisted.
- `milestone:*` trigger definitions validate but are not installed as trigger
  routes.
- Trigger-level budget and turn overrides are not carried into activated jobs.
- Schedule identity is global by name rather than scoped to a deployment, and
  replacement can discard last-run history.
- Removed definitions are not consistently disabled during reconciliation.
- Job activation provenance is not persisted.
- Multi-stage jobs do not durably record the actual agent, attempt, session,
  usage, and outcome for each stage.
- Current tool/input data is cleared after tool completion; no durable recent
  activity remains.
- Successful runtime final text, exact turns, and sessions are generally left
  in raw logs rather than normalized.
- PRs are partially modeled, but issues, comments, reports, summaries, and
  other deliverables are not.
- Supervisor events use a single bounded channel rather than a fan-out,
  resumable event stream.
- Log paths and follow behavior are inconsistent between local and remote
  clients.

## Target architecture

```text
                         standalone mode
jobs.yaml ──> Coordinator ───────────────> foreground stdout
                  │
                  ├── Scheduler
                  ├── Supervisor
                  ├── Agent runtimes
                  ├── SQLite / event / log stores
                  └── local versioned API socket

                         managed mode
minder server ──> deployment worker A ──> Coordinator A
              ├─> deployment worker B ──> Coordinator B
              └─> deployment worker C ──> Coordinator C
                    │
                    └── host-level aggregate API

CLI / TUI / SwiftBar / future web UI ──> API clients
```

The separation is between presentation and execution, not between
`jobs.yaml` and the component responsible for enforcing it.

### Coordinator

One Coordinator exists per active deployment and is the sole owner of:

- The loaded `jobs.yaml` snapshot.
- Trigger matching and cron scheduling.
- Job activation and provenance.
- Supervisor and runtime lifecycle.
- Budgets, reviews, dependencies, and worktrees.
- Live deployment snapshots and domain events.
- Validated configuration reload in the future.

Foreground and managed runners must instantiate the same Coordinator.

### Deployment worker

A deployment worker hosts one Coordinator. Initially this can remain the
current foreground or daemon process. Each worker exposes a permission-scoped
local Unix socket by default so local clients can observe it without requiring
public TCP or `--serve`.

Keeping managed deployments as child processes is preferred because it
preserves crash and resource isolation. This decision can be revisited without
changing the Coordinator or client contracts.

### Host server

The optional host server is a registry, lifecycle manager, and API gateway. It
does not contain a second scheduler or supervisor.

It discovers or registers deployment workers, combines their live snapshots
with host-wide durable history, and exposes collections across deployments.
Later it may start and stop managed worker processes.

### Clients

The TUI, existing CLI commands, SwiftBar, and any future browser UI are API
clients. They do not import Supervisor internals or independently interpret
`jobs.yaml`.

Client-local actions remain on the client. For example, the server returns
repository, remote, branch, runtime, session, log, and deliverable context;
`minder checkout` or the TUI creates a local worktree and launches the local
interactive runtime.

## Authoritative state

- `jobs.yaml` is canonical desired configuration.
- The Coordinator's loaded configuration snapshot is canonical active
  configuration.
- SQLite is canonical durable job and history state.
- Coordinator snapshots are canonical live state.
- Normalized domain events connect live and durable state.
- Raw runtime logs remain evidence and a debugging surface, not the UI data
  model.

The loaded configuration snapshot should include source path, SHA-256 revision,
load time, validation error, and whether the file on disk has drifted.

## Proposed durable model

Existing `jobs` rows remain the backward-compatible aggregate. New state should
be additive:

### Automations

- Identity: `(deployment_id, name)`.
- Source: `jobs.yaml`, CLI watch filter, or another future source.
- Kind: cron, trigger, or watch.
- Expression and normalized matcher.
- Effective agent, runtime, model, budget, and turn settings.
- Loaded/enabled/error state.
- Last evaluation, match, activation, and outcome.
- Next run for cron schedules.
- Configuration revision.

### Job provenance

Every job records:

- `source_type`: explicit deploy, watch, trigger, cron, or manual.
- `source_name`: automation name when applicable.
- `source_ref`: issue, labels, schedule time, or initiating command metadata.

This permits reliable automation-to-job navigation without inferring ancestry
from a job-name prefix.

### Agent runs

One job can involve several agents, stages, retries, or resumed sessions.
Persist one run/attempt record with:

- Job, stage, and attempt identity.
- Actual agent, runtime, model, and session.
- Start, completion, and last-activity timestamps.
- Status, stop reason, and failure details.
- Live step count versus exact final turns.
- Cost and effective limits.
- Current and recent sanitized activity.
- Final text and normalized outcome.
- Run-specific log identity.

### Job events

Persist typed, append-oriented events with a monotonic cursor, timestamp,
deployment/job/run identity, severity, summary, and structured data.

The event publisher must support fan-out so terminal rendering, persistence,
and API streaming receive the same events independently. A snapshot plus event
cursor lets clients reconnect without relying on uninterrupted delivery.

### Deliverables

Normalize deliverables as typed records:

- Pull request.
- Issue.
- Comment.
- Report.
- Review.
- Branch.
- Worktree.
- File.
- Final summary.

Each record may include a label, URL, path, reference, status, creation time,
and type-specific metadata. Server-local paths must be omitted or explicitly
marked unavailable to remote clients.

## Versioned API

The singleton deployment endpoint and host-level aggregate endpoint should
return the same resource shapes.

Initial read API:

```text
GET /api/v1/meta
GET /api/v1/deployments
GET /api/v1/deployments/{deployment_id}
GET /api/v1/deployments/{deployment_id}/automations
GET /api/v1/deployments/{deployment_id}/jobs
GET /api/v1/deployments/{deployment_id}/jobs/{job_id}
GET /api/v1/deployments/{deployment_id}/jobs/{job_id}/runs
GET /api/v1/deployments/{deployment_id}/jobs/{job_id}/deliverables
GET /api/v1/deployments/{deployment_id}/jobs/{job_id}/logs
GET /api/v1/events
```

Collection endpoints use stable envelopes and cursor pagination. Event
streaming uses SSE with event IDs, heartbeats, `after`/`Last-Event-ID` resume,
and snapshot resynchronization.

The contract source of truth is Go types in `internal/controlapi` with
`encoding/json` tags, guarded by golden-JSON tests (see Resolved decisions). An
OpenAPI document is generated from those types only when an out-of-repo consumer
(e.g. a future browser UI) needs it.

`/api/v1/meta` advertises version, instance identity, mode, and capabilities so
clients can degrade cleanly when attached to older servers.

Future mutation API:

```text
POST /api/v1/deployments/{deployment_id}/automations/{name}:run
POST /api/v1/deployments/{deployment_id}/jobs/{job_id}:stop
POST /api/v1/deployments/{deployment_id}/jobs/{job_id}:retry
POST /api/v1/deployments/{deployment_id}:pause
POST /api/v1/deployments/{deployment_id}:resume
POST /api/v1/deployments/{deployment_id}:reload
POST /api/v1/deployments/{deployment_id}:stop
```

Mutation endpoints are not required by the first TUI. They require explicit
scope, authorization, capability checks, idempotency keys, command-status
events, and an audit trail.

All v1 job and log lookups must verify deployment ownership. TCP remains
opt-in, remote authentication is required, and browser support must not retain
the current permissive cross-origin policy.

## TUI experience

`minder tui` is an API-only client with three primary scopes:

- All deployments on a connected host.
- One repository across deployments.
- One deployment.

It may later aggregate named endpoints such as local, workstation, and VPS.
Stable client identity is therefore `(server_id, deployment_id, resource_id)`.

Primary views:

1. **Overview** — host/deployment health, capacity, budget, last trigger poll,
   next cron, active agents, attention items, and recent deliverables.
2. **Automations** — active triggers, watches, and cron jobs; loaded revision,
   drift/error state, effective agent/runtime/limits, last/next activation, and
   linked active or recent jobs.
3. **Runs** — the existing checkout history evolved into a live,
   cross-deployment master-detail view.
4. **Run detail** — activation source, stage/agent timeline, current and recent
   activity, usage, logs, results, reviews, failure information, and
   deliverables.

The UI should display live assistant-message counts as steps, not true turns.
Exact turns and cost are shown when reported by the runtime; estimates are
labeled as estimates.

The existing Enter-driven checkout action menu remains the discoverable
interaction. Contextual shortcuts may supplement it for logs, worktree,
interactive continuation, issue, PR, copying, and filtering.

`minder checkout` remains available as the fast non-TUI path. Checkout,
session launch, URL opening, and log formatting should move into shared
client-side services so CLI and TUI behavior cannot diverge.

Any worktree recreation action must check for dirty or unpushed work and
require explicit confirmation before force removal or branch replacement.

## Proposed package boundaries

Names are provisional, but responsibilities should remain separate:

- `internal/coordinator` — construct, run, reload, snapshot, and stop one
  deployment Coordinator.
- `internal/controlapi` — transport-neutral v1 resources, handlers, clients,
  capabilities, and legacy adapters.
- `internal/eventbus` — fan-out, persistence, cursor, and subscription
  semantics.
- `internal/registry` — local deployment discovery and host aggregation.
- `internal/checkout` — local worktree, interactive-session, URL, and
  capability-aware action services shared by CLI and TUI.
- `internal/tui` — API-only presentation and interaction.

The server must not import TUI packages or expose pre-rendered rows, colors, or
keybinding concepts.

## Milestones

### [M1 — Coordinator & Observability](https://github.com/aptx-health/agent-minder/milestone/20)

Formalize the shared Coordinator, correct automation state, introduce durable
run/activity/deliverable data, and expose a versioned read contract while
preserving foreground behavior.

Major workstreams:

- Characterize current foreground and daemon behavior with compatibility tests.
- Extract duplicated foreground/daemon assembly behind Coordinator lifecycle.
- Reconcile and expose the loaded automation snapshot.
- Correct milestone triggers, trigger overrides, schedule scoping/history, and
  removal semantics.
- Add job provenance, agent-run identity, event fan-out, recent activity,
  normalized final usage/results, deliverables, and consistent log identity.
- Add the `/api/v1` singleton deployment surface and local Unix socket.
- Preserve old routes, commands, SQLite, and JSONL compatibility.

Exit gate: an external read-only client can accurately observe a running
Coordinator, and `minder deploy --foreground` behaves as it does today without
requiring another process.

### [M2 — Multi-Deployment TUI](https://github.com/aptx-health/agent-minder/milestone/21)

Build the checkout-centered API-only TUI and host-level read aggregation.

Major workstreams:

- Discover and select hosts, repositories, and deployments.
- Aggregate registered or local deployment workers without reimplementing
  coordination.
- Build Overview, Automations, Runs, and Run Detail views.
- Add resumable live activity and formatted log following.
- Show active stage/agent, recent command/tool activity, usage/limits, results,
  and typed deliverables.
- Extract and reuse checkout/session/log/URL services.
- Preserve `minder checkout` and make local/remote capabilities explicit.
- Cover disconnected, degraded, empty, narrow-terminal, and multi-deployment
  states.

Exit gate: one replaceable TUI process can monitor at least two concurrent
deployments, including a foreground deployment, without affecting either
Coordinator.

### [M3 — Safe Operations](https://github.com/aptx-health/agent-minder/milestone/22)

Add guarded, auditable operations and managed deployment lifecycle after the
read-only model is stable.

Major workstreams:

- Run an automation now.
- Pause and resume dispatch.
- Stop or retry a job or agent.
- Resume or explicitly extend a budget.
- Validate and reload `jobs.yaml` with diff/error reporting.
- Start, stop, restart, recover, and reconcile managed deployment workers.
- Add authorization, capability negotiation, idempotency, command state,
  audit events, and failure-injection tests.
- Add TUI confirmations with clear target and impact summaries.

Exit gate: supported commands reach only the intended Coordinator, recover
cleanly across disconnects and restarts, and never make managed mode mandatory.

## Testing strategy

- Characterization tests for current foreground and daemon behavior before
  extraction.
- Unit tests for Coordinator lifecycle and automation reconciliation.
- API schema/golden and backward-compatibility tests.
- Authorization and deployment-scoping tests for every resource.
- Snapshot/event ordering, fan-out, reconnect, retention, and backpressure
  tests.
- Integration tests with at least two simultaneous Coordinators.
- Runtime fixtures covering Claude Code, Codex, and OpenCode activity/results.
- Log tail, rotation, redaction, offset, and reconnect tests.
- Pure TUI model tests for navigation, resize, filtering, disconnect, and
  capability degradation.
- End-to-end checkout tests with dirty and unpushed-work protection.
- Command idempotency, cancellation, crash recovery, and failure-injection
  tests before controls are enabled.

## Rollout

1. Land additive models, compatibility tests, and read APIs behind no UI
   requirement.
2. Allow current CLI commands and SwiftBar to adopt v1 incrementally.
3. Ship the TUI as an optional client.
4. Observe the read path before enabling mutation endpoints.
5. Add managed lifecycle without removing standalone foreground mode.
6. Consider a browser UI only after authentication, scoping, and command
   semantics are mature.

## Resolved decisions

- **API contract source of truth: Go types, not OpenAPI (2026-08-01).** The v1
  contract lives as `internal/controlapi` structs with `encoding/json` tags,
  guarded by golden-JSON tests. Every M1 client (foreground stdout, CLI, TUI) is
  in-repo Go, so hand-maintained OpenAPI would be sync overhead with no consumer;
  `/api/v1/meta` capability advertising covers version negotiation. OpenAPI is
  generated from the Go types later, only if an out-of-repo consumer (browser UI)
  appears. This defers `/api/v1` golden contract tests until M1-19 lands the
  envelope, rather than pinning them during the correctness/refactor waves.

## Open decisions

- Whether managed deployment workers remain child processes permanently or may
  optionally run in-process.
- Event and log retention limits.
- Remote endpoint discovery and credential/profile storage.
- Redaction rules for command/tool summaries and raw logs.
- The boundary between exact usage, runtime-provided estimates, and
  Minder-computed estimates.
- Whether a future browser UI is embedded in the host server or shipped
  separately.
