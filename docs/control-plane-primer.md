# The Control Plane Milestones, Explained From Scratch

Audience: an engineer who knows minder's purpose but hasn't followed the recent
work. This explains what milestones 20–22 (called M1, M2, M3 in the docs) are
building, why each piece exists, and where things stand. It duplicates nothing
authoritative; the source documents are
[`control-plane-and-tui-plan.md`](control-plane-and-tui-plan.md) (the design)
and [`control-plane-milestone-breakdown.md`](control-plane-milestone-breakdown.md)
(the task list). This one is for orientation.

## The one-paragraph version

Minder can run agents well, but it can't *show you* what it's doing very well.
Today, answering "what is that agent doing right now?" means reading raw log
files, and the code that runs agents is tangled together with the code that
prints status. The three milestones untangle that: M1 gives minder a clean
internal engine (the Coordinator) plus a proper record of everything that
happens and a read-only API to query it. M2 builds a terminal dashboard (the
TUI) that is purely a *viewer* of that API. M3 adds carefully guarded buttons
(stop this job, pause dispatch, reload config) on top. Read first, then watch,
then touch.

## The problem being solved

A few concrete pains motivated this project. All of these are true of the code
as it stood when the plan was written:

1. **Two copies of the startup logic.** `minder deploy --foreground` (run it in
   your terminal, watch the output, Ctrl-C to stop) and the background daemon
   each independently wire up the same scheduler, trigger routes, supervisor,
   and optional HTTP server. Two assembly paths means they drift, and any new
   feature has to be added twice.

2. **Live state evaporates.** The supervisor knows, at any instant, which tool
   an agent is running and how long it's been going. But that information is
   held in memory and erased the moment the tool finishes. Once a job ends, the
   only record of *how* it went is a giant raw log file. There's no durable
   answer to "what were the last five things this agent did."

3. **Events get dropped.** Internally, the supervisor announces things
   ("job started", "PR opened") on a single Go channel with a capacity of 64.
   If the consumer is slow, events are silently thrown away. And only one
   consumer can listen. That's fine for printing to a terminal; it's useless as
   a foundation for a dashboard, an API, or persistence — those need *fan-out*
   (several listeners getting every event) and *resumability* (a client that
   disconnects can catch up on what it missed).

4. **Configuration state is half-real.** `jobs.yaml` declares scheduled jobs
   and label-triggered automations. But some of what it declares wasn't
   faithfully applied: `milestone:*` triggers validated but never actually
   installed, per-trigger budget overrides were dropped on the floor, schedule
   identity was global by name (two deployments with a `nightly-scan` job
   collided), and removing an entry from the file didn't reliably disable it.
   You couldn't trust any UI built on this, because the system's actual state
   didn't match the file, and neither was queryable.

5. **The status API is an afterthought.** The daemon's HTTP routes are ad hoc,
   tied to a single deployment, unversioned, and know nothing about most of the
   interesting state. The TUI command (`minder tui`) is literally a stub.

The theme: **orchestration and presentation are fused, and there is no
trustworthy, queryable model of what minder did or is doing.** Everything in
M1–M3 follows from deciding to fix that.

## The architectural idea

The plan introduces a strict layering. Reading it top to bottom:

```
jobs.yaml            what you asked for (canonical desired config)
   │
Coordinator          the engine: loads config, schedules, supervises,
   │                 enforces budgets — one per deployment
   ├── SQLite        durable history (jobs, runs, events)
   ├── event bus     live happenings, fan-out, cursor-numbered
   └── /api/v1       versioned read (later: command) surface
   │
clients              foreground stdout, CLI commands, the TUI, SwiftBar —
                     all mere consumers of the API, none special
```

Three rules make the design hold together:

- **One engine, many hosts.** Foreground mode, the daemon, and any future
  managed server all *host the same Coordinator object* instead of assembling
  their own. Foreground stays a first-class way to run minder forever; it just
  becomes "a Coordinator whose output goes to your terminal."

