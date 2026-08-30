---
title: "Trigger — charter (v1)"
status: draft
date: 2026-08-29
tags: [charter, scope, product]
---

# Trigger — charter (v1)

The scope contract for the first version. Draft — Dustin ratifies before implementation.

## Outcome

A single local daemon that:

1. Runs **declarative cron jobs** (scheduled agent or script runs).
2. Runs **triggered one-offs** — a job fired by an external event, not the clock.
3. Can be **spun up from an external trigger** (an inbound signal starts work).
4. Drives **swappable agent runtimes** through ACP (opencode and Claude Code first).
5. Keeps **local SQLite as the single source of truth**.
6. Exposes a **thin TUI now, a GUI later**, both reading a local API — never the DB
   directly.

The dogfood target: run Trigger's own scheduled and triggered jobs against opencode,
using Dustin's OpenCode/OpenRouter credits, and watch them in the TUI.

## Scope boundaries — explicitly OUT of v1

- **No review agent, no auto-merge.** The PR is the review surface. pr-triage or a
  human gates it. (See [[project_minder_vs_pr_triage]] reasoning.)
- **No dependency graph resolution.** That was agent-minder's autopilot machinery.
- **No lessons system in v1.** Deprioritized per BYOA notes; revisit later if wanted.
- **No GitHub-centric state.** GitHub is one trigger adapter, never app state.
- **Not a platform.** No plugin marketplace, no multi-tenant server, no web console.

## Constraints (the invariants — see ADRs)

- Local SQLite is the source of truth; every external system is an I/O adapter
  ([[0002-local-sqlite-source-of-truth]]).
- Runtimes are reached through ACP; adapters are thin ([[0003-acp-runtime-seam]]).
- The daemon owns all state and logic; interfaces read a local API
  ([[0004-daemon-interface-split]]).
- Triggers are an abstraction; GitHub, cron, webhook, and manual are implementations
  ([[0005-trigger-source-agnostic]]).
- Config is declarative and versionable (YAML), resolved once at load.
- **Platform: macOS only for v1.** Dustin and coworkers are all on Mac; secrets use the
  macOS Keychain ([[0006-secrets-and-agent-permissions]]). Multi-OS/arch is out until a
  real need appears — extract-on-force.

## Prior decisions this rests on (harvested lessons)

- Local-state-as-source-of-truth and one-way projection — pr-triage ADR-0006.
- Deterministic-first, AI assists, human decides — pr-triage ADR-0007.
- The Fable-review discoveries in agent-minder (hidden bugs found by deep review)
  argue for harvesting *hardened* code, not rewriting it blind. See
  [[docs/guidance/harvest-map]].

## Task breakdown (rough — refined during planning)

1. Repo + module skeleton; doc discipline in place.
2. SQLite schema + migration harness (harvest `sqliteutil`).
3. Config loader (declarative jobs: cron, trigger, one-off).
4. Daemon core: job store, scheduler tick, local API server.
5. ACP runtime client + opencode adapter (copy from pr-triage as the proven start).
6. Trigger abstraction + first adapters (manual/CLI, cron, external webhook).
7. Thin TUI reading the local API.
8. Dogfood: schedule and trigger real opencode jobs.

## Verification criteria (what "v1 done" means)

- A declarative cron job fires on schedule and runs an opencode agent to completion,
  visible in the TUI, with state recorded in local SQLite.
- A one-off job fired by an external webhook runs to completion the same way.
- Killing and restarting the daemon loses no state (source-of-truth invariant holds).
- Swapping opencode for Claude Code is a config change, not a code change.

## Open decisions (do not block the charter)

See [[docs/research/open-questions.md]] — repo boundary, autopilot fan-out timing,
external-trigger transport, ACP Go client availability.
