# Expedition IV — Snapshot and Event Consistency Contract

Status: complete (research decision; implementation deferred to the typed-envelope, M1-18, M1-19, and M1-21 issues)
Reviewed baseline: `origin/main` @ `b96d79da0b89fe98813a1dd34e9ef02051207e19` (merge of PR #596, 2026-08-03)
Consumes: `01-architecture-truth-map.md` (Expedition I — DM-2, DM-4, risks 1/4/9, invariant §6.5) and `02-worker-process-topology-adr.md` (Expedition II — I-1, I-7, I-8, and the §8 event-write-contention revisit trigger).
Evidence reviewed: `internal/eventbus`, `internal/supervisor` (`supervisor.go`, `jobmanager.go`, `watch.go`), `cmd/deploy.go`, `internal/daemon/server.go`, `internal/db/schema.go`, `internal/sqliteutil/recover.go`, issue #584, PR #587, `docs/control-plane-and-tui-plan.md`, `docs/control-plane-milestone-breakdown.md`.

Claims are cited to file:line at the reviewed SHA. Statements derived by reading code paths rather than executing them are marked *(inference)*.

---

## 1. Verdict and the decision in one paragraph

**The current bus is a sound foundation for exactly one of the three jobs people will ask of it, and it must be demoted before it acquires the other two.** As an in-process fan-out seam it is correct: monotonic cursor, gap-free missed-then-live resume, per-subscriber backpressure with no silent drops (`internal/eventbus/eventbus.go:84-109`, PR #587). But its cursor is process-lifetime (`cursor = len(events)+1`, `eventbus.go:65`) and its log is unbounded memory (`eventbus.go:38,66`), so it can be neither the wire contract nor the replay authority. The contract this expedition fixes: **durable events are appended to SQLite in the same transaction as the state change they describe, the durable row id is the only cursor that ever crosses the process boundary, and the in-memory bus becomes the live-tail delivery mechanism under that log.** Snapshots are single read transactions that return state plus the log watermark; clients reconcile via snapshot-then-resume; every loss is explicit and every recovery path ends at snapshot resync. Live-only state (current tool, step counts, budget pause) is advisory, keyed to a per-process incarnation id, and never replayed.

---

## 2. Current truth

What exists at the reviewed SHA, because every rule in §4 exists to fix or preserve something on this list:

- **One producer, one payload shape.** `Supervisor.emitEvent` publishes `Event{Time, Type string, Summary string, TaskID}` (`internal/supervisor/supervisor.go:53-59,487-492`). Publish errors are swallowed to the debug log (`supervisor.go:489-491`). The doc comment enumerates eight type strings; the code emits at least twelve — `waiting` (`jobmanager.go:955`), `manual` (`jobmanager.go:1190`), `spared` (`supervisor.go:898`), and `reaped` (`supervisor.go:908`) are undocumented. The vocabulary is open and presentation-shaped (Expedition I, C10).
- **No ordering discipline between DB writes and event publishes.** `waitForUsageLimitReset` emits `waiting` *before* `UpdateJobStatus(StatusWaiting)` (`jobmanager.go:955-959`); review start emits *before* the status write (`jobmanager.go:992-993`); pipeline completion writes status *then* emits (`jobmanager.go:1084-1085`); user-stop writes *then* emits (`jobmanager.go:567-568`). Both orderings ship today. Nothing anywhere ties an event to the transaction it describes.
- **One production consumer.** Foreground stdout subscribes from cursor 0 and renders `[HH:MM:SS] type: summary` (`cmd/deploy.go:377-387`); the anchors pin this output. `DrainEvents` is a test-harness accessor (`jobmanager.go:1297-1310`). The daemon HTTP server holds no supervisor or bus reference at all — only three callbacks (`internal/daemon/server.go:27-31`); no API client can see an event today.
- **Snapshots have no watermark.** `GET /status` and `GET /jobs` are plain DB reads plus callback probes (`server.go:112-162`); a client cannot know which events its snapshot already reflects.
- **No incarnation identity.** Daemon restart reuses the deployment row (`cmd/deploy.go:422-425`); `deployments.started_at` is set at creation (`internal/db/schema.go:42`); the bus restarts its cursor at 1. A client holding cursor N across a worker restart and calling `Subscribe(N)` either gets `ErrCursorAhead` (new bus shorter) or — worse — **silently receives unrelated events** (new bus already past N). The in-memory cursor is not merely non-durable; it is ambiguous across restarts.
- **The transport that exists would kill SSE.** The daemon server sets `WriteTimeout: 30s` (`server.go:66`), which terminates any long-lived streaming response. M1-21 cannot reuse this server config as-is.
- **SQLite is the shared substrate and its recovery can eat history.** Host-wide DB, WAL + `busy_timeout(5000)` across processes, one connection in-process (`schema.go:438-445`). `sqliteutil.CheckAndRecover` deletes `-wal`/`-shm` files when health checks fail (`internal/sqliteutil/recover.go:15-72`), which can discard committed-but-uncheckpointed transactions — including, once M1-18 lands, the tail of the durable event log *(inference)*.

---

## 3. Identity model

Three identities, each answering one of the issue's questions:

**ID-1: Durable cursor.** The `events` table id — `INTEGER PRIMARY KEY AUTOINCREMENT`, matching every existing table (`schema.go:46,165`) — is the event's identity and the only cursor exposed to clients. `AUTOINCREMENT` is load-bearing, not house style: retention deletes the newest-free rowids back into circulation without it, and a reused cursor id is a silent-corruption bug for every client holding the old one. Host-global and monotonic across worker restarts, so `Last-Event-ID` resume works across restarts with no epoch arithmetic.

**ID-2: Log epoch.** A random identifier created when the event table is first initialized and stored beside it, identifying *this history*. It changes only when history is destroyed: DB deleted or replaced, `MINDER_DB` pointed elsewhere, WAL recovery truncation detected. Advertised in `/api/v1/meta` and the SSE stream preamble. A client whose stored epoch mismatches discards its cursor and resyncs. This is the guard against the "cursor from a different universe" failure that no amount of monotonicity fixes.

**ID-3: Worker incarnation.** A random identifier generated at Coordinator construction, process-lifetime, advertised in `/meta` and the stream preamble. Not persisted, no migration. It scopes everything that dies with the process: live run info (`RunInfo.CurrentTool` is cleared on tool completion, `internal/supervisor/runtime.go:65`), budget-pause state, ephemeral signals (§5). An incarnation change tells a client its *live* picture is stale even though its durable cursor is still valid.

The split matters: durable resume survives restart because of ID-1/ID-2; restart detection for live state needs ID-3. Collapsing them (e.g., prefixing every event id with the incarnation) would force full resync on every worker restart for no benefit.

---

## 4. Delivery and ordering guarantees

Normative rules. Violating any is a bug, not a tradeoff. They extend Expedition I invariant §6.5 ("events are never silently dropped").

- **R-1 (atomic append).** A durable event describing a SQLite state change is inserted in the same transaction as that change. Commit is the publish: before commit the event does not exist; after commit it is visible to snapshot readers and eligible for live delivery. This is the atomic boundary between a snapshot and its watermark — both are views of the same committed log.
- **R-2 (publish after commit).** Live fan-out (bus publish, SSE write, stdout render) of a durable event happens only after its transaction commits. The pre-write emissions in today's code (`jobmanager.go:955-959,992-993`) must migrate to this ordering when the envelope lands. A crash between commit and fan-out loses only *promptness*; the event is recovered by resume or snapshot. A crash before commit loses the event *and* the state change together — consistent by construction.
- **R-3 (cursor discipline).** The in-memory bus cursor never crosses a process or API boundary. Wire cursors are durable event ids only. Corollary: SSE (M1-21) cannot ship before durable events (M1-18), and no "temporary" SSE over the in-memory cursor is permitted — the restart ambiguity in §2 is why.
- **R-4 (per-deployment total order).** Events for one deployment are totally ordered by id, and every consumer — in-process, SSE, aggregator — observes them in that order. The host-global id gives cross-deployment interleaving within one host DB for free; no stronger aggregate guarantee is offered, and none is claimed across hosts (M2 aggregator merges per-worker streams; ordering between workers is arrival order, explicitly unspecified).
- **R-5 (in-connection losslessness, cross-connection at-least-once).** Within one subscription or SSE connection: exactly-once, in-order, no gaps. Across reconnects: at-least-once — a client that processed events but failed to persist its cursor will see them again; consumers must treat events idempotently keyed by id. Duplicates are permitted only at reconnect seams; loss is never silent (R-7).
- **R-6 (gaps are impossible or explicit, never inferred).** Clients do not detect loss by id arithmetic — a deployment-filtered stream has holes in the global id sequence by design. Loss surfaces exactly one way: the server refusing a resume cursor (below retention floor, above current head, or wrong epoch) with an explicit resync-required signal. Heartbeats (R-8) bound how long a dead connection can masquerade as a quiet one; they carry no ids and update no cursors.
- **R-7 (explicit truncation).** Retention advances a durable floor. Resume at-or-below the floor fails with a typed signal whose documented client response is snapshot-then-resubscribe-from-watermark. This is DM-4 extended from the memory log to the durable log: the "never silently drop" invariant survives retention by making loss loud and recoverable.
- **R-8 (bounded liveness).** The live stream carries periodic heartbeats (SSE comment lines; no event id) so clients can distinguish idle from dead. Heartbeat interval and client patience are M1-21 parameters, not contract; their *existence* is contract.
- **R-9 (scoped replay).** Every event row carries deployment identity (and job/run identity where applicable). Replay applies exactly the same scoping filter as live delivery. A worker serves only its own deployment's events even though the table is host-global — the unscoped-`GetJob` leak (Expedition I EC2, L0.4) must not be reproduced in the event path, where replay would leak *history*, not just current rows.
- **R-10 (events are facts about committed state; external effects reconcile via snapshot).** GitHub-side effects (PR opened, comment posted) are not transactional with SQLite and never can be. The durable event describes the *recorded* outcome (the status write, the stored PR number), not the external act. Drift between GitHub and the DB is reconciled by existing polling, not by the event stream. This keeps R-1 honest: nothing outside SQLite is inside the atomic boundary.

---

## 5. Event taxonomy: durable events vs ephemeral signals

Two classes, decided here because every retention and replay question collapses without the split:

**Durable events** — state transitions and decisions: job status changes, stage start/finish, run begin/end, review verdicts, budget pause/resume, discovery of new work, bail/failure records. Persisted under R-1, replayable, carry ids, participate in resume. Field minimum per Expedition I DM-2: id (cursor), timestamp, deployment/job/run identity, closed type enum, severity, human summary, structured `data` JSON. The twelve-plus ad-hoc strings in §2 get audited into the enum when the envelope lands; the audit is bookkeeping, not architecture.

**Ephemeral signals** — progress telemetry: current tool, tool input, step-count ticks, throttled offline/online chatter. Live-only: never persisted, never replayed, delivered best-effort on the live stream **without SSE ids** (the SSE spec makes id-less events leave `Last-Event-ID` untouched — the mechanism exists precisely for this). Scoped by incarnation (ID-3): after a worker restart they restart from nothing, and that is correct because the state they described died with the process.

Two consequences worth stating:

1. **Compaction is a classification error.** The durable log is append-only and immutable; there is no rewriting, merging, or summarizing of history. If a durable event type proves too chatty to retain, the fix is reclassifying it as ephemeral (or emitting it less), never compacting stored rows. "Compaction" in this system means only retention deletion under R-7.
2. **The write-contention escape hatch stays open.** Expedition I risk 9 and Expedition II §8 worry about event writes starving worker DB access. The taxonomy is the first relief valve (chatty telemetry never touches the DB); transaction piggybacking under R-1 is the second (a status-change event adds a row to a transaction that already exists, not a new commit); batching independent durable events into group commits is the third and is compatible with R-2 (delivery waits for commit; commit may cover several events). Process merger remains prohibited (Expedition II).

---

## 6. Snapshot/watermark protocol

A snapshot is a single SQLite read transaction that returns:

- the requested state (deployment, jobs, runs, automations — whatever the endpoint serves),
- the **watermark W** = max durable event id visible in that transaction,
- the log epoch (ID-2) and worker incarnation (ID-3).

Under R-1, this is the whole protocol: because events commit atomically with the state they describe, any transaction's state view reflects *exactly* the events ≤ its W — no fencing, no locks, no coordination beyond the transaction SQLite already provides. WAL gives readers a stable point-in-time view while the single writer proceeds (`schema.go:438-445`).

Client join sequence: fetch snapshot → record (epoch, incarnation, W) → subscribe with `after=W` → apply events. The seam is exact: nothing missed, nothing double-applied, because "reflected in snapshot" and "id ≤ W" are the same predicate. Live-only fields inside a snapshot (current tool, elapsed, step count) are advisory, incarnation-scoped, and carry no watermark semantics — the next ephemeral signal or snapshot refresh supersedes them.

Foreground stdout needs none of this: it is in-process, subscribes from bus start, and predates any durable log; it keeps its current semantics (§9, compat C-1).

---

## 7. State machines

**Server, per subscription/connection:**

```
CATCHING_UP --(replayed to head)--> LIVE --(buffer overflow / write stall)--> DROPPED(resync signal)
     |                                |--(heartbeat write fails / deadline)--> REAPED
     |--(cursor below floor at subscribe)--> REJECTED(resync signal)
     |--(cursor above head, or epoch mismatch)--> REJECTED(resync signal)
LIVE --(client disconnect)--> CLOSED
any --(worker shutdown)--> drain-then-close (bus Close semantics, eventbus.go:126-138)
```

The server holds no per-client durable state: a subscription is (filter, position, buffer) and dies with the connection. All resume state lives client-side. This is what keeps the aggregator (M2) a stateless pass-through and the worker restart-cheap.

**Client:**

```
DISCONNECTED --> SNAPSHOT_FETCH --(got state, W, epoch, incarnation)--> REPLAYING(after=W) --> LIVE
LIVE --(connection lost)--> RESUMING(Last-Event-ID) --(accepted)--> LIVE
RESUMING --(resync signal: floor/epoch/ahead)--> SNAPSHOT_FETCH        [discard cursor if epoch changed]
LIVE --(incarnation changed in preamble)--> refresh live-state via SNAPSHOT_FETCH, durable cursor kept
LIVE --(no event or heartbeat within patience)--> RESUMING
```

The invariant shape: **every failure path terminates at SNAPSHOT_FETCH**, and SNAPSHOT_FETCH is always safe (idempotent read). There is no client state that cannot be rebuilt from one snapshot plus one subscription; any client design that accumulates state not recoverable this way is wrong.

---

## 8. Restart, retention, and gap behavior

| Situation | What survives | Client experience |
|---|---|---|
| Worker restart (clean or crash) | Durable log, ids, epoch | Connection drops; resume by `Last-Event-ID` succeeds (ids are durable and host-global); new incarnation in preamble tells the client to refresh live state. In-flight-job cleanup emits durable events via existing crash recovery (`TransitionStaleRunningJobs`, `cmd/deploy.go:415-420`) so the log records the transition *(inference: recovery currently writes status only; the envelope work adds the paired event under R-1)* |
| Retention deletion | Log above the floor | Resume below floor → explicit resync signal (R-7) → snapshot-then-resubscribe. Snapshot state is complete regardless of how much log was deleted — state tables are not derived from the log (§12, alternative B) |
| DB reset / replacement / WAL-recovery truncation | Nothing (new epoch) | Epoch mismatch → discard cursor → snapshot resync. The WAL-recovery path (`recover.go:15-72`) must be treated as potential truncation: verification of the log tail after recovery, and an epoch rotation if the tail regressed, is part of the M1-18 acceptance gates |
| In-memory bus loss (always, at restart) | Nothing — by design | Invisible: no client ever held its cursor (R-3) |
| Aggregator restart (M2) | Nothing server-side | Clients hold per-worker cursors; aggregator reattaches and passes through. The aggregator persists no cursor state of its own |

Pre-M1-18 interim: until durable events exist there is no wire event stream at all (R-3 forbids the stopgap). The in-memory bus keeps serving foreground stdout exactly as today. DM-4's retention cap on the memory log remains required independent of this contract — the unbounded `b.events` ships in every long-lived daemon now (Expedition I risk 4). After M1-18, the memory log's role shrinks to live-tail buffering and its retention becomes an invisible implementation detail under R-3.

---

## 9. Slow and abandoned subscribers

**In-process (today and after):** the current policy is correct and kept — bounded per-subscriber channel, backpressure isolated to the lagging subscriber's delivery goroutine, others unaffected (`eventbus.go:31-35,171-198`). One repair: when DM-4's retention cap truncates the memory log beneath a lagging subscription, that subscription must terminate with a typed truncation error, not stall — uniform with R-7. An abandoned subscription (reader gone, `Close` never called) currently parks a goroutine forever on channel send (`eventbus.go:182-184`); with truncation-on-cap it instead dies the first time retention passes it *(inference)*. Foreground stdout, the only production in-process consumer, is fast and unaffected.

**SSE connections (M1-21):** per-connection bounded send buffer. On overflow or write stall, drop the connection with a resync signal — never block the fan-out path, never skip events silently within a connection (R-5/R-6). Dropping is safe *because* resume is lossless: the client re-enters at RESUMING with its last id. Abandoned connections are reaped by heartbeat write failures and write deadlines; the existing blanket `WriteTimeout: 30s` (`server.go:66`) must be replaced by per-write deadlines on the streaming route (mechanism left to M1-21).

**Policy asymmetry, stated deliberately:** in-process subscribers get backpressure (they are trusted, few, and part of the program); network subscribers get disconnect-and-resume (they are untrusted, many, and own their cursors). Both end at the same place — explicit signal, snapshot resync — so there is one recovery story, not two.

---

## 10. Failure matrix

| # | Failure | Detected by | Guarantee that holds | Recovery |
|---|---|---|---|---|
| F1 | Worker crash between state commit and live fan-out | Client: connection drop | Event exists durably (R-1); only promptness lost | Resume from last id delivers it |
| F2 | Worker crash before commit | — | Neither state nor event exists; consistent | Nothing to recover |
| F3 | Client crash after applying events, before persisting cursor | Client restart | At-least-once (R-5) | Re-delivery; idempotent apply by id |
| F4 | Resume cursor below retention floor | Server check at subscribe | Explicit loss (R-7) | Snapshot resync |
| F5 | Resume cursor above head (client from the future: DB reset behind an epoch the client missed) | Server check | Explicit rejection (R-6) | Epoch check → discard cursor → snapshot resync |
| F6 | Epoch mismatch (DB replaced, WAL truncation) | Preamble / meta comparison | History discontinuity is explicit (ID-2) | Discard cursor, snapshot resync |
| F7 | Worker restart mid-connection | Drop + new incarnation | Durable cursor still valid (ID-1) | Resume; refresh live state (ID-3) |
| F8 | Slow SSE consumer | Server buffer overflow | No fan-out stall, no silent skip | Disconnect with signal; client resumes |
| F9 | Silent network death | Heartbeat absence (R-8) | Bounded staleness | Client re-enters RESUMING |
| F10 | Two workers, one DB, interleaved writes | — | Per-deployment total order (R-4); ids allocated at commit by the shared table *(inference: AUTOINCREMENT allocation under WAL serializes on the write lock)* | None needed |
| F11 | Event-write contention starves job writes (Exp I risk 9) | Measurement, not failure | Taxonomy + piggybacking + group commit (§5) before any architecture change | Expedition II §8 escape sequence; process merger prohibited |
| F12 | Clock skew / non-monotonic wall clock | — | Ordering is id-based, never time-based; timestamps are display data | None needed |
| F13 | Aggregator crash (M2) | Client connection drop | Workers unaffected (Exp II I-3); no server-side cursor state to lose (§7) | Reconnect, per-worker resume |

---

## 11. Compatibility requirements

- **C-1 (foreground stdout).** Byte-stable per the green anchors: the typed envelope must render to today's `[HH:MM:SS] type: summary` line (`cmd/deploy.go:385`), which constrains the envelope to carry the legacy type string and summary verbatim (or derivable losslessly). The anchors are the enforcement; if an anchor fails, the envelope change is wrong.
- **C-2 (in-process bus API).** `Subscribe(afterCursor)` missed-then-live, gap-free-at-the-seam semantics are preserved for in-process consumers (`eventbus.go:81-109`); `DrainEvents` keeps its test-harness contract (`jobmanager.go:1297-1310`). The bus's generic type and package API are internal and may evolve freely under R-3.
- **C-3 (legacy HTTP surface).** Untouched. No legacy route grows event or watermark fields; `/api/v1` is additive (Expedition I invariant §6.8).
- **C-4 (persistence discipline).** The events table is one migration in the v12+ single-file queue: additive, `AUTOINCREMENT` id, deployment/job/run identity columns, one `schemaVersion` bump, `TestClaudeMDSchemaVersion` updated. Single-writer discipline (`SetMaxOpenConns(1)`, `schema.go:445`) is unchanged by this contract; R-1 rides existing transactions rather than adding coordination.
- **C-5 (API clients).** v1 snapshot endpoints carry `(watermark, log_epoch, incarnation)` from birth — retrofitting watermarks after golden-JSON tests pin the envelope is a breaking change, which is precisely the trap DM-2 flags for events themselves.
- **C-6 (topology).** Nothing here requires or permits cross-worker communication beyond the shared DB (Expedition II I-7). The event table is read via each worker's own API scoped by R-9; the aggregator consumes worker APIs, never the table directly.

---

## 12. Alternatives considered

**A. Write-behind persistence (bus authoritative, DB as subscriber)** — the reading of "M1-18: persist typed events *onto the bus*" (`control-plane-milestone-breakdown.md:115`) closest to the current code. Rejected: a crash between publish and persist creates phantom cursors — events clients saw but history never recorded — making duplicate *and* lost delivery both possible at the same seam, and making the snapshot/watermark boundary unbuildable (the snapshot transaction cannot see an unpersisted event that live clients already consumed). Store-first inverts one arrow and dissolves the entire failure class. Confidence: high. Revisit: none — this is the load-bearing decision.

**B. Full event sourcing (state derived from the log)** — makes snapshot-vs-log consistency true by construction. Rejected: inverts the ownership model of eleven schema versions (`jobs`/`agent_runs` are the truth today, Expedition I §1), requires replay machinery and log completeness guarantees far beyond observability needs, and turns retention deletion into state loss (F4 would corrupt rather than inconvenience). The chosen model keeps state tables authoritative and the log as *history about* them — retention can delete history without touching truth. Confidence: high. Revisit: only if a future feature needs time-travel reconstruction, which nothing planned does.

**C. Per-deployment cursor sequences (instead of one host-global id)** — gives contiguous per-stream ids, enabling client-side gap arithmetic. Rejected: requires per-deployment sequence allocation (a counters table or MAX+1 under the write lock) instead of free `AUTOINCREMENT`; breaks the aggregate host stream's natural interleaving; and buys a gap-detection mechanism that R-6 makes unnecessary (loss is server-signaled, never client-inferred). Confidence: medium-high. Revisit: if M2's aggregator needs per-stream contiguity for a merge algorithm — no current design does.

**D. Incarnation-prefixed wire cursors (`incarnation:cursor`), no durable ids** — shippable before M1-18. Rejected: forces full snapshot resync on every worker restart forever, makes `Last-Event-ID` resume worthless across the most common discontinuity, and would freeze into the SSE golden tests as exactly the kind of contract DM-2 warns about. The durable id costs one migration already planned as M1-18. Confidence: high. Revisit: none.

**E. Replace the eventbus package** — considered because its role shrinks. Rejected: its subscription semantics are exactly right for the live tail and foreground stdout, its concurrency is tested (PR #587), and demotion under R-3 requires no interface change. The bus is kept, capped (DM-4), and repositioned. Confidence: high.

---

## 13. Testable invariants and verification suite outline

Invariants (each maps to at least one test below):

- **V-1.** For any snapshot transaction: every durable event with id ≤ W is reflected in the returned state; no event with id > W is. (R-1 + §6)
- **V-2.** No durable event is observable on any live channel before its transaction commits. (R-2)
- **V-3.** No wire response, SSE id, or persisted client artifact ever contains an in-memory bus cursor. (R-3)
- **V-4.** Any single consumer sees one deployment's events in strictly increasing id order, exactly once per connection, at-least-once across its lifetime. (R-4/R-5)
- **V-5.** Every loss path — floor, epoch, ahead-of-head, overflow disconnect — produces an explicit machine-readable signal, and snapshot-then-resubscribe from that signal reaches LIVE with a state equal to continuous observation. (R-6/R-7, §7)
- **V-6.** Retention never reuses an id and never mutates a surviving row. (ID-1, §5)
- **V-7.** Worker restart preserves resumability: a cursor taken before SIGTERM resumes correctly after restart; the incarnation visibly changes; live-state fields reset. (ID-1/ID-3, F7)
- **V-8.** Foreground stdout is byte-identical before and after the envelope/persistence work. (C-1; the existing anchors *are* this test)

Verification suite outline (cheaper-agent executable; the harness patterns already exist — `t.TempDir()` + real SQLite, `TestHooks` lifecycle driving, `httptest` black-box):

1. **Unit — envelope:** render typed envelope → legacy stdout line, golden-compared (V-8 support); enum covers the twelve observed type strings (§2 audit).
2. **Unit — store:** append-in-transaction visibility (V-1, V-2 at the SQL level); AUTOINCREMENT non-reuse after deletion (V-6); floor query correctness.
3. **Concurrency — bus (extend existing `eventbus_test.go`):** DM-4 cap → lagging subscription gets typed truncation, others unaffected; abandoned subscription is reaped by truncation rather than leaking its goroutine (§9).
4. **Integration — snapshot/watermark:** drive a job lifecycle via `TestHooks` while a reader loops snapshot-then-replay; assert V-1 at every observed W. This is the single highest-value test in the suite.
5. **Integration — resume seams:** kill and resume a subscriber at every state in §7's client machine, including cursor-persisted-late (F3 duplicate handling) and restart-mid-stream (V-7, two worker processes per Expedition II I-6's carve-out rules).
6. **Integration — scoping:** two deployments, one DB: each worker's event replay and live stream contain only its own deployment's events (R-9; extends G-api-scope).
7. **Failure injection:** delete the WAL mid-test to force `CheckAndRecover`, assert epoch rotation or tail-verification behavior (F6); saturate an SSE client, assert disconnect-signal-resume-equivalence (V-5, F8).
8. **Byte-neutrality:** anchors, run unmodified (V-8).

---

## 14. Cheaper-agent handoff

### Fixed decisions (do not re-litigate)

- Store-first: durable append is the publish; commit is the visibility boundary (R-1/R-2); the bus is the live tail, kept and capped, never the wire contract (R-3).
- One host-global `AUTOINCREMENT` id is the only wire cursor; log epoch and worker incarnation are separate identities with the §3 division of labor.
- Two event classes with the §5 rules; compaction of durable history is prohibited; retention is deletion + explicit floor signal.
- Every recovery path ends at snapshot resync; servers hold no per-client durable state; clients own their cursors.
- Snapshots carry `(watermark, log_epoch, incarnation)` from the first v1 endpoint.
- SSE ships only after durable events; ephemeral signals ride id-less.

### Open decisions and their owners

- Exact envelope field names/JSON shape and the closed type enum → typed-envelope issue (constrained by DM-2's minimum and C-1), informed by Expedition V's runtime-fidelity audit for anything activity-shaped.
- Retention limits (age/count values) and whether they are per-deployment or global → M1-18 issue; Expedition I's plan doc lists retention limits as open.
- Heartbeat interval, resync signal encoding (dedicated SSE event type vs HTTP status on reconnect), buffer sizes → M1-21 issue.
- Whether ephemeral signals get their own SSE `event:` name or a field flag → M1-21; cosmetic under this contract.
- WAL-recovery tail-verification mechanism (epoch rotation vs tail checksum) → M1-18 acceptance gates.

### Prerequisites and safe sequencing

Unchanged from Expedition I's route, now unblocked: DM-4 retention cap (L0.3) is independent and prior; typed envelope next (no migration); M1-18 events table takes one v12+ queue slot; M1-19 snapshot endpoints carry watermarks from birth; M1-21 SSE last. Nothing here touches Expedition II's topology or conflicts with L0 correctness work.

### Likely traps and prohibited shortcuts

1. Shipping SSE (or any wire cursor) off the in-memory bus "temporarily" — the restart ambiguity in §2 is silent garbage delivery, not an error.
2. Emitting the live event before the transaction commits because "that's where the emit call already is" (`jobmanager.go:955-959` is the existing counterexample, not a pattern to preserve).
3. Forgetting `AUTOINCREMENT` — plain `INTEGER PRIMARY KEY` reuses rowids after deletion, and the failure appears only after retention runs in production.
4. Client-side gap detection by id arithmetic on a filtered stream (R-6 exists because this "obvious" check is wrong by design).
5. Compacting, rewriting, or summarizing durable rows; or "fixing" chattiness by deleting history instead of reclassifying the emitter (§5).
6. Reusing the daemon server's blanket `WriteTimeout` for the SSE route (`server.go:66` kills streams at 30s).
7. Serving event replay without the R-9 deployment filter because "the worker only has one deployment" — the table is host-wide; the unscoped-`GetJob` leak already happened once.
8. Persisting aggregator-side cursor state in M2 — resume state is client-owned everywhere (§7).
9. Blocking the fan-out path on a slow network subscriber, or silently skipping within a connection — disconnect-with-signal is the only permitted overflow response (§9).
10. Treating `sqliteutil.CheckAndRecover`'s WAL deletion as harmless once the event log is durable — it is a potential history truncation and must interact with the epoch (F6).

### Verification checklist (per landed task)

- [ ] `go test ./...` green; anchors re-run explicitly (V-8).
- [ ] Envelope/persistence tasks: suite items 1–4 exist and pass; one migration, fresh-create + migrate-from-v11 both tested; `CLAUDE.md` schema line updated.
- [ ] Any event-path change: existing eventbus concurrency tests plus item 3's truncation tests pass under `-race`.
- [ ] API tasks: snapshot responses carry watermark/epoch/incarnation; item 6's two-deployment scoping test passes; golden legacy shapes unchanged.
- [ ] SSE task: items 5 and 7 pass; a 35-second idle stream survives (trap 6).
- [ ] PR bodies use `Closes #N` (Expedition I, C6).

### Suggested issue boundaries

1. **Eventbus retention cap + truncation error** — already Expedition I's L0.3, now with §9's abandoned-subscriber semantics folded in. Solo/interactive.
2. **Typed event envelope** — struct + enum audit + legacy rendering (C-1) + suite item 1. No migration, no wire exposure yet. Solo.
3. **M1-18 events table + store-first publish rewiring** — migration (v12+ queue), R-1/R-2 ordering at every emit site, epoch bootstrap, retention job, suite items 2 and 4. Solo; this is the contract's heart.
4. **M1-19 watermarked snapshots** — per C-5, atop the controlapi scaffold. Autopilot-plausible once 3 lands.
5. **M1-21 SSE** — resume, heartbeats, resync signaling, overflow policy, suite items 5–8. Solo (streaming + concurrency).

---

## 15. Unresolved disagreements

- **Milestone wording vs this contract.** The breakdown describes M1-18 as persisting events "onto the bus" with the bus as the spine (`control-plane-milestone-breakdown.md:115`, plan doc `:243-245`); this expedition inverts the authority (store-first, bus demoted to live tail) per §12-A. The plan documents should be read as superseded on this point; the M1-18 issue must be written against this contract, and the divergence is disclosed rather than silently reconciled.
- **Whether budget pause is durable or live-only.** It lives in supervisor memory today (Expedition I §1) and is classified here as live state under ID-3; an argument exists that pause/resume are durable domain events (they are decisions, not telemetry). Recommended resolution at envelope time: emit durable events *about* pause transitions while the paused *flag* stays live-state — but this is one field's classification, safely decidable in the envelope issue.
- **Retention scope (global vs per-deployment) has real tradeoffs** — a chatty deployment can starve a quiet one's history under a global cap — but the choice is deferred to M1-18 with the note that the floor signal (R-7) must be per-stream-correct under either choice.
