---
title: "Harvest: pr-triage runtime seam (shared adapter context)"
status: accepted
date: 2026-08-29
tags: [harvest, runtime, adapters, acp, transplant]
source: pr-triage internal/runtime (runtime.go, types.go, registry.go, config.go), docs/runtime-capability-table.md
related: "[[0003-acp-runtime-seam]], [[0009-cross-tool-boundary-shared-conventions]]"
---

# Harvest: pr-triage `internal/runtime` seam

~450 lines total. Proven-working in pr-triage's daemon; the fastest path to
dogfooding Trigger. Lift the seam + both adapters together — they share tests idioms
and types. Migrate to ACP ([[0003-acp-runtime-seam]]) after.

## The interface (three methods, no more)

```go
type AgentRuntime interface {
    Name() string
    Run(ctx, Invocation, logFile io.Writer) (exitCode int, err error)
    ParseResult(log io.Reader) (*Result, error)
    ClassifyOutcome(res *Result, exitCode int) Outcome
}
```

Contract subtleties (both paid for):
- `Run` convention: **non-nil error = couldn't run at all**; `(exitCode≠0, nil)` = ran
  and exited non-zero. Callers must distinguish "never started" from "started and
  failed".
- Raw log goes to `logFile` as produced; parsing is a separate `ParseResult` pass
  (enables re-parse of old logs without re-running).
- `PIDCallback(pid)` fires at launch — the daemon needs the PID to enforce limits the
  runtime won't.

## Types worth copying verbatim

- **`CostBasis` honesty rule**: `exact | estimated | unavailable | runtime-defined`;
  `Result.Validate()` rejects empty. A genuine $0 must never be indistinguishable from
  "never measured" — this killed cost dashboards in pr-triage before the rule.
- **Turns are not comparable across runtimes** — only `turns` vs *that run's own*
  `Limits.MaxTurns` is meaningful. Never average/compare raw turn counts.
- `Outcome`: `success | failed | timeout | error` — timeout is its own outcome because
  the kill path differs.
- `Limits{Timeout, MaxTurns}` + rule: **an adapter must either enforce a limit itself
  or make clear it doesn't — never silently ignore it.**
- `Invocation` carries `Workdir`, `PIDCallback`; registry self-registers via `init()`
  and **panics** on nil/empty/duplicate names (programmer error at startup).

## Config resolution (runtime + model)

`flag → repo config → user config → default`, returning `{Value, Source}` where source
is one of `flag|repo|user|default`. Source travels with the resolved value — this is
the [[config-resolve-once]] pattern in miniature. Candidate paths include
`.pr-triage/`, `.pr-triage.yaml/.yml`, `~/.config/pr-triage/`. Unknown runtime names
are config *bugs*, validated eagerly, not "fall back to default".

## Capability table (docs/runtime-capability-table.md)

Declare per-runtime capabilities statically, never probe at runtime. **Expedition V's
conformance audit ([[fable-expedition-crosswalk]]) is the deeper version of this table —
it adds resume-needs-workdir, usage-limit signal quality, permission denials, and
shared-process rows, plus basis-label rules for everything reported.**

| Runtime | Cost | Self-enforces | Quirks |
| --- | --- | --- | --- |
| claude-code | exact | max-turns/budget via CLI flags; tool allowlist; resume needs workdir (sessions per-project-dir) | `stop_reason` arrives double-quoted |
| codex | estimated (per-model price table; no terminal cost field) | nothing — adapter must watch the stream | no tool allowlist (sandbox only) |
| opencode | exact (sum of `step_finish.cost`) | nothing — caller enforces turns/budget | needs `provider/model`; shared server → creds are deployment-level, env frozen at server start |

**Rule of thumb: never advertise a limit (timeout, allowlist, budget) the adapter
doesn't actually enforce.**

## Also from pr-triage docs

- One **fresh log file per run**, never append (multi-run logs break parsing; each
  runtime normalizes "multiple result events" differently — sum/first/last).
- `num_turns` semantics differ per runtime (opencode: 1 per `step_finish`; claude:
  its own counter) — normalize at the adapter boundary, compare only via limits.

## Expedition V additions over this seam

agent-minder's three-adapter conformance audit found leaks the two-adapter seam hides:
`turn_basis` labels (turns mean 3 different things), resume-must-carry-workdir+env+limits
(all three adapters shipped broken), first-wins vs last-wins vs accumulate on multi-run
logs (fresh log per run is the only safe rule), and tool-input redaction beyond
truncation. See [[fable-expedition-crosswalk]] §Expedition V.
