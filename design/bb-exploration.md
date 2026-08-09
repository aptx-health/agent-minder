# bb Exploration — What It Does Well and What Minder Should Borrow

Status: research
Last updated: 2026-08-08
Sources: bb repo (github.com/get-bb/bb — VISION.md, system-overview.md), live `bb guide` output
(threads, environments, agent-configuration, providers, automations, plugins, machines,
terminals, projects) from a running bb 0.35.x install, minder milestones 20–22 and 24,
`docs/control-plane-and-tui-plan.md`, `docs/control-plane-milestone-breakdown.md`.

## Framing

bb and minder solve adjacent but different problems:

- **bb** is an agentic IDE. A human sits in front of it, spawns threads, watches them live,
  steers them mid-turn, and forks them. Its center of gravity is the interactive session.
- **minder** is a headless autonomous supervisor. Its center of gravity is the unattended
  deploy loop: GitHub issues in, reviewed PRs out, with the human showing up afterward to
  merge. Interactive babysitting is deliberately out of scope — that's what Claude Code
  (or bb) is for.

So the question is not "how do we become bb" but "which of bb's operational ideas make an
autonomous supervisor more inspectable, more resilient, and easier to live with." Most of
bb's headline features (live steering, pane management, forking, terminals) belong to the
interactive world and should stay there. What transfers is the plumbing underneath:
lifecycle discipline, capability contracts, run history, and recovery behavior.

The good news: minder's Control Plane plan (milestones 20–22) already independently
converges on bb's strongest architectural moves. Several recommendations below are
refinements to planned work rather than new directions.

---

## Part 1 — What bb does well

### 1.1 Thread event model and live state

A bb thread is an append-only stream of typed events — messages, tool calls, file changes,
lifecycle transitions — persisted in server-side SQLite and pushed to clients over
WebSocket. Everything the UI shows derives from this stream; `bb thread log --format json
--after-seq <n>` gives cursor-paginated resume, and `bb thread wait --status/--event` turns
the stream into a scriptable synchronization primitive.

Why it works: one canonical event stream serves rendering, persistence, automation, and
agent-to-agent coordination simultaneously. There is no "parse the raw log to find out what
happened" path.

