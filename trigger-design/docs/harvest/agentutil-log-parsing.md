---
title: "Harvest: agentutil — stream-json log parsing"
status: accepted
date: 2026-08-29
tags: [harvest, parsing, failure-signals, transplant]
source: agent-minder internal/agentutil (result.go, result_test.go)
related: "[[0009-cross-tool-boundary-shared-conventions]], [[0003-acp-runtime-seam]]"
---

# Harvest: `internal/agentutil`

~60 lines, one function: `ParseAgentLog(logPath) (*AgentResult, error)` — scans a
Claude Code stream-json log line-by-line and returns the `type=="result"` event.
Used by onboarding validation and `resume`. Transplant as-is.

## What the struct captures

`SubType`, `IsError`, `NumTurns`, `TotalCostUSD`, `StopReason` (RawMessage — CLI
sometimes emits non-string), `Result`, `PermissionDenials` (RawMessage — shape varies),
`SessionID`. Keep the `json.RawMessage` fields loose; the CLI's shape is not a contract.

## Hardening to preserve

1. **1MB max line** (`scanner.Buffer(…, 1024*1024)`) — result lines carry full final
   text; default 64KB scanner truncates and corrupts JSON.
2. **Malformed lines are skipped, not fatal** — real logs contain interleaved noise;
   parse errors on non-result lines are ignored.
3. **`(nil, nil)` means "no result event found"** — distinct from an error. Missing
   result ≠ failed parse; the caller decides what absence means (e.g., job still
   running vs. crashed).
4. **First result event wins** — later duplicates are ignored.
5. **Empty path → `(nil, nil)`**, nonexistent path → error.

## Where failure-signal detection actually lives now

Not in agentutil. agent-minder grew `internal/runtime` (the ADR-0003 seam): each
adapter implements `ClassifyOutcome(Result, Limits) Outcome`, with a normalized
taxonomy in `internal/runtime/types.go`:

- `Outcome.Status`: `""` | `"warning"` (permission denials — still check for PR) | `"failed"`
- `FailureReason` vocabulary: `max_turns`, `max_budget`, `error`, `permissions`,
  `usage_limit`, plus supervisor-level reasons (`preflight`, `setup_hook`,
  `branch_in_use`, `pr_required`, `timeout`, `bailed`)
- `Result` is the normalized summary (SessionID for resume, FinalText, Native raw
  event for forensics); `Limits{MaxTurns, MaxBudgetUSD, Timeout}`; zero = no cap.

**Takeaway for Trigger:** harvest the *taxonomy* (failure_reason vocabulary + warning
vs failed distinction) into the runtime seam from day one — it is the cross-tool
boundary ADR-0009 needs. The raw-log parser is only needed for runtimes without a
queryable session result.
