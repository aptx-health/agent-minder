---
title: "Harvest index — investigation takeaways"
status: accepted
date: 2026-08-29
tags: [harvest, index]
source: guidance/harvest-map.md
---

# Harvest index

One doc per row of the harvest map: the API surface, the hardening that must survive a
transplant, and (for pattern rows) the rewrite guidance. [[harvest-map]] is the source
of truth for scope; these docs are the "what exactly to carry across" detail.

## Lift close to verbatim

- [[sqliteutil-wal-recovery]] — WAL recovery; the `-wal` deletion ⇒ epoch-rotation marker contract
- [[agentutil-log-parsing]] — stream-json result parsing + the failure-reason taxonomy
- [[event-log-store-first]] — commit-as-publish event log, cursor/epoch invariants
- [[agent-runs-table]] — per-stage/attempt run records; progress vs final-truth split
- [[script-execution-config]] — deterministic `script` steps; timeout kill + reason vocabulary
- [[git-worktree-helpers]] — worktree lifecycle, `worktreeinclude` copier, `setup.sh` hook
- [[checkout-and-auth]] — worktree summon/resume UX mechanics; keyring token storage
- [[pr-triage-runtime-seam]] — shared AgentRuntime seam, CostBasis honesty, capability table
- [[opencode-runtime-adapter]] — OpenCode CLI adapter
- [[claudecode-runtime-adapter]] — Claude Code CLI adapter

## Pattern only (rewrite, don't copy)

- [[config-loader-pattern]] — declarative jobs; sync-with-disable reconciliation
- [[scheduler-tick-pattern]] — tick loop; provenance-based idempotence
- [[stage-executor-pattern]] — deterministic step conductor; v1 routing limits
- [[daemon-api-pattern]] — snapshot-then-stream API; SSE resume on the event log

## Cross-cutting findings

- sqliteutil and the event log are coupled: `-wal` recovery must rotate the log epoch or
  recovery is silent history loss ([[sqliteutil-wal-recovery]], [[event-log-store-first]]).
- agent-minder already has a normalized runtime seam (`internal/runtime/types.go`) with the
  cross-runtime failure taxonomy Trigger's ADR-0003 needs; pr-triage's adapters prove the
  CLI-level version ([[pr-triage-runtime-seam]], [[agentutil-log-parsing]]).

## Fable expedition crosswalk

The five Fable deep-review expeditions (`agent-minder/docs/research/fable-expedition/`)
audited the hardening in these packages and, in several cases, *are* the design behind it:

- [[fable-expedition-crosswalk]] — per-expedition findings mapped onto the harvest
  docs. Highest-value: Expedition IV is the design spec for [[event-log-store-first]]
  (identity trio, durable-vs-ephemeral taxonomy, rejected alternatives); Expedition V
  extends the runtime seam with basis labels, model precedence, and resume-correctness
  rules ([[agent-runs-table]], [[pr-triage-runtime-seam]]).
