# Expedition II — ADR: Coordinator Worker and Process Topology

Status: accepted (research decision; implementation deferred to M1/M2/M3 issues)
Reviewed baseline: `origin/main` @ `df83cbf960e9670983dc3024548d72bc1f626e43` (merge of PR #595, 2026-08-03)
Consumes: `docs/research/fable-expedition/01-architecture-truth-map.md` (Expedition I), especially DM-5, risk 9, and invariant §6.2.
Evidence reviewed: `internal/runtime` (registry, claudecode, codex, opencode), `internal/coordinator`, `internal/supervisor`, `internal/daemon`, `internal/db/schema.go`, `cmd/deploy.go`, `cmd/stop.go`, `cmd/status.go`, `design/opencode-mapping.md`, `docs/control-plane-milestone-breakdown.md`, issues #589/#535, milestones 20–22.

Claims are cited to file:line at the reviewed SHA. Statements derived by reading code paths rather than executing them are marked *(inference)*.

---

## 1. Decision in one paragraph

**One Coordinator per OS process is the invariant; the two-year default topology is that invariant plus, later, a host-side manager/aggregator process that contains zero Coordinators.** A worker process is the unit of deployment, credential scope, failure isolation, signal delivery, and runtime-resource ownership. M2's aggregation and M3's managed lifecycle are built *around* worker processes (fan-in over per-worker APIs plus the host-wide DB; child-process spawn/monitor/restart), never by co-hosting Coordinators in one process. In-process multi-Coordinator is permitted only in tests, under a stated carve-out (§6, I-6).

---

## 2. Context: the topology that already exists

Minder today is process-per-deployment in every load-bearing mechanism, even though no document had yet declared it:

- Both entry points construct exactly one Coordinator for exactly one deployment (`cmd/deploy.go:332`, `cmd/deploy.go:445`); the Coordinator owns one Supervisor, one optional scheduler, one set of trigger routes (`internal/coordinator/coordinator.go:71-100`).
- Daemonization is a re-exec of the minder binary with `--deploy-id`, `Setsid: true`, one child process per deployment (`cmd/deploy.go:261-286`, `internal/daemon/daemon.go:148-187`).
- Identity and discovery are PID + heartbeat files keyed by deploy ID (`internal/daemon/daemon.go:31-38`); liveness is a per-PID signal-0 probe (`daemon.go:59-77`); crash detection compares heartbeat age against PID liveness (`daemon.go:125-133`).
- `minder stop` is a SIGTERM to the deployment's PID (`cmd/stop.go:43-60`); the in-process signal handler cancels the one Coordinator (`cmd/deploy.go:350-359`).
- `minder status` lists deployments from the host-wide DB and probes each PID (`cmd/status.go:227-238`).
- The HTTP API server is constructed with a single `deployID` and per-Supervisor callbacks (`internal/daemon/server.go:19-31`, `cmd/deploy.go:362-373`).
- SQLite is host-wide and multi-process: WAL + `busy_timeout(5000)` is the cross-process contract, `SetMaxOpenConns(1)` the in-process one (`internal/db/schema.go:438-445`). Two concurrent `minder deploy` processes already share `v2.db` this way today.

Two runtime facts make the process boundary semantically meaningful, not just incidental:

1. **claude-code and codex are per-run subprocesses** (`internal/runtime/claudecode/claudecode.go:114`, `internal/runtime/codex/codex.go:107`) — stateless with respect to the worker process.
2. **opencode is a process-global shared server.** `sharedManager` lives at package scope (`internal/runtime/opencode/server.go:30-35`); its serve process inherits `os.Environ()` plus per-invocation env, and that environment — provider API keys and the injected `OPENCODE_PERMISSION` headless policy — is applied **only when the server first starts** (`server.go:42-46`, `server.go:57-65`, `internal/runtime/opencode/permission.go:29-44`). The design decision was explicitly "one shared `opencode serve` per deployment" (`design/opencode-mapping.md:127`, spike #535); the code realizes "per deployment" as "per process" and documents that callers must treat provider credentials as deployment-level (`server.go:40-41`).

Teardown couples these: `runtime.RegisterShutdown` accumulates **process-global** hooks (`internal/runtime/registry.go:59-66`), opencode registers its server-killer from `init()` (`internal/runtime/opencode/opencode.go:68-73`), and `Supervisor.Stop` — a *per-deployment* method — calls `runtime.ShutdownAll()` after draining (`internal/supervisor/supervisor.go:465-485`). This is correct **if and only if** the process contains one Coordinator. That conditional correctness is the crux of this ADR.

---

## 3. Options

### Option A — One Coordinator per OS process (pure worker-per-deployment)

What exists today, declared as policy. Every deployment is its own worker process (foreground or setsid daemon). M2 aggregation is client-side fan-in: the TUI/CLI discovers workers (PID/heartbeat, later sockets) and merges their per-worker API responses with host-wide DB history. M3 lifecycle stays `Daemonize` + SIGTERM as today, driven directly by the CLI.

### Option B — Multiple Coordinators in one host process

A single long-lived `minder host` daemon runs all deployments as in-process Coordinators. One process, one SQLite connection, one API server multiplexing deployments, one shared opencode server.

### Option C — Deliberate hybrid: worker processes + a Coordinator-free host process (recommended)

Option A's invariant, plus a separate host-side process (or processes) for the cross-deployment concerns M2/M3 add:

- **Aggregator (M2-03):** read-only fan-in over worker APIs + host-wide DB, serving the identical resource shapes as a single worker (`docs/control-plane-milestone-breakdown.md:192-194`).
- **Manager (M3-09/10):** spawns, monitors, restarts worker processes; owns command routing to the right worker (`breakdown.md:256-261`).

The host process holds **zero Coordinators** and executes **zero agents**. Option A is Option C's degenerate form — the manager/aggregator is optional forever; foreground workers with no host process remain first-class (Expedition I invariant §6.3, M3-12's "managed mode is never mandatory", `breakdown.md:268`).

---

## 4. Evaluation against the issue's nine axes

| Axis | A: process-per-Coordinator | B: multi-Coordinator process | C: hybrid |
|---|---|---|---|
| Credential & permission isolation | Per-process env is the credential scope; opencode's first-start env capture is *correct* (process = deployment) | **Broken by construction**: first deployment's provider keys + `OPENCODE_PERMISSION` policy captured at first `ensure()` apply to all later deployments (`server.go:42-46,65`); `GITHUB_TOKEN` is the only per-invocation env (`internal/supervisor/runtime.go:102-105`) | Same as A; manager holds no agent credentials at all |
| Stop one deployment without affecting another | SIGTERM to one PID (`cmd/stop.go:43-60`); `ShutdownAll` in `Stop` reaps only that deployment's resources | **Broken**: `Supervisor.Stop` → `runtime.ShutdownAll()` (`supervisor.go:484`) kills the shared opencode server mid-run for every other deployment; process signals are all-or-nothing | Same as A; manager adds graceful stop-with-timeout on top of SIGTERM |
| Crash & resource isolation | A panic in any supervisor/runtime goroutine kills one deployment; budget/turn limits are per-deployment | One panic kills every deployment; one runaway deployment's memory/etc. starves the rest | Same as A, plus manager-driven crash restart (M3-10); manager crash leaves workers running (they are setsid-independent, `daemon.go:171-173`) |
| Logs, events, stable identity | Per-deployment daemon log (`daemon.go:41-43`), per-deployment agent logs, per-Supervisor eventbus; deploy-id ↔ PID is 1:1 | Daemon stdout/log interleaves all deployments; `WritePID` writes the same PID under N deploy IDs, so `IsRunning`/`WasCrashShutdown` conflate deployments (`daemon.go:59-77,125-133`) | Same as A; registry (M2-02) formalizes `(server_id, deployment_id, resource_id)` identity over it |
| Memory/process overhead | N Go workers (small) + N opencode serve processes when opencode is used. Agent subprocesses (claude/codex CLIs) dominate host cost regardless of topology and scale with total concurrent agents, not with worker count *(inference)* | Lowest: 1 Go process, 1 opencode server — but the shared server is exactly the credential/teardown defect above | A's cost + one small manager process |
| Upgrades & rolling restarts | Per-deployment: new binary serves new deployments while old workers drain; `Daemonize` re-execs `os.Executable()` (`daemon.go:157,176`) | All-or-nothing: upgrading the binary restarts every deployment simultaneously | Best: manager restarts workers one at a time; manager itself can restart without touching workers (reattach via discovery, not in-memory state) |
| Local & remote discovery | Local: scan deploy-ID-keyed PID/heartbeat files + host-wide DB (`cmd/status.go:227-238`); planned per-worker Unix socket (M1-22, `breakdown.md:124`) slots in as one file per worker. Remote: per-worker TCP (`server.go:74-81`) | Single well-known endpoint (simplest discovery) — the one genuine advantage, and the aggregator in C provides it without the defects | Local same as A; remote: aggregator is the single endpoint, workers stay local-only |
| Testability | Integration tests must spawn processes for true topology coverage (M2-14 needs ≥2 concurrent deployments, `breakdown.md:220`) | In-process Coordinators are trivially testable — but the tests then exercise a topology production never runs | A's story, plus the test carve-out (I-6): in-process multi-Coordinator unit tests stay legal because hook-registering runtimes are excluded from them |
| Future runtimes with process-global resources | Safe by invariant: "process-global = deployment-global" holds, so a runtime may own a package-global child (opencode does) | Every such runtime is a first-writer-wins conflict; each needs bespoke multi-tenancy | Same as A, with the invariant written down (I-4) so the next adapter author doesn't have to rediscover it |

**Verdict:** B fails three axes outright (credentials, independent stop, crash isolation) on current-code evidence, and its advantages (memory, single endpoint, single DB connection) are either noise (worker memory vs. agent subprocess cost), provided defect-free by C (aggregator endpoint), or already solved (cross-process SQLite via WAL + busy_timeout, in production today with concurrent deployments). A is correct but leaves M2/M3's cross-deployment concerns unowned. C owns them without touching the invariant.

**Recommendation: Option C, with confidence high on the invariant and medium on the manager's exact shape** (single combined manager+aggregator vs. separate processes is deliberately left to M2/M3 design — it does not affect the invariant).

---

## 5. Why this is the two-year default, not just the easiest next step

The tempting "easiest next step" framing is Option A alone — do nothing, declare the status quo. The reason to commit to C is that M2 and M3 both contain a fork where an implementer, absent this ADR, would plausibly reach for B:

- M2-03 "Host-level aggregate API" (`breakdown.md:194`) could be built as a daemon that *hosts* deployments instead of *reading* them — it must not be.
- M3-09 explicitly records "Managed workers child-process vs in-process" as an open decision (`breakdown.md:260,291`). This ADR closes it: **child-process**.

Two-year pressures the decision was tested against:

1. **BYOA / multi-provider credentials.** The BYOA direction (hosted OSS providers, per-deployment provider choice) makes per-deployment credential scope a product feature, not an implementation detail. Only process-per-Coordinator gives it to us for free via process env; B would require per-session credential plumbing through every runtime, including opencode's server which applies env only at first start (`server.go:42-46`).
2. **Runtime growth.** Every runtime added so far either spawns per-run subprocesses or owns a process-global server. The invariant makes both patterns safe without per-runtime multi-tenancy engineering. A future runtime backed by a genuinely *host*-global resource (e.g., a single local inference server) is handled by rule I-5: externalize it as an independent service; never share it as a runtime-owned child.
3. **Write-load growth (Expedition I, risk 9).** If M1-18 event persistence multiplies write volume, the fix is write batching or moving the event log — explicitly **not** merging processes to get a single writer. C keeps that escape hatch honest by never making single-process an architectural dependency.

---

## 6. Invariants

Violating any of these is a bug, not a tradeoff. They extend Expedition I §6 and are the contract cheaper agents implement against.

- **I-1 (topology).** A production minder process contains at most one Coordinator, and a Coordinator's lifetime is its process's lifetime. `runForeground` and `runDaemon` remain the only production assembly paths (Expedition I invariant §6.2); any M3 manager spawns workers through the same daemonize/re-exec seam, never `coordinator.New` in its own process.
- **I-2 (lifecycle).** Worker shutdown ordering is fixed: signal → context cancel → Supervisor drains in-flight jobs → `runtime.ShutdownAll()` → PID/heartbeat cleanup (`cmd/deploy.go:354-359`, `supervisor.go:465-485`, `cmd/deploy.go:305-311`). `ShutdownAll` may only be reached from a code path that knows it is the process's sole Coordinator; today that is `Supervisor.Stop`, and it is correct only under I-1. If I-1 is ever relaxed, `supervisor.go:484` is the first line that must move (to the process entry point) — flagged here so nobody discovers it in an incident.
- **I-3 (failure isolation).** A worker crash affects exactly one deployment. Recovery is worker-local and DB-driven: stale-heartbeat detection + `TransitionStaleRunningJobs` on next start (`daemon.go:125-133,142-144`, `cmd/deploy.go:415-420`). A manager crash affects zero workers; a restarted manager reattaches via discovery (PID/heartbeat/socket + DB), never via retained in-memory state.
- **I-4 (runtime resources).** "Process-global equals deployment-global." A runtime adapter may own package-global shared resources (opencode's `sharedManager`) and register process-level teardown (`registry.go:59-66`) only because I-1 makes the process a deployment. This is now a stated clause of the runtime contract, to be recorded in `AgentRuntime`'s documentation when next edited.
- **I-5 (host-global resources).** A resource that must be shared *across* deployments on a host (a local inference server, a shared model cache) is an external service with its own lifecycle and address, configured per deployment — never a runtime-owned child process, which I-2 would kill when its one deployment stops.
- **I-6 (test carve-out).** Tests may instantiate multiple Coordinators/Supervisors in one process only with runtimes that register no shutdown hooks (the default claude-code path, fakes). A test driving two Supervisors where either uses opencode is invalid: the first `Stop` kills the other's server (`supervisor.go:484` + `opencode.go:72`). True topology coverage (M2-14) requires real worker processes.
- **I-7 (data plane).** SQLite stays host-wide and multi-process: WAL + `busy_timeout` across processes, single connection within one (`schema.go:438-445`). No per-worker database; no worker-to-worker communication except through the DB and (read-only) through APIs. Workers never read each other's sockets.
- **I-8 (identity).** Deploy ID ↔ worker PID is 1:1 while the worker runs; every per-worker artifact (PID, heartbeat, daemon log, future socket) is keyed by deploy ID under `~/.agent-minder` (`daemon.go:26-43`). M2-02's registry identity builds on this key, not beside it.

---

## 7. Current code that conflicts with each rejected alternative

**Conflicts with Option B** (each is a rework item B would require; none block C):

| # | Mechanism | Evidence | What B would force |
|---|---|---|---|
| B-1 | `Supervisor.Stop` calls process-global `ShutdownAll` | `supervisor.go:484`; `registry.go:68-80` | Refcounted or Coordinator-scoped runtime resource lifecycle |
| B-2 | opencode server is package-global; env + permission policy fixed at first start | `server.go:30-35,42-46,57-65`; `permission.go:36-44` | Per-deployment server instances or per-session credential injection through the SDK |
| B-3 | Credentials are process-env-scoped; invocation env carries only `GITHUB_TOKEN` | `server.go:58` (`os.Environ()`); `internal/supervisor/runtime.go:102-105` | A per-deployment credential plumbing layer across all three runtimes |
| B-4 | Signal handling cancels the whole process's one Coordinator | `cmd/deploy.go:350-359,467-475` | Per-deployment stop channels; SIGTERM semantics redefined |
| B-5 | PID/heartbeat identity assumes one deployment per PID | `daemon.go:46-51,59-77,125-133`; `cmd/stop.go:43-60` | New liveness/crash-detection scheme; `stop` can no longer be a signal |
| B-6 | HTTP server binds one deploy ID; budget callbacks reach one Supervisor | `server.go:19-31`; `cmd/deploy.go:362-373,478-489` | Deployment-multiplexed routing (and fixing the unscoped `GetJob`, Expedition I EC2, becomes a prerequisite rather than a cleanup) |
| B-7 | Daemonize re-exec carries per-deployment flags in argv | `cmd/deploy.go:261-283` | A config-delivery channel for a multi-deployment host process |

**Conflicts with pure Option A** (why A alone is insufficient): nothing in code — A is the status quo — but M2-03 aggregation and M3-09 managed lifecycle have no home. Pure A pushes both into the CLI/TUI client, which then owns worker spawning and fan-in itself; that is workable for M2 reads *(inference)* but makes M3 restart/recovery (a supervisor-of-workers loop that must outlive any CLI invocation) structurally homeless.

---

## 8. Migration and escape strategy

**Migration to the recommendation: none.** The invariant is already true. The work C adds (aggregator, manager) is additive in M2/M3, and this ADR only constrains *where* it runs.

**Escape hatch to Option B**, should a revisit trigger fire. Prerequisites, in order — each is independently useful hardening, so the escape path degrades gracefully into cleanup even if B never happens:

1. Move `ShutdownAll` out of `Supervisor.Stop` to the process entry points (dissolves B-1's coupling; harmless under I-1).
2. Replace the package-global `sharedManager` with an instance owned by the Coordinator's runtime resolution, keyed by credential set (B-2).
3. Introduce per-invocation credential injection in the runtime contract, ending `os.Environ()` inheritance as the credential channel (B-3).
4. Only then: multiplex signals, identity files, and the API server (B-4..B-7).

**Revisit triggers** (any one suffices to reopen this ADR):

- Sustained operation of roughly two dozen or more concurrent deployments per host where measured worker + opencode-server memory (not agent subprocess memory) is the binding constraint.
- Minder embedded as a library or offered as a hosted multi-tenant service.
- A runtime whose backing resource provably cannot run one instance per worker process on a host, and cannot be externalized per I-5.
- Expedition IV / M1-18 outcome: if durable event writes measurably starve worker DB access across processes (risk 9), first apply batching / event-store separation; if that fails, single-writer consolidation may be re-argued — with this ADR's prerequisites as the entry fee.

---

## 9. Implications

**M1 (remaining, per Expedition I Levels 1–3):**
- M1-22's per-worker Unix socket is confirmed topology-final, not provisional: one socket per worker at a deploy-ID-keyed path under `~/.agent-minder` (I-8). This resolves Expedition I's disclosed disagreement ("should the per-worker socket wait for Expedition II?") — **no**; it proceeds.
- DM-5's state-provider interface is *served* in-worker, per its revisit note. The aggregator later consumes worker HTTP/socket APIs as a client; it never links the provider interface directly.
- Legacy-route deployment scoping (Expedition I L0.4) gains urgency: under C, the aggregator will re-serve worker responses, so a worker leaking another deployment's jobs from the shared DB (`EC2`) would propagate upward.

**M2:**
- M2-02 local discovery composes exactly what exists: deploy-ID-keyed PID/heartbeat/socket files + host-wide DB rows; no new registry daemon is required for local mode *(inference; M2-02's design remains open on remote)*.
- M2-03's aggregator is a Coordinator-free process (or a mode of the manager) doing read-only fan-in; per M2's exit gate, it must serve resource shapes identical to a single worker (`breakdown.md:194,220`). Historical/cross-deployment queries come from the DB; live state comes from worker APIs — the same split Expedition I §1 documented.
- The TUI attaches to workers or the aggregator, never hosts a Coordinator (Expedition I invariant §6.9 already forbids supervisor imports).

**M3:**
- M3-09 is decided: managed workers are **child processes**, spawned through the existing daemonize seam, stopped via SIGTERM with a manager-enforced drain timeout. The manager is a restartable supervisor-of-processes whose own crash strands nothing (I-3).
- M3-10 crash recovery composes existing worker-local recovery (heartbeat staleness + `TransitionStaleRunningJobs`) with manager-driven restart; no new recovery semantics inside the worker.
- Command routing (M3-01) addresses a command to a deployment, and the manager delivers it to that deployment's worker; misdelivery is structurally impossible when worker = deployment (M3-12's exit-gate requirement, `breakdown.md:268`).

**Runtime adapters:**
- The runtime contract gains I-4/I-5 as documented clauses (a comment-level change on `AgentRuntime` / `RegisterShutdown` when next touched — not a refactor now, per the issue's non-goals).
- Adapter authors get a simple rule: per-run subprocess (claudecode/codex pattern) or process-global shared child (opencode pattern) are both fine; anything host-global must be an external service.
- opencode's `ensure()` env-capture semantics stop being a wart and become the documented consequence of I-4; the existing comment at `server.go:40-41` is already correct and should not be "fixed" toward per-call env.

---

## 10. Cheaper-agent implementation and verification guide

### Fixed decisions (do not re-litigate)

- One Coordinator per production process (I-1); hybrid per Option C; M3-09 = child processes; per-worker socket proceeds without waiting on M2 design.
- The host process (aggregator/manager) never constructs a Coordinator, never runs agents, never registers runtime shutdown hooks.
- All invariants I-1..I-8.

### Open decisions and their owners

- Manager and aggregator: one process or two, and whether the aggregator is a manager mode → M2/M3 design (M2-03, M3-09 issues), constrained by I-1/I-3.
- Socket path convention and permission bits → M1-22 issue (I-8 fixes only the keying).
- Remote discovery/trust → M3-02; untouched here.
- Event-write contention response → Expedition IV; this ADR only forbids solving it via process merger.

### Prerequisites and safe sequencing

Nothing here blocks Expedition I's Level 0–2 route; this ADR removes two blockers from it (per-worker socket; DM-5 serving location). The only strictly *new* implementation work this ADR generates before M2/M3 is documentation-adjacent:

1. **Record I-4/I-5 in the runtime contract docs** — comment-only change to `internal/runtime/runtime.go` / `registry.go` doc comments. Autopilot-safe; no behavior change; anchors unaffected.
2. **Add a topology guard test** — a test asserting `runForeground`/`runDaemon` remain the only production `coordinator.New` callers (e.g., a package-list grep test in the spirit of `TestClaudeMDSchemaVersion`). Autopilot-safe. *(Optional; skip if it fights the build.)*

Everything else lands inside already-planned M1/M2/M3 issues; add "per ADR-02" citations to those issue bodies when filing.

### Likely traps and prohibited shortcuts

1. Building M2-03 aggregation as a daemon that *hosts* Coordinators "since it's already long-lived." Prohibited by I-1; the failure modes are B-1..B-7.
2. "Cleaning up" `Supervisor.Stop`'s `ShutdownAll` call, or conversely adding a second `ShutdownAll` call site, without reading I-2. Its placement is deliberate and safe only under I-1.
3. Writing an integration test that runs two Supervisors in one process with the opencode runtime (violates I-6; the first Stop breaks the second deployment). Use real processes or hook-free fakes.
4. Sharing one opencode server across workers "to save memory" — recreates B-2's credential capture across deployments. If memory is measured to matter, that is a revisit trigger, not a shortcut.
5. Making managed mode (M3) required for anything M1/M2 does, or letting the manager's absence degrade a foreground worker. Foreground-first-class is an anchor-enforced invariant.
6. Giving workers knowledge of each other (reading sibling sockets, coordinating through new tables). Cross-deployment logic lives in the aggregator/manager or the client, full stop (I-7).
7. Redefining PID/heartbeat semantics while adding sockets. The socket is an *additional* deploy-ID-keyed artifact (I-8); liveness remains PID-probe-based until M3 deliberately revisits it.

### Verification checklist

- [ ] Two concurrent deployments (separate processes, shared DB): `minder stop` of one leaves the other's jobs running and — with opencode — its server alive; assert via `status` and a live prompt after the stop.
- [ ] Worker kill -9 mid-job: restart recovers via stale-heartbeat path (`TransitionStaleRunningJobs` count > 0), sibling deployment unaffected.
- [ ] With opencode on two deployments (two workers): each worker owns a distinct serve process/port; killing one worker's server does not disturb the other. *(This doubles as the I-4 regression test.)*
- [ ] Foreground neutrality: all of the above with one worker in `--foreground` — byte-stable stdout per the green anchors.
- [ ] Contract-doc change (I-4/I-5) introduces no behavior diff: `go test ./...` green, anchors green.
- [ ] When M2-03/M3-09 land: exit-gate proofs already specified in `breakdown.md:220,268` (one TUI over ≥2 deployments with zero Coordinator effect; commands reach only the intended Coordinator; managed mode never mandatory) — those are this ADR's acceptance gates and need no new ones.

### Suggested issue boundaries

1. **Runtime-contract doc clauses (I-4/I-5) + optional topology guard test.** Autopilot-safe, immediate.
2. **M1-22 per-worker Unix socket** — now unblocked; cite I-8 for pathing. (Already in Expedition I's route, Level 2.)
3. **M2-02 discovery registry** — cite I-8; local mode composes existing files, no new daemon.
4. **M2-03 aggregator** — must open with a design note confirming Coordinator-free process placement; cite I-1/I-3 and §7's B-table as the anti-pattern list.
5. **M3-09/10 manager** — child-process decision is pre-made; issue scope is spawn/monitor/restart mechanics + drain timeout semantics only.

---

## 11. Unresolved disagreements

- **Whether the aggregator and manager are one process or two.** One process is operationally simpler; two keeps M2 (read-only, low-trust) separable from M3 (command-bearing, high-trust). Deliberately left to M2/M3 design; both satisfy the invariants. Revisit at M2-03 scoping.
- **Per-worker opencode server memory cost.** The claim that agent subprocesses dominate host memory is inference, not measurement. If someone measures N idle opencode serve processes as a real constraint before the revisit trigger's deployment count, the cheaper mitigation is lazy start (already the behavior — the server starts on first opencode run, `server.go:42-77`) plus idle shutdown, not sharing. Noted so the measurement, if made, lands on the right fix.
- **Expedition I's socket-timing disagreement is resolved here in favor of proceeding** (per-worker socket is topology-final). If Expedition IV's event-transport work wants a different local transport, it must argue against I-8's keying, not reopen the topology.
