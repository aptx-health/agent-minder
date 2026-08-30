---
title: "Harvest: pr-triage Claude Code runtime adapter"
status: accepted
date: 2026-08-29
tags: [harvest, runtime, claude-code, transplant]
source: pr-triage internal/runtime/claudecode (claudecode.go, claudecode_test.go)
related: "[[0003-acp-runtime-seam]], [[0006-secrets-and-agent-permissions]], harvest/pr-triage-runtime-seam.md"
---

# Harvest: pr-triage Claude Code adapter

~218 lines + 434 lines of tests. Second proven runtime; pairs with the opencode
adapter under the same seam (see harvest/pr-triage-runtime-seam.md).

## Invocation shape

```
claude [--agent <name>] -p --output-format stream-json --verbose
       [--permission-mode bypassPermissions] [--model <m>] [--max-turns N] -- <prompt>
```

- `--max-turns` passed only when `Limits.MaxTurns > 0` — claude enforces its own
  turn cap (unlike opencode/codex).
- `--` before the prompt (correct for claude; the *opposite* rule from opencode —
  each runtime's arg conventions live in its adapter).

## The permission-mode decision (deliberate, documented)

Unattended runs need `--permission-mode bypassPermissions`: the default mode
auto-denies non-allowlisted Bash calls, so the agent can't run its verification
toolchain, commit/push, or post comments. Rationale: the agent operates inside an
isolated worktree and risky changes escalate to a human before merging. **For Trigger
this is exactly the [[0006-secrets-and-agent-permissions]] tension** — pr-triage's
answer was "bypass inside the sandbox, humans gate the boundary", not per-tool
allowlists in the daemon.

## ParseResult: terminal-event extraction

Reads stream-json, keeps the **last** `type=="result"` event (claude emits exactly one
per run; overwrite semantics differ from agent-minder's first-wins — one more reason
logs must be per-run). Hardening:

- `is_error` OR `subtype=="error"` ⇒ IsError (belt and suspenders).
- **`stop_reason` double-quote defense**: arrives as RawMessage, may be a JSON string
  *containing* quotes — unmarshal, then `strconv.Unquote`, else `Trim("\"")`. Pinned
  by `TestParseResult_DoubleQuotedStopReason`.
- Pointer fields (`total_cost_usd`, `num_turns`, `is_error`) tolerated missing/empty —
  CLI version drift changes which fields appear.
- Zero cost is valid and reported exactly (CostBasis honesty).
- No terminal result ⇒ error.
- `bufio.Reader.ReadString` — no max-line limit (result events can be huge).

## ClassifyOutcome

`stop_reason == "timeout" || "timed_out"` → **timeout** (claude reports it itself);
`IsError || exitCode != 0` → failed; else success.

## Run hardening

Same skeleton as opencode (context timeout, SIGTERM cancel, PIDCallback, exit-code
convention, stdout+stderr to fresh log file) — the seam's Run contract is identical
across adapters, which is the point of the seam.

## Test coverage (acceptance gate)

BuildArgs; success/error/zero-cost/missing-field parse; double-quoted stop_reason;
long lines; nil/empty classify; non-zero exit; command-not-found; PID callback +
timeout firing.
