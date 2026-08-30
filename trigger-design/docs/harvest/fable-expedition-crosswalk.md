---
title: "Fable expedition crosswalk — findings that inform the harvest"
status: accepted
date: 2026-08-29
tags: [harvest, research, events, runtime, topology]
source: agent-minder docs/research/fable-expedition/01–05
related: "[[event-log-store-first]], [[pr-triage-runtime-seam]], [[agent-runs-table]], [[daemon-api-pattern]], [[sqliteutil-wal-recovery]], [[0004-daemon-interface-split]]"
---

# Fable expedition crosswalk

Five Fable deep-review expeditions (Aug 2026) audited agent-minder's control-plane work.
They are the *why* behind much of the hardening in the harvested code, and in two areas
they are the design doc Trigger should follow, not just the code. Note: expeditions
I–IV reviewed schema v11–v12; the code has since landed the event log, v1 API, and SSE
they specified — so their proposals largely describe the *current* harvested code.

## Expedition I — Architecture truth map (01-architecture-truth-map.md)

Status audit + decision memos. What Trigger should absorb:

- **Provenance taxonomy** `source_type ∈ {explicit | watch | trigger | cron | manual}`
  written at *every* activation site — half-done at review time, since fixed. Confirms
  [[scheduler-tick-pattern]]'s provenance-dedup rule: name-prefix dedup was the bug.
- **C9 parity trap**: `CreateJob` and `BulkCreateJobs` silently diverged on columns.
  Any Trigger with two insert paths needs one shared column list or a test.
- **Config revision recording** (DM-3): no record of *which* config revision a process
  loaded (path, SHA-256, load time, validation error). Trigger's config loader
  ([[config-loader-pattern]]) should compute and surface this from day one.
- **Typed event envelope before persistence** (DM-2): the `{type string, summary string}`
  shape was the thing they refused to persist. The harvested `events` table is the
  post-envelope shape — vindicating it.
- **DM-5**: API handlers consume a Coordinator-owned provider interface, never the
  supervisor directly — the [[daemon-api-pattern]] shape.

## Expedition II — Worker/process topology ADR (02-…-adr.md)

Decision: **one Coordinator per OS process is the invariant**; future host
aggregator/manager holds zero Coordinators. For Trigger:

