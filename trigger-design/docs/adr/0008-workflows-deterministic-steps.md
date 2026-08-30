---
title: "ADR 0008 — Workflows: deterministic, declarative ordered steps"
status: accepted
date: 2026-08-29
tags: [architecture, workflow, pipeline, steps, determinism]
superseded_by:
---

## Context

A trigger should be able to start a **workflow**, not just a single agent run: implement →
test → do-something-else, where each step can be a different agent with its own model and
agent type (or a script). agent-minder did this with its stage executor (autopilot →
review); pr-triage does a lighter version with tier routing. Dustin flags this as the
**least well-thought-out and most fragile** part if done wrong, and explicitly does not want
to recreate the heavy multi-agent orchestration engines other tools ship. This ADR fixes
the *shape and the limits* now, so the fragile version never gets built. The detailed step
schema is a design task ([[open-questions]] R3), not decided here.

## Decision

A job may be a **workflow: an ordered sequence of steps.** Four rules keep it robust and
simple:

1. **Sequencing is deterministic and declarative.** The next step is decided by config, not
   by an LLM. A dumb engine walks the ordered steps. Agents are smart *inside* a step; the
   conductor is dumb and predictable. (Deterministic-first — pr-triage ADR-0007.)
2. **A step is self-describing, and may be an agent OR a deterministic script.** Each step
   declares its own `kind`:
   - `script` — a plain deterministic command (run tests, lint, build, deploy, a data
     transform). No LLM. Carries its command, timeout, env, and work dir.
   - `agent` — an agent invocation with its own runtime, model, and agent type.
   Both carry a secrets/permissions block ([[0006-secrets-and-agent-permissions]]). **Prefer
   a `script` step wherever the work is mechanical; reach for an `agent` step only where
   genuine judgment is needed** — deterministic-first at the step level (pr-triage ADR-0007).
   A workflow is normally a mix: scripts for the mechanical stages, agents for the judgment
   ones. Steps are heterogeneous by design.
3. **Steps are durable and resumable.** Each step run is recorded as local state
   ([[0002-local-sqlite-source-of-truth]]) — one run record per step/attempt (harvest
   agent-minder's `agent_runs` shape). Two failure modes are handled **separately**:
   - **Infrastructure failure** (daemon crashes / is killed mid-step): mechanical resume
     from the last completed step; re-run the interrupted step. No decision, no routing.
   - **Step-logic failure** (the step ran but failed its job — tests red, agent bailed,
     script non-zero): a declared outcome that follows `on_failure` routing (rule below).
   Conflating the two is a known pipeline bug (retrying a real failure forever, or
   escalating a mere crash); keep them distinct in the state model.
4. **Context passes forward, bounded and explicit.** A step declares what it receives from
   prior steps. No implicit global scratch space.

**v1 stays linear.** Routing is limited to simple per-step conditions: `on_success` (default:
next step) and `on_failure` (stop | escalate | a named step). Explicitly OUT of v1: arbitrary
DAGs, parallel fan-out/join, loops, and dynamically generated steps.

**The data model carries steps from day one** even when a job has exactly one step — so
multi-step is not bolted on later. Execution complexity is added incrementally, only on
evidence.

## Consequences

- Robustness by construction: a deterministic conductor over durable steps cannot wander,
  and resumes cleanly after a crash. This is the direct defense against the fragility Dustin
  named.
- The "avoid heavy orchestration" goal is enforced by the linear-first limit, not by
  willpower — richer routing requires a new ADR, so it cannot creep in.
- The config schema (R3) must express a step list with per-step runtime/model/agent/secrets
  and the two routing conditions — fold this into the schema design now.
- Harvest targets: agent-minder stage executor (pattern), `agent_runs` table (durable
  per-step records), and its script-execution config
  (`script_command`/`script_timeout`/`script_env`/`script_work_dir` on jobs and schedules)
  for `script` steps. See [[harvest-map]].
- Open door: DAGs / parallelism / loops are deferrable behind the same step model; when a
  real workflow needs one, it is an additive ADR, not a redesign.