- **Clients are dumb.** The TUI may not open the SQLite database, parse log
  files, or import supervisor internals. It talks HTTP/SSE to the API like any
  external program would. This sounds like ceremony, but it's what guarantees
  the dashboard can never corrupt or slow down the thing it's watching, and it
  means killing or restarting the TUI has zero effect on running agents.
  (SSE — Server-Sent Events — is the boring, HTTP-native way to stream events
  to a client; each event carries an ID so a reconnecting client can say "give
  me everything after event 4823.")

- **Truth is layered, and each layer has one owner.** `jobs.yaml` is desired
  config. The Coordinator's loaded snapshot is active config (including "the
  file on disk has changed since I loaded it" drift detection). SQLite is
  durable history. Snapshots plus cursor-numbered events are live state. Raw
  logs get demoted to evidence for debugging, no longer the data model.

If you've seen Kubernetes' controller pattern or any snapshot-plus-event-log
system, this will feel familiar. The novelty is only in fitting it to minder's
constraint that a single foreground process with no server must keep working.

## M1 — Coordinator & Observability (milestone 20)

M1 is the spine, and deliberately the biggest of the three. Its exit test:
**an external read-only client can accurately observe a running minder, and
`minder deploy --foreground` behaves exactly as before.** No UI ships in M1.

The work came in waves, and the ordering itself is worth understanding because
it's a textbook refactoring discipline: *characterize before you refactor,
refactor before you extend.*

**Wave 0 — pin current behavior with tests.** Before touching anything, write
"anchor" tests that pass today (startup summary lists the right subscriptions,
cron rows persist, shutdown is clean, legacy HTTP routes return exactly these
JSON shapes) and "red spec" tests that *fail* today, one per known correctness
bug. The anchors make the refactor safe; each red test turns green when its fix
lands, so the bug list is executable. (#559, #560 — done.)

**Wave 1 — extract the Coordinator.** Create `internal/coordinator` owning the
config snapshot, scheduler, supervisor, and lifecycle, then rewire foreground
and daemon to host it. Pure restructuring, no behavior change, anchors stay
green. (#569, #582 — done.)

**Wave 2 — make automations honest.** Fix the config-vs-reality gaps from pain
#4 above: install milestone triggers, carry budget/turn overrides into jobs,
scope schedule identity to `(deployment, name)`, disable removed entries.
(#566, #567, #570, #568 — done.)

**Wave 3 — build the durable model.** New additive SQLite tables and columns:

- *Provenance* (#571 — done): every job records why it exists —
  `source_type` (trigger / cron / watch / explicit deploy), which automation,
  and what reference. So "show me everything the nightly scan produced" becomes
  a query instead of guesswork.
- *Agent runs* (#583 — done): one row per (job, stage, attempt) recording the
  actual agent, runtime, model, session ID, timestamps, status, cost, and final
  text. Previously a multi-stage job with a retry left no structured trace of
  what actually ran.
- Still to come in this wave: durable recent activity (stop erasing "which
  tool, what input" when the tool finishes), normalized final results, typed
  deliverables (PRs, issues, comments, reports as records instead of prose in
  logs), and consistent log addressing.

**Wave 4 — the event bus.** Replace the drop-prone channel with
`internal/eventbus`: every event gets a monotonically increasing cursor number,
multiple subscribers each get the full stream, and slow consumers are handled
deliberately instead of silently. (#584 done; #606 — bounded retention and
reaping of abandoned subscribers — is the branch currently in flight.) On top
of the bus: a closed set of *typed* event kinds (#608) rather than free-form
strings, and persistence of events to SQLite so history and live tail connect.
A design study (expedition 04) fixed the consistency contract: events are
stored first, the bus is the live tail, and clients resync from a snapshot
watermark. That's what makes "disconnect, reconnect, miss nothing" possible.

**Wave 5 — the versioned API.** `internal/controlapi` with `/api/v1/...` read
endpoints (deployments, automations, jobs, runs, deliverables, logs) plus
`/api/v1/events` as SSE. Two decisions here worth knowing:

- *The contract is Go types, not an OpenAPI file.* Every consumer in sight is
  in-repo Go, so a hand-maintained spec would just drift; golden-JSON tests pin
  the wire format instead. OpenAPI can be generated later if a browser UI ever
  appears.
- *Local transport is a Unix socket, on by default.* Every deployment worker
  exposes a permission-scoped local socket, so local tools can observe it with
  no TCP port, no `--serve` flag, and no network exposure. TCP stays opt-in and
  authenticated. (Related loose end: #524, the daemon's TCP API currently
  allows running with no auth at all.)

Also under the hood: #607 inserts a "state provider" interface so the API layer
never touches the `*Supervisor` type directly. That's the seam that keeps
presentation permanently separated from orchestration.

**Status:** roughly the first half is merged (waves 0–2, provenance, agent
runs, the bus core; schema went v8 → v11 along the way). Open now: #606
(retention, in flight), #607, #608. Unfiled remainder: the rest of wave 3, event
persistence, and the whole `/api/v1` surface. Dispatch is intentionally paused
for budget.

## M2 — Multi-Deployment TUI (milestone 21)

M2 is the payoff you can see. A TUI — terminal user interface, a full-screen
keyboard-driven dashboard in the terminal, bubbletea-based like the rest of
minder's interactive surfaces — that is *only* an API client.

Why "multi-deployment" is in the name: you might have a foreground deployment
running in one terminal, a daemon on the same machine, and eventually a worker
on a VPS. One TUI session should watch several at once, which is why every
resource is identified as `(server, deployment, resource)` and why M2 includes
a small discovery/registry layer (find local workers via their sockets and
heartbeat files) and a host-level aggregate API that merges live worker
snapshots with host-wide history.

The four planned views map directly onto the M1 data model, which is the point
— every view is possible *only because* some M1 wave built its data:

| View | Shows | Fed by |
|---|---|---|
| Overview | health, capacity, budget, next cron, active agents, attention items | snapshots |
| Automations | loaded triggers/cron, drift and errors, last/next activation, linked jobs | wave 2 + automation snapshot |
| Runs | live cross-deployment job list, master-detail | jobs + provenance |
| Run detail | stage/agent timeline, recent tool activity, usage, logs, deliverables | agent runs, events, deliverables |

The design anchors the TUI on the strongest existing workflow rather than
inventing a new one: `minder checkout` (the searchable picker over job history
with an Enter-driven action menu — open worktree, continue interactively, view
logs, open the PR). That picker UX becomes the Runs view, and the checkout
logic moves into shared services so the CLI path and the TUI path can't
diverge. `minder checkout` itself stays, as the fast non-TUI route.

The one M2 issue filed so far, #621, is exactly this: `minder sessions`, an
API-only picker and detail view for active deployments. Its acceptance criteria
are a good miniature of the whole philosophy — for instance, a test must prove
the command performs *no* direct database, PID-file, or supervisor reads.

M2 ships no controls. If you notice a stuck job in the TUI, you still stop it
the old way. That's deliberate.

## M3 — Safe Operations (milestone 22)

Only after the read-only model has been used and trusted do mutations arrive:
run an automation now, pause/resume dispatch, stop or retry a job, extend a
budget, reload `jobs.yaml` with a validated diff, and start/stop managed
workers.

The reason this is a whole milestone rather than a handful of POST endpoints:
remote controls over long-running, money-spending agents have sharp failure
modes (double-fire on retry, a command landing on the wrong deployment, a crash
mid-command). So M3 builds one shared *command envelope* first — every command
declares its scope and required capabilities, carries an idempotency key
(re-sending the same command is safe and produces one audit record), moves
through explicit states (pending → accepted → completed/failed/timed-out) that
are themselves events on the bus, and leaves an audit trail. Individual
controls then become thin: a Coordinator method, an endpoint, a TUI
confirmation showing exactly what will be affected.

Two guardrails recur throughout: `jobs.yaml` stays canonical (reload applies
the validated file; there is never a second, UI-owned config store), and
nothing about managed mode is mandatory — foreground with Ctrl-C keeps working
untouched.

The one filed issue, #622, is representative: `minder sessions <id> drain`
pauses dispatch for one deployment (running jobs finish, nothing new starts,
worker stays alive and observable), idempotent, audited, and distinguishable in
the UI from a budget-triggered pause so you always know *why* dispatch stopped.

## Where the surrounding milestones fit

- **Milestone 23 ("The Cartographer's Lantern")** was a research pass:
  documentation-only expeditions that audited this plan against the real code
  and fixed the expensive design questions — the architecture truth map, the
  worker-process topology decision (workers stay child processes for crash
  isolation), and the event/snapshot consistency contract mentioned above. Its
  outputs live in `docs/research/fable-expedition/`.
- **Milestone 24 ("Lantern Cleanup")** was the correctness fallout from those
  audits — nine small fixes (worktree collision destruction, auto-merge base
  verification, provenance gaps, API scoping, a timing-side-channel in auth)
  cleared *before* resuming M1, so the remaining control-plane work builds on
  ground that's known-good. One item remains open (#524, optional API auth).

## What comes after: the bb-inspired improvements

Separately from M1–M3, a research pass over [bb](https://github.com/get-bb/bb)
(the agentic IDE minder development itself runs inside) looked for ideas worth
borrowing. Full detail is in
[`../design/bb-exploration.md`](../design/bb-exploration.md); the short version
belongs here because it's the likely *next* milestone after the control-plane
work.

First, the reassuring finding: bb's internals independently converge on the
same architecture M1–M3 are building — append-only typed events with cursors,
snapshot-plus-resume for reconnecting clients, UIs that are pure API consumers,
and guarded audited commands. Nothing in bb argues for changing course. bb and
minder also split cleanly on purpose: bb is built around a human watching and
steering live sessions, minder around unattended operation. The interactive
features (live mid-turn steering, panes, forking, terminals) are deliberately
*not* being copied; minder's answer when a human wants hands-on is the existing
checkout flow into a real interactive session.

What transfers is operational plumbing, grouped into a proposed follow-up
milestone ("Operator Experience — Lifecycle & Resilience"):

1. **A worktree lifecycle contract.** bb gives every fresh worktree a
   git-tracked setup hook (install deps, prep fixtures; a failure blocks the
   job cleanly instead of letting the agent flail), a `.worktreeinclude`
   allowlist that copies untracked files like `.env` from the main checkout,
   and an ownership rule for cleanup. Minder's version: run a setup hook and
   include-copy before the agent starts, auto-remove worktrees when the PR
   merges, and keep failed/suspect ones around with a TTL for debugging
   instead of relying on manual reaper sweeps. Highest payoff of the batch —
   today agents burn paid turns rediscovering missing dependencies.

2. **Runtime capability preflight.** Treat each runtime (Claude Code, Codex,
   opencode) as something you can *ask*: is the CLI installed, which models
   does it accept, what flags does it support. Validate every job at
   activation, so a typo'd model or missing CLI fails instantly with a clear
   error instead of after a worktree and an agent launch. Record the resolved
   runtime/model/version in the `agent_runs` table, closing the long-standing
   "does the model field in config provably reach execution" question. A
   `minder doctor` command prints the capability matrix.

3. **Usage-limit park-and-resume.** Minder runs on subscription-billed agent
   CLIs, which have usage windows. Today, hitting the limit mid-deploy strands
   the job; it's recorded (`status = usage_limit`) but nothing acts on it.
   Borrowing bb's provider-retry idea: park the job with the reported reset
   time, pause dispatch for that runtime only, and resume the same session
   after the window resets. This is the difference between an overnight fleet
   stopping at 1am and finishing by morning, which is minder's whole pitch.

4. **Scheduler quality-of-life.** Three small ones: `minder jobs history`
   (the M1 provenance columns already make "what did the nightly scan do the
   last five times" a query — the command just doesn't exist yet); one-shot
   schedules (`at:` a timestamp / `in: 2h`) alongside cron; and `kind: script`
   jobs that run a plain command on schedule with captured output — no agent,
   no LLM cost — for lint sweeps, cleanup, metric export.

5. **Event sinks instead of a plugin system.** bb has a full plugin platform;
   for a single-operator Go CLI that's rejected as overkill. The cheap
   substitute: once M1's typed events exist, a `sinks:` block in jobs.yaml can
   POST matching events to a URL or pipe them to a command. That covers Slack/
   Discord notifications, cost export, and custom glue for a few hundred lines.
   (Gated on closing #524, the optional-auth hole, before anything leaves the
   host.)

Sequencing: this slots after the M1 remainder, and most of it can run in
parallel with M2's view work since it barely touches the TUI. There's also a
freestanding experiment — an "open in bb" action on a job, spawning a bb
thread inside the job's existing worktree — so investigating a failed job gets
a full IDE session while minder keeps ownership of the job state machine,
review, and merge.

## Mental model to keep

If you retain one picture, make it this progression:

1. **M1:** minder starts telling the truth, durably, about what it's doing —
   and exposes that truth through one versioned, read-only door.
2. **M2:** a dashboard walks through that door. It can see everything and
   touch nothing.
3. **M3:** the door gets a small set of labeled, logged, idempotent switches.

And one invariant that survives all three: you can always ignore the entire
control plane, run `minder deploy --foreground` in a terminal, and hit Ctrl-C —
that path is a release gate, tested at every step.

## If you want to go deeper

- The design and its resolved/open decisions:
  [`control-plane-and-tui-plan.md`](control-plane-and-tui-plan.md)
- Task-level sequencing and the test discipline:
  [`control-plane-milestone-breakdown.md`](control-plane-milestone-breakdown.md)
- Why the plan is trustworthy (evidence audit):
  [`research/fable-expedition/01-architecture-truth-map.md`](research/fable-expedition/01-architecture-truth-map.md)
- The event-consistency contract:
  [`research/fable-expedition/04-snapshot-event-consistency.md`](research/fable-expedition/04-snapshot-event-consistency.md)
- How this compares to bb, and what comes after:
  [`../design/bb-exploration.md`](../design/bb-exploration.md)