- **Runtime-contract clause I-4: "process-global equals deployment-global."** A runtime
  adapter may own a package-global resource (opencode's shared server) *only* because
  the process is the deployment. This is the missing *why* behind the opencode
  env-frozen-at-first-start quirk in [[opencode-runtime-adapter]] — it's a feature of
  the topology, not a wart.
- I-5: host-global resources (shared inference server, model cache) are external
  services with their own lifecycle — never runtime-owned children.
- Cross-process SQLite contract: WAL + `busy_timeout` across processes, single
  connection within one — the harvested [[sqliteutil-wal-recovery]] pool settings
  (`MaxOpenConns(1)`, idle recycle) are the in-process half of this two-level contract.
- Relevant to [[0004-daemon-interface-split]]: per-worker socket keyed by deploy-id;
  an aggregator (if ever built) is a stateless fan-in client, holds no cursors.

## Expedition III — Integration-target domain review (03-…md)

Mostly out of Trigger v1 scope (stacked-PR targets, auto-merge). Transferable patterns:

- **Resolve-and-pin-at-dispatch**: resolve a branch ref to a full SHA immediately
  before marking running, child worktree created from the SHA, HEAD verified after
  creation (`pin_mismatch` ⇒ failed). If Trigger jobs ever need branch pinning, this
  is the pattern.
- **Resolve outside the lock**: network work (fetch) must not run under the supervisor
  mutex — unlock → fetch → relock → re-check atomically. Load-bearing anywhere Trigger
  does I/O during scheduling.
- **Branch-ownership claim set**: any non-terminal status other than queued/blocked
  owns a branch (a `review`-status job's branch backs an open PR). Extends the
  branch-collision guard in [[stage-executor-pattern]].
- **Never silently fall back to default on parse/resolution failure** — the failure
  modes are `failed` and `blocked`, never silent default. Generalizes the
  hard-fail philosophy behind [[config-loader-pattern]].

## Expedition IV — Snapshot/event consistency contract (04-…md)

**This is the authoritative rationale for [[event-log-store-first]] — read it before
touching the event log.** The harvested code implements it. The rules (R-1–R-10),
identity model, and taxonomy:

- **Identity trio**: ID-1 durable cursor (AUTOINCREMENT id, only wire cursor), ID-2 log
  epoch (rotates only when history is destroyed — including WAL recovery, F6; the
  marker mechanism in [[sqliteutil-wal-recovery]] is this), ID-3 worker incarnation
  (process-lifetime; scopes *live-only* state like current-tool/step ticks). Collapsing
  them forces full resync on every restart.
- **Durable events vs ephemeral signals**: state transitions/decisions are persisted
  under commit-as-publish; progress telemetry (tool ring, step counts) is live-only,
  never persisted, rides the stream **id-less** (SSE `Last-Event-ID` untouched).
  Compaction of durable history is prohibited — fix chattiness by reclassifying.
- **R-2 ordering**: publish to live channels only after commit (a crash between commit
  and fan-out loses only promptness).
- **Snapshot protocol**: one read tx returns state + watermark + epoch + incarnation;
  snapshot-reflected-state ≡ events ≤ watermark, so the join is exact.
- **Policy asymmetry**: in-process subscribers get backpressure; network subscribers
  get disconnect-with-signal-and-resume. Every failure path terminates at snapshot
  resync; servers hold no per-client durable state.
- **Alternatives rejected with reasons** (§12): write-behind persistence (phantom
  cursors), full event sourcing (retention would corrupt state), per-deployment cursors
  (gap arithmetic unnecessary), incarnation-prefixed cursors (kills resume across
  restart). Don't re-derive these.

## Expedition V — Runtime conformance (05-…md)

The deepest source for the runtime seam ([[pr-triage-runtime-seam]]). Adds to the
pr-triage capability table:

| Capability | claude-code | codex | opencode |
|---|---|---|---|
| Resume sessions | yes (**needs workdir** — sessions per project dir) | yes | re-prompt, not continuation |
| Model selection | alias | alias | `provider/model`, **silently drops slash-less** |
| Tool allowlist | yes | **no** (sandbox only, network forced on) | yes |
| Enforces max turns / budget | CLI / CLI | adapter-cancels / **no** | **no** / **no** |
| Cost basis | exact | estimated (static table) | exact |
| Usage-limit signal | **structured** | keyword scan | keyword scan |
| Permission denials | yes | no | no |
| Shared process | no | no | **yes** |

- **Basis labels on the wire** (§8): every cost/turn/model field carries a companion
  basis (`exact|estimated|unavailable|runtime-defined`; `turn_basis` ∈
  `cli_turns|completed_turns|message_steps`). Never sum exact with estimated; never
  print a model the runtime didn't confirm; never compare turns across runtimes.
- **agent_runs columns spec** (§6, extends [[agent-runs-table]]): `model_requested`,
  `model_resolved` (**never written from config** — runtime-observed or null),
  `model_source` (which precedence rank won), `runtime_version`, `cost_basis`,
  `turn_basis`, `limits_enforced` (JSON). Warn on requested≠resolved mismatch. Every
  resume attempt gets its own row (`stop_reason = "resumed_from:<id>"`).
- **Model precedence** (§5): stage → agent frontmatter → job → deployment → repo →
  user → runtime default; resolve once per **stage**, never reuse an analyzer model for
  doers; empty means "runtime default", never "claude default". This is the
  [[config-resolve-once]] instance with the most layers.
- **Resume correctness trio** (all three adapters were broken): resume must carry
  workdir (claude stores sessions per project dir), env/credentials, and limits.
- **Multi-run logs are a live defect**: claude first-wins, codex accumulates, opencode
  last-wins — the only safe rule is one fresh log per run ([[pr-triage-runtime-seam]]).
  If appending is unavoidable, scan to the *last* result event.
- **Tool-input summaries need redaction, not just truncation** (80c truncation still
  passes tokens through verbatim); the tool ring must tolerate missing starts/ends and
  unrepresentable parallel calls (claude reports only the last of a parallel batch).

## Where this changes the harvest map's confidence

- Event log + sqliteutil: *raised* — the code matches an adversarially-reviewed design.
- Runtime seam: pr-triage's two adapters are proven but *narrower* than agent-minder's
  three-adapter conformance picture; Expedition V's capability table and basis-label
  rules are the more complete spec for Trigger's seam.
- Nothing in the expeditions contradicts the harvest map's "do NOT harvest" list — the
  review pipeline, dep graphs, and lessons remain out of scope.
