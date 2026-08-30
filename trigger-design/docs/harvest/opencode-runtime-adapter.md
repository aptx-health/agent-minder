---
title: "Harvest: pr-triage OpenCode runtime adapter"
status: accepted
date: 2026-08-29
tags: [harvest, runtime, opencode, transplant]
source: pr-triage internal/runtime/opencode (opencode.go, opencode_test.go), docs/opencode-runtime.md
related: "[[0003-acp-runtime-seam]], harvest/pr-triage-runtime-seam.md"
---

# Harvest: pr-triage OpenCode adapter

~195 lines, self-contained, plus 269 lines of tests. Proven working in pr-triage's
daemon — the fastest path to dogfooding per the harvest map. Copy first; migrate to
ACP later.

## Invocation shape

```
opencode run --format json [-m <model>] [--dir <workdir>] [--agent <name>] <prompt>
```

- Prompt is the **trailing positional message** — do NOT use a `--` separator;
  opencode's yargs diverts everything after `--` away from the message positional.
- `--dir` sets the workdir (no need to `cmd.Dir`, but the adapter does both).
- **Model must be `provider/model` form** — adapter rejects slash-less models loudly at
  invocation time (opencode otherwise *silently drops* the string).
- Empty model ⇒ omit `-m` ⇒ agent definition's own model wins (agent definitions can
  carry models; config overrides when set).

## Run hardening

- Timeout via `context.WithTimeout` + `cmd.Cancel` sending **SIGTERM** (graceful,
  not SIGKILL).
- `PIDCallback` at launch for daemon kill-switches.
- `ExitError` → `(code, nil)` per the seam's exit-code convention; launch failure →
  `(-1, err)`; `TestRun_CommandNotFound` pins this.
- stdout+stderr → the run's fresh log file.

## ParseResult: accumulate, don't scrape

NDJSON stream; per `step_finish` event: +1 turn, `cost` (pointer, may be null)
summed, `reason` kept as StopReason. `text` events concatenated in order into
Summary (the review write-up). Hardening:

- **No `step_finish` at all ⇒ error**, not success-with-zero — catches "log is empty
  garbage / CLI never actually ran".
- Non-JSON lines skipped silently.
- `CostBasis: exact` — cost comes from the runtime's own structured events, never
  scraped text. Multi-step runs sum per-step costs; each step is one turn.
- Reader-based (`ReadString`) — no line-length limit trap (contrast: agentutil's
  scanner needed a 1MB buffer for the same reason).

## ClassifyOutcome

Minimal: `res == nil` → error; `IsError || exitCode != 0` → failed; else success.
No timeout detection from the stream (opencode gives none) — timeout is enforced by
the caller's context.

## Ops notes (docs/opencode-runtime.md)

- Credentials live in **opencode's own config** (`opencode auth`), not the daemon env
  — opencode is a shared server, so provider creds are deployment-level and env
  freezes at server start. Smoke test: `opencode run -m <model> "hi"` in a shell.
- Runtime selection belongs in routing/per-job config; top-level `runtime:`/`model:`
  fields were display-only traps in pr-triage — don't reproduce that split.
