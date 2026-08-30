---
title: "ADR 0023 — Handoff packet and pickup verb: completion is a reproducible summary plus a restore, not a machine signal"
status: deferred
date: 2026-08-30
tags: [architecture, workflow, completion, handoff, pickup, tui, human, deferred]
superseded_by:
---

<!-- deferred: ratified in conversation 2026-08-30; flip to accepted on Dustin's sign-off.
     Supersedes the completion-contract direction of [[0022-station-completion-contract]]:
     completion is not a signal for workflow 2; it is a packet + a pickup verb. 0022 stays
     as history (its grammar/signal direction is dead). LLM summaries are a deprioritized
     dogfood; v1 is mechanical naming. -->

## Context

[[0022-station-completion-contract]] recorded a hole: "how does the downstream consumer know
a workflow is ready?" The design conversation closed that question decisively — the answer
is **the operator decides** (the seam is a human `approve` step, D2: "me deciding"), so a
machine-to-machine completion *signal* is not wanted in v1 or even v2.

But a real need survived: **coming back to a finished workflow and picking up the threads.**
The operator leaves for days, returns cold to the TUI, and must re-derive from raw state what
a workflow produced, where it lives, and what is next. Separately: a workflow result may need
handing to **another human on another machine**, with no shared Trigger state and no local
knowledge of the chunk. Both needs are satisfied by a **packet + a verb**, not by an engine
signal.

Constraint that shapes all of it: Trigger stays headless — it never hosts an interactive
session ([[0013-ask-and-resume-instead-of-bail]], [[0016-trigger-owns-proactive-loop]], and
[[0021-step-execution-and-done]]). The human works in their own tool. So the packet's job is
to make that handoff *conversational and ceremony-free*, not to launch sessions for the
human.

## Decision

**A finished workflow produces a handoff packet — a reproducible, human-readable projection
of the run's durable state — and two verbs: `pickup` (recover and resume, on any machine)
and `handoff` (export the packet so another human can get up to speed from a specific
commit, with no Trigger on their side). The completion signal direction of 0022 is
superseded: there is no machine-to-machine "ready" event; the seam is the operator.** *(Direction; mechanism deferred. LLM-generated summaries are a deprioritized dogfood, not v1.)*

### 1. The packet is a projection, not new engine state

The packet is **assembled from what already exists** — nothing new is stored as truth:

- The run record (final step, status, timestamps, ratification/verdict records).
- The step artifacts (charter, red-test verdict, verify-report — the `done.artifact` outputs of [[0021-step-execution-and-done]]).
- The git refs (branch, head commit), the worktree path, and the **diff vs. the default branch** (`main`).

Everything above is inspectable state ([[0002-local-sqlite-source-of-truth]], [[db-schema]]). The **only new thing** is a **prose summary**, and it is **derivable at any time** — generated on demand against the packet.

### 2. Prose is optional and deprioritized; v1 is mechanical naming

- **v1: mechanical naming.** List entries are named mechanically from what the operator specified: **workflow name (derived from the YAML job name) + run state + completion datetime**. A run's "recently completed" entry is `name · succeeded · 2026-08-30 14:03`. No LLM, zero dogfood dependency.
- **Later (deprioritized dogfood):** an on-demand LLM summarizer (cheap model over OpenRouter, the existing runtime seam) generates the packet prose when requested — *outside* any run, a new small capability (an on-demand summarizer, not a step). This is deliberately **not v1**; it is a dogfood first, and only if it earns its keep. The packet shape must not require prose to be useful.

### 3. `pickup` — the operator's verb (all machines, typically local)

A TUI "recently completed" surface: a list of finished runs, each **timestamp + workflow name + branch** (mechanically named), navigable up/down, with a modal for details. Details are the assembled packet: AI prose *when that dogfood lands*; until then, mechanical facts + artifact paths + the diff-vs-main view. Actions:

- **Restore/jump into the worktree** — `cd` to the run's worktree (harvest agent-minder's checkout affordance); the summary document sits in the directory root.
- **Continue** — must **not** mean "Trigger launches an interactive session" (never hosted). It means: restore worktree + hand a **paste-ready orientation snippet** ("you're at ref X on branch Y; the charter is at P; scenario → binding map is in the verify-report") the operator drops into their own orchestrator session. The operator opens the session; the packet just orients it.

### 4. `handoff` — the export verb (another machine, another human)

No shared Trigger state exists across machines (local truth), so the packet must travel in a store the receiver already has: **git**. `handoff` commits the packet — summary document + context pointers — into the repo (a `SUMMARY`/`handoff` file on the chunk branch or at the specific commit) and yields the address: **`repo url`, `commit`, `path`**. The receiving human fetches, reads the summary, and fires their **own interactive agent** (their orchestrator), which reviews the summary and starts working — writing subtasks or farming to sub-agents. **Ceremony-free:** no workflow-2 kickoff, no engine launch, no official trigger. The packet's only job is to make a cold human (or their agent) *conversationally* up to speed.

### 5. What this is not

- **Not a completion-signal event** (0022's direction dies here): no `workflow.ready`, no run-status change, no orchestrator hook. The seam is the operator deciding.
- **Not a new run state or a new bus** ([[0011-internal-pubsub-two-buses]]): the packet is a consumer-facing projection; the verbs are Service operations.
- **Not an interactive-session host**: Trigger never launches the human's session; it hands refs and orientation. (See the anti-conflict with [[0013-ask-and-resume-instead-of-bail]] — there Trigger resumes a run *it* owns; here nothing is resumed inside Trigger.)

## Consequences

- The completion hole of [[0022-station-completion-contract]] is closed by direction: **packet + verb, not signal**. 0022's grammar/signal/event-shape openness is obsolete; this ADR carries its replacement. The naming collision between "handoff" (this verb) and "park-and-handoff" ([[charter-workflow]]'s interactive-step mechanics, [[0021-step-execution-and-done]]) is acknowledged and kept distinct: station-handoff is *within* a workflow; packet-handoff is *after* one.
- v1 is cheap: mechanical list naming + a TUI tab + two verbs; the only new Service operations are a packet assembler and a git-commit wrapper.
- The operator's cold-return flow is now a glance + modal + jump, not a re-derivation forensic exercise.
- Cross-machine, no-Trigger-on-receiver handoff works because the payload is plain git.
- Deprioritized, explicit: LLM prose is a dogfood, never a load-bearing v1 dependency. Mechanical naming is load-bearing.

## Open questions

- Where the summary document lives when committed (worktree root vs. a `handoff/` dir vs. `.trigger/handoff.md`) and whether the commit rides on the chunk branch or a dedicated handoff ref.
- Whether `handoff` should also include the AI prose path (the deprioritized dogfood) or stay mechanical-only until then.
- Whether `pickup`'s modal diff view ships in v1 or only the list + restore (TUI cost).
- Relationship to crash-resume ([[0008-workflows-deterministic-steps]]) and the one-worktree-per-run rule: `pickup`/restore must not collide with a live or parked run's worktree.