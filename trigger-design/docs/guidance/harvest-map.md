---
title: "Harvest map — what to lift from agent-minder and pr-triage"
status: draft
date: 2026-08-29
tags: [guidance, harvest, reuse]
---

# Harvest map

The rewrite risk is losing hardened edge-case handling — the code that only exists because
something broke once (the Fable deep-review discoveries in agent-minder are exactly this
class of hidden-bug fix). The mitigation is to **transplant proven packages**, not rewrite
them blind. This map lists what to lift, from where, and how much trust it carries.

## Lift close to verbatim (proven, well-bounded)

| From | Package / area | Why it is worth transplanting |
| --- | --- | --- |
| agent-minder | `internal/sqliteutil` | WAL recovery, stale -shm/-wal cleanup, `OpenWithRecovery`. Restart-safety is a v1 invariant. |
| agent-minder | `internal/agentutil` | Agent-log / stream-json parsing, failure-signal detection. |
| agent-minder | event log (store-first publish) | Durable event log with commit-as-publish; the future TUI/GUI stream depends on it. |
| agent-minder | `agent_runs` table shape | Durable per-step/attempt run records; the resumability basis for workflows ([[0008-workflows-deterministic-steps]]). |
| agent-minder | script execution config | `script_command`/`script_timeout`/`script_env`/`script_work_dir` for deterministic `script` steps ([[0008-workflows-deterministic-steps]] rule 2). |
| agent-minder | `internal/git` worktree helpers | Worktree add/list, `worktreeinclude`, `setup.sh` run. Only if jobs use worktrees. |
| agent-minder | `checkout` command + `internal/auth` | Summon a job's worktree locally and jump to the PR (keep in the TUI); OS-keyring token storage (feeds [[0006-secrets-and-agent-permissions]]). |
| pr-triage | opencode runtime adapter | Proven-working; the fastest path to dogfooding. Copy first, migrate to ACP after. |
| pr-triage | Claude Code runtime adapter | Second proven runtime. |

## Lift the pattern, not the code (rewrite cleaner)

- **Config loader** — agent-minder's `jobs.yaml` scheduler config is the right shape
  (cron / trigger / one-shot), but Trigger's schema should be redesigned around the
  trigger abstraction ([[0005-trigger-source-agnostic]]), not copied.
- **Scheduler tick** — the 30s refill/tick loop pattern is sound; reimplement against the
  new job store.
- **Stage executor** — agent-minder's stage executor is the right pattern for the
  deterministic step conductor ([[0008-workflows-deterministic-steps]]), but reimplement it
  linear-first with the v1 routing limits; do not copy the autopilot/review-specific logic.
- **Daemon HTTP API + client** — pattern is proven; rebuild narrow and explicit per
  [[0004-daemon-interface-split]].

## Do NOT harvest

- Autopilot supervisor, dependency graphs, review pipeline, auto-merge, lessons,
  onboarding, reaper — all out of v1 scope ([[0001-purpose-and-scope]]).
- Any GitHub-as-state code. GitHub enters only as a trigger adapter and an inbound signal.

## Method

For each "lift verbatim" item: copy the package, run its existing tests against the new
module, and only then adapt. A transplanted package that still passes its own tests carries
its hardening with it. A rewritten one starts at zero hardening — reserve that for the
pattern-only items.

## Farming the harvest to side agents

Each "lift verbatim" row is a **discrete, verifiable task** suitable for an opencode side
agent (once the Trigger repo exists) — it keeps the mechanical transplant off the main
session's context. A self-contained task per row:

> Copy `~/repos/agent-minder/internal/<pkg>` into the Trigger module as
> `internal/<pkg>`, fix the import path, run `go test ./internal/<pkg>/...`, and report the
> result. Do **not** adapt behavior — a clean copy that passes its own tests is the goal.
> If tests fail, report why; do not paper over them.

Rules for the farmed work:
- One package per task; the package's own passing tests are the acceptance gate (this is the
  hardening carrying across — see Method).
- "Lift the pattern" rows are **not** farmed as copies — they are rewrites against the new
  design and belong with the person/agent doing that module, not a mechanical transplant.
- The agent reports pass/fail and any test it could not carry; a human/main session decides
  on failures rather than the side agent forcing green.