**Minder status:** this is exactly M1 Waves 3–5. `agent_runs` (v11) landed, the eventbus
(#584, #606) landed, the typed envelope (#608) and state-provider boundary (#607) are the
open remainder. bb validates the design; no course change needed. Two refinements worth
stealing:

- bb's `wait` primitive (`bb thread wait --status idle --timeout`) is trivially cheap once
  you have cursored events, and it converts the API from "observability" into "composable
  automation" — scripts, CI, and other agents can block on minder state. Worth a small M2
  issue: `minder sessions <id> wait --until <state>`.
- bb exposes the same event log at three fidelities (`json`, `minimal`, `verbose`). The
  planned formatted-log-following work (M2 Wave C) should keep the raw-event escape hatch
  next to the pretty view rather than replacing it.

### 1.2 Managed environment lifecycle

This is bb's most directly transplantable subsystem. Managed worktrees have a full
lifecycle contract:

- **`.bb-env-setup.sh`** — a git-tracked setup hook run in every fresh worktree
  (`env bash`, cwd = workspace, sanitized env). Non-zero exit fails provisioning and the
  worktree is removed. Progress is reported as first-class provisioning events.
- **`.worktreeinclude`** — gitignore-syntax allowlist of untracked files (`.env`,
  `certs/`, `!.env.example`) copied from the source checkout into the new worktree,
  after `git worktree add` and before the setup hook. Copy-only, no symlink following,
  never overwrites, failures are reported but non-fatal.
- **Ownership-based cleanup** — a managed environment is removed when no unarchived
  thread uses it. Unmanaged (project-checkout) environments are never touched.

Why it works: worktrees become reproducible and self-cleaning without the user thinking
about them, and the managed/unmanaged distinction gives a crisp rule for what the tool is
allowed to destroy.

**Minder status:** minder has worktree creation, a reaper command, and (post-#612) branch
collision protection, but no setup hook, no untracked-file copy, and cleanup is a manual
sweep rather than an ownership rule. Agents in fresh worktrees routinely burn turns
reinstalling dependencies or failing on missing `.env` files. This is a top recommendation
(see Part 3, item 1).

### 1.3 Provider abstraction and capability resolution

bb treats each agent backend as a queryable catalog, not a string:

- `bb provider list` / `bb provider models <id>` are scoped per machine or environment,
  because what's installed differs per host.
- Model resolution is layered: explicit flag → live parent execution → remembered project
  default → provider-reported default → first catalog model. The resolved choice is
  recorded on the thread.
- Known ACP agents (opencode, grok, hermes…) auto-appear as providers when their CLI is on
  PATH; custom ACP agents are declared in config with explicit capability fields
  (`modelCli`, `reasoningCli`, `nativeReasoning`).
- Per-machine **permission limits** cap the maximum permission mode any thread on that
  machine can run with; a provider that can't operate under the cap simply can't run there.
  Configuration failures become preflight errors, not mysterious runtime behavior.
- bb can even toggle provider-native features (memory, subagents) per provider, applied at
  session start rather than mid-turn.

**Minder status:** `internal/runtime` has the right interface boundary, but runtime/model
selection is stringly-typed: a bad `model:` in jobs.yaml or an uninstalled runtime CLI is
discovered when the agent run fails. There is no preflight, and `agent_runs` records what
was requested more reliably than what was resolved. This directly connects to the
long-standing note that model fields must provably reach actual execution. See Part 3,
item 2.

### 1.4 Subscription-limit recovery

bb's provider-retry plugin recognizes structured Codex/Claude subscription-limit failures,
waits in memory until the reported reset window (plus buffer), then issues one agent-only
"Please continue." turn on the existing provider session. Waits are coordinated per
machine/provider subscription so threads release one at a time; provider-native retries
remain authoritative; a max-wait setting bounds it.

Why it matters for minder specifically: minder's whole value proposition is unattended
operation on subscription-billed CLIs. Today a usage-limit hit mid-deploy strands the job
(`agent_runs.status = usage_limit` records it, but nothing acts on it). An overnight fleet
that parks itself at the limit and resumes at reset is the difference between "ran out at
1am" and "finished by morning." See Part 3, item 3.

### 1.5 Automations: run history, run-now, one-shots, and script mode

bb automations are the closest analog to minder's `jobs.yaml` scheduler, and four details
stand out:

- **`bb automation runs <id>`** — per-automation run history as a first-class query.
  Minder persists `last_run_at` but the question "what did the nightly security scan do the
  last five times" requires joining jobs by provenance manually. (M1's `source_type/name`
  provenance columns, #571/#602, were built for exactly this — the query surface just
  doesn't exist yet.)
- **`run` (run-now)** — manually fire a scheduled automation. Already planned as M3 Wave
  II; bb confirms the shape.
- **One-shot schedules** — `--at <iso>` and `--in 30s|5m|2h|1d` alongside cron. Cheap to
  add to the cron parser and genuinely useful ("re-run the dependency updater in 2h when
  the registry outage clears").
- **Script automations** — stored scripts (bash/node/python) that run on schedule with
  captured output and no model usage. Minder's scheduler currently assumes every job is an
  agent. A `kind: script` job type would cover linting sweeps, cache warming, artifact
  cleanup, and metric export without paying LLM cost or agent latency.

Also worth noting: `automation update` replaces execution config atomically (all agent
fields or a complete script — no stale-field survival across mode changes). That's a good
validation rule for jobs.yaml reload (M3 Wave III).

### 1.6 Layered agent configuration

bb layers instructions provider-agnostically: `~/.bb/AGENTS.md` (user-global) →
`<workspace>/.bb/AGENTS.md` (repo) → skills (builtin → user → project, with same-name
override and collision-drop rules) → per-thread context variables. The precedence rules
are explicit and documented, and skills double as slash commands.

**Minder status:** minder's four-layer guidance model (CLAUDE.md facts / context providers
/ agent contracts / house style) is arguably cleaner for the autonomous case, and the
contract resolution chain (repo → user → built-in registry) already mirrors bb's skill
precedence. No structural change recommended. One gap: minder has no *user-level*
cross-repo instruction layer ("in all my repos, never bump major versions"). A
`~/.agent-minder/AGENTS.md` appended by the house-style renderer would be a few lines of
code. Lessons partially cover this ground, but they're learned rather than declared.

### 1.7 Queued messages vs. live steering

bb distinguishes `tell --mode steer` (inject into the active turn) from `--mode queue`
(deliver when the agent is free), with a full queue CRUD surface (list, reorder, group,
send). The queue is the part that transfers to minder: it's asynchronous, durable, and
consumed at a well-defined boundary — no interactive session required.

A minder-shaped version: `minder jobs tell <job> "<directive>"` appends a durable directive
event; the stage executor drains pending directives into the context assembly of the next
stage (and the review stage). The operator glances at the TUI over coffee, notices the
agent heading toward a migration, queues "don't touch migrations, use the existing
adapter," and the correction lands at the next stage boundary. This preserves the headless
model — no PTY, no mid-turn injection, full audit trail through the event stream. Live
mid-turn steering, by contrast, is interactive babysitting and should stay out; the
existing `checkout` → continue-interactively flow is minder's answer when a human wants
hands on the wheel.

### 1.8 Machines and the execution-host boundary

bb's topology: a central server owns state and routes commands; host daemons connect
outward over WebSocket, provision workspaces, run providers, and post events back.
Machines carry identity, permission limits, provider-CLI install status, and auto-update
with exponential backoff. Command side effects settle when the daemon returns an RPC
result, so lifecycle operations are async-safe.

**Minder status:** the worker-topology ADR (expedition 02) and the M1/M2 plan already
adopt the valuable core of this — per-deployment workers exposing Unix sockets, a host
aggregate API, clients that never import supervisor internals. bb's confirmations for that
plan: keep workers as child processes (crash isolation), make discovery registration-based,
and treat "capability negotiation per host" (which runtimes are installed where) as part of
the machine record, not something clients probe ad hoc. Full remote-machine execution
(minder supervisor on the Mac dispatching to Linux workers) is a real future milestone but
should wait until M2/M3 land; the API contract being built is exactly the seam it needs.

### 1.9 Plugin architecture

bb plugins are full-trust TypeScript packages with a large in-process API surface: CLI
subcommands, cron schedules, background services, HTTP routes, storage, settings, lifecycle
hooks, UI slots. It's impressive and it's the right call for an IDE platform.

It is the wrong call for minder. A Go CLI embedding a script runtime, a plugin store, and a
stability contract for a one-operator tool is a maintenance tax with no payer. What minder
should take is the *shape of the extension points without the plugin machinery*: the
lifecycle hooks bb exposes (`thread.created/idle/failed/deleted`) map to minder's job
lifecycle, and once the M1 event envelope exists, an **event-driven webhook/exec sink** —
a jobs.yaml block that POSTs typed events to a URL or pipes them to a command, filtered by
event type — buys Slack notifications, cost export, and custom approval glue for a few
hundred lines. The Discord setup doc in this repo suggests the appetite already exists.

### 1.10 Everything else (noted, not recommended)

- **Terminals** (persistent PTYs), **panes**, **forking with session clone**,
  **sections**, **bb connect** (tunneled remote access), **file openers, themes,
  mention providers** — interactive-IDE surface, out of scope for minder.
- **Tasks/Memory/Docs plugins** — minder's GitHub-issues-as-queue and lessons system
  occupy this ground already.
- **Hidden threads** — a nice detail (background workers addressable by ID but excluded
  from lists/attention); minder's equivalent concern is handled by job status filtering.

---

## Part 2 — Where milestones 20–22 and 24 already stand

Snapshot as of 2026-08-08:

| Milestone | State | Open remainder |
|---|---|---|
| 20 — M1 Coordinator & Observability | 11 closed, 3 open | #608 typed event envelope, #607 state-provider boundary, #606 eventbus retention (branch in flight) |
| 21 — M2 Multi-Deployment TUI | 0 closed, 1 filed | #621 `minder sessions` picker/detail (blocked on M1-19/20/22, M2-02/03 unfiled) |
| 22 — M3 Safe Operations | 0 closed, 1 filed | #622 `sessions drain` (blocked on M3-01/02) |
| 24 — Lantern Cleanup | 9 closed, 1 open | #524 daemon API optional auth |

Reading bb against this plan, the plan holds up well. bb independently arrives at the same
pillars: append-only typed events with cursors (M1 Wave 4), snapshot + resume (expedition
04), API-only clients (M2), scoped auditable commands (M3), worker processes with a
registry (expedition 02). Nothing in bb argues for reordering M1→M3.

What bb adds is a *second wave* of operator-experience features that slot naturally after
M1's remainder and alongside/after M2 — that's the proposed follow-up milestone below.

One sequencing note: #524 (API allows no auth) should close before any M2 client work
makes the API more attractive to expose, and before recommendation 6 (event sinks) ships
anything off-host.

---

## Part 3 — Recommendations

Ranked by leverage-per-effort for the headless-autonomous mission. Items 1–3 are the core
of a proposed follow-up milestone; 4–7 are smaller riders; 8 is a standalone experiment.

### 1. Worktree lifecycle contract (setup hook, include-file copy, retention policy)

The bb feature with the highest direct payoff. Scope:

- `.agent-minder/setup.sh` (or a `setup:` key in onboarding.yaml): git-tracked hook run
  after worktree creation, before the agent starts. Non-zero exit fails the job into
  `blocked` with a clear failure_detail instead of letting the agent flail. Emit start/
  finish/fail as typed events.
- `.agent-minder/worktreeinclude`: gitignore-syntax allowlist for copying untracked files
  from the deploy repo into the worktree (bb's exact semantics: copy-only, no overwrite,
  no symlinks, non-fatal misses, run before the setup hook).
- Retention policy replacing sweep-only cleanup: worktree removed automatically on `done`
  (merged); retained with a TTL on `bailed`/`suspect` review ("preserve for debugging");
  reaper enforces TTLs instead of guessing. Dirty/unpushed protection from the TUI plan
  applies here too.

Effort: small-medium. Touches `internal/supervisor` worktree setup and `internal/reaper`.
No schema change beyond maybe a `worktree_state` column or event types.

### 2. Runtime capability preflight and resolved-execution recording

Make each `AgentRuntime` implementation answer: is the CLI installed (and what version),
which models/aliases does it accept, does it support structured output / budget flags /
session resume. Then:

- Validate every job at activation: unknown runtime, unavailable CLI, or unsupported model
  fails fast into `blocked` with a preflight error event — before a worktree is created or
  a turn is burned.
- Record the *resolved* runtime, model, and version in `agent_runs` (columns exist;
  guarantee they're the resolved values, not the requested strings).
- `minder doctor` (or `agents check`): print the capability matrix per runtime, mirroring
  `bb provider list/models`.

This closes the "does the model field actually reach execution" loop permanently and is a
precondition for trusting per-job model overrides in jobs.yaml. Effort: medium; mostly
additive interface methods on the three runtimes.

### 3. Usage-limit park-and-resume

When a run terminates with a structured subscription-limit signal (already detected —
`status = usage_limit`), park the job in a `waiting_limit` state with the reset timestamp,
pause dispatch for that runtime (not the whole deployment — mirrors #622's requirement
that budget-pause and manual drain be independently observable, adding a third orthogonal
reason), and resume the session (`--resume`/session ID from `agent_runs.session_id`) after
reset + buffer, one job at a time per provider. Cap with a configurable max-wait.

This is bb's provider-retry translated to supervisor semantics, and it's the single
biggest step toward "start a deploy at 6pm, wake up to finished PRs." Effort: medium.
Depends on the typed event envelope (#608) for clean observability of the parked state.

### 4. Scheduler riders: run history, one-shots, script jobs

Three small additions to `internal/scheduler` + jobs.yaml, all bb-validated:

- `minder jobs history <name>`: list past activations of an automation via the provenance
  columns already landed in M1 (#571/#602). Read-only, no schema change.
- One-shot schedules: `at: 2026-08-09T02:00` / `in: 2h` alongside `schedule:` cron.
- `kind: script` jobs: run a stored command on schedule, capture output, record a run —
  no runtime, no LLM cost. Watch the #604 class of bug: mode conversion between
  agent/script must atomically replace execution config, per bb's update rule.

(Run-now is deliberately omitted here — it's already M3 Wave II through the command
envelope, and doing it earlier as a side door would undercut the audit-trail design.)

### 5. `wait` and `--json` as automation primitives

`minder sessions <id> wait --until drained|idle|done [--timeout]`, plus the already-planned
stable `--json` everywhere. bb's lesson: once every command is scriptable and blockable,
external automation (cron, CI, other agents) composes with the supervisor for free. Cheap
rider on the M2 sessions client.

### 6. Event sinks (webhook/exec) instead of a plugin system

After #608 lands: a jobs.yaml `sinks:` block subscribing to typed event patterns
(`job.review.completed`, `job.done`, `deployment.budget.*`) and delivering to a URL or a
local command. This is the 80% of bb's plugin value (notifications, cost export, custom
glue) at 2% of the cost, and it dogfoods the event envelope. Requires #524 (auth) resolved
first for anything non-local.

### 7. User-level instruction layer

`~/.agent-minder/AGENTS.md` appended after house style in every prompt, mirroring bb's
data-dir AGENTS.md. Trivial effort; gives cross-repo standing orders a declared home
instead of hoping lessons learn them.

### 8. bb as an optional interactive companion (experiment, not a milestone)

Minder keeps ownership of the job state machine, GitHub policy, review, and merge; bb
supplies the interactive deep-dive when a human wants one. Concretely: a checkout/sessions
action "open in bb" that runs `bb thread spawn --environment <worktree-path> --prompt
<job context>` against the job's existing worktree, so investigating a bailed or suspect
job happens in a full IDE session with the worktree's state intact. Zero coupling — it's
one exec of an optional CLI, gated on `bb` being on PATH. Worth a spike after M2's shared
checkout services exist, since that's the natural place to hang the action.

### Explicitly not recommended

- **Live mid-turn steering / PTY attach** — interactive babysitting; belongs in Claude
  Code/bb, reachable via checkout. (Queued stage-boundary directives, a possible M3+
  follow-up to recommendation 5's primitives, are the headless-compatible version if
  demand appears; they'd ride the M3 command envelope.)
- **A plugin system** — see recommendation 6.
- **A central server that owns all state** — minder's inverted topology (standalone
  workers, optional aggregation) is the right fit for foreground-first operation and is
  already decided in the ADR.
- **Web UI, terminals, panes, forking, sections, bb connect** — IDE surface.
- **Tasks/memory/docs analogs** — GitHub issues and lessons already occupy this ground.

---

## Part 4 — Proposed follow-up milestone shape

Suggested: one new milestone, sequenced after the M1 remainder (#606–#608) and #524, and
runnable in parallel with M2 view work since it barely touches the TUI.

**Milestone: "Operator Experience — Lifecycle & Resilience" (candidate M25)**

| # | Issue | Rec | Size |
|---|---|---|---|
| 1 | Worktree setup hook + provisioning events | 1 | M |
| 2 | worktreeinclude untracked-file copy | 1 | S |
| 3 | Worktree retention policy + reaper TTL enforcement | 1 | M |
| 4 | Runtime capability preflight + `minder doctor` | 2 | M |
| 5 | Resolved runtime/model/version guaranteed in agent_runs | 2 | S |
| 6 | Usage-limit park-and-resume per runtime | 3 | M-L |
| 7 | `jobs history` via provenance | 4 | S |
| 8 | One-shot schedules (`at:`/`in:`) | 4 | S |
| 9 | `kind: script` scheduled jobs | 4 | M |
| 10 | Event sinks (webhook/exec) over typed envelope | 6 | M |
| 11 | User-level AGENTS.md layer | 7 | S |

Items 1–3 and 4–5 are dependency-free of each other; 6 wants #608; 10 wants #608 and #524.
That gives the usual autopilot-friendly property: several dependency-free roots to label
agent-ready immediately (per the dep-sequencing rule, only the roots get the label).

Recommendation 5 (`wait`) belongs inside M2's #621 scope rather than the new milestone;
recommendation 8 (bb companion) is a spike issue, unmilestoned, after M2 Wave A.

## Bottom line

bb's architecture largely confirms the Control Plane plan minder is already executing —
typed events, snapshot+cursor resume, API-only clients, scoped commands. The genuinely new
material is operational: a worktree lifecycle contract, runtime capability preflight,
usage-limit park-and-resume, and small scheduler riders. Those four turn the autonomous
supervisor from "runs unattended until something environmental goes wrong" into "runs
unattended through the things that go wrong," which is the mission. The interactive layer
— steering, panes, forking — is bb's product, not minder's, and the right integration is
an optional "open in bb" escape hatch, not feature parity.
