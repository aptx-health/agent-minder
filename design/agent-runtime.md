# AgentRuntime contract

Status: proposed. Lands with milestone 17. Codex is out of scope; gaps for Codex are tracked in milestone 18.

## Purpose

Extract the agent **doer** execution boundary so the supervisor stops knowing about Claude Code specifics (CLI flags, stream-json shape, `.claude/agents/` convention, `--resume`). After this lands, a second runtime can be added by implementing one interface in a sibling package.

This is a refactor, not a feature. No user-visible behavior changes from #500 alone. The CLI flag (`--runtime claude-code`) ships in #504.

## Non-goals

- No Codex support. The contract is designed from current Claude Code usage. Gaps surfaced by milestone 18 are acceptable and will be addressed there.
- No coverage of the "thinker" path (`internal/claudecli`). That package is used by depgraph and review-risk classification and is a separate, narrower boundary.
- No changes to agent contracts (`.claude/agents/*.md` markdown format). Contracts remain Minder-defined; the runtime translates them at materialization time.
- No anticipation of resume semantics for runtimes that lack sessions. The interface exposes `ErrNotSupported` and the caller falls back.

## Interface

See `internal/runtime/runtime.go` and `internal/runtime/types.go`. Summary:

```go
type AgentRuntime interface {
    Name() string
    Capabilities(ctx) (RuntimeCapabilities, error)
    PrepareAgentDef(ctx, ws, def) error
    Run(ctx, inv, sink, logFile) (exitCode int, err error)
    Resume(ctx, sessionID, sink, logFile) (exitCode int, err error)  // ErrNotSupported allowed
    ParseResult(logPath) (*Result, error)
    ClassifyOutcome(r, limits) Outcome
    ExtractBailReport(r, logPath) *BailReport
}
```

Supporting types: `Workspace`, `AgentDefinition`, `Invocation`, `Limits`, `Result`, `Outcome`, `BailReport`, `EventSink`.

## Seam-by-seam mapping

Each row maps an interface method to the current Claude-Code-shaped code it abstracts. The ClaudeRuntime implementation in #501 will move or wrap these.

| Interface method | Current code | Notes |
|---|---|---|
| `Capabilities` | `internal/runtime/capability.go` plus each concrete adapter's CLI/version probe | Reports CLI availability, version, accepted models/aliases/formats, and feature flags for launch preflight and `minder doctor`. Results are cached per process. |
| `PrepareAgentDef` | `ensureAgentDefByName` in `internal/supervisor/prompt.go:184` (called from `SlotContext.EnsureAgentDef` at `internal/supervisor/jobmanager.go:138`) | Writes `.claude/agents/<name>.md` into the worktree. Returns `AgentDefSource` today; the runtime swallows that. |
| `Run` | `SlotContext.RunClaudeAgent` at `internal/supervisor/jobmanager.go:157` + `buildAgentArgs` at `internal/supervisor/prompt.go:324` + `scanStream` at `internal/supervisor/scanner.go:46` | The supervisor builds `[]string` Claude CLI args today; ClaudeRuntime owns that internally. Stream-json scanning becomes runtime-internal, with normalized events pushed to `EventSink`. |
| `Resume` | Hard-coded `--resume <session_id>` at `internal/supervisor/jobmanager.go:527-531` | Currently always supported because we only have Claude. Becomes ClaudeRuntime-internal. |
| `ParseResult` | `agentutil.ParseAgentLog` in `internal/agentutil/result.go:30` | The `AgentResult` struct is Claude's result-event shape. ClaudeRuntime adapts it to the normalized `Result`. |
| `ClassifyOutcome` | `classifyOutcome` in `internal/supervisor/outcome.go:24` | Pure function over `(result, maxTurns, maxBudget)`. Largely lifts as-is, with the input switched to normalized `Result` + `Limits`. |
| `ExtractBailReport` | `extractBailReport` and `extractBailReportFromLog` in `internal/supervisor/bail.go` | Knowledge of where the final text lives (Claude's `result.Result`) and the raw-log fallback are runtime-specific. The `<bail-report>` JSON parser inside is shared and stays in a common helper. |

## Design decisions and rationale

**EventSink instead of supervisor mutation.** Today `scanStream` directly writes to `supervisor.running[jobID].liveStatus`. That couples the runtime to supervisor internals. `EventSink` is a four-method interface the supervisor implements against its running-map. Runtime knows nothing about jobs, slots, or supervisors.

**Resume is optional, not required.** Claude Code supports session resume; we don't know what Codex offers. The interface exposes `Resume`, and runtimes that lack it return `ErrNotSupported`. The supervisor's existing "re-run stage from scratch" fallback (already present in `jobmanager.go:532-536`) catches that case without losing function.

**Bail-report extraction is runtime-shaped, not runtime-internal.** The `<bail-report>` JSON protocol is Minder-defined and the agent emits it on instruction from `prompt.go`. But *where* the final message lives, and what "scan the raw log" means, differ per runtime. `ExtractBailReport` is on the interface; the JSON parser (`bail.go`'s `unescapeJSONString` etc.) stays in a shared helper that runtimes call internally.

**Tooling and system prompt as normalized strings.** `AllowedTools []string` and `SystemPrompt string` pass through as Minder's view; each runtime translates to its own gating and injection model. Claude maps `AllowedTools` to `--allowedTools`; Codex may map differently or ignore. Lossy translation is the runtime's problem.

**`Result.Native` and `BailReport.Native` for forensics.** Both types include a raw `json.RawMessage` so the runtime-native data survives normalization. Used for debug logging and for diagnosing surprises in the wild.

**`AgentDefinition.Body` is opaque bytes.** The runtime decides how to interpret the contract markdown. Today's `.claude/agents/<name>.md` form works because Claude Code reads frontmatter; Codex might extract a different subset. Keeping it opaque lets PrepareAgentDef do whatever materialization is right.

## Deferred / open

- **AgentDefinition source-of-truth.** Today contracts live in `.claude/agents/*.md` in the worktree and Minder's embedded defaults in `internal/supervisor/agents/`. The materialization step writes a worktree-local copy. This stays Claude-Code-shaped for now; Codex may want contracts in a different location (e.g., a temp file passed via env). PrepareAgentDef can do whatever it needs; no contract changes in #500.
- **Timeout handling.** `Limits.Timeout` is on the interface but unused by the current supervisor (`internal/supervisor/jobmanager.go` does not currently set a deadline on the runtime call). ClaudeRuntime will ignore it. Wiring deferred until a real need surfaces.
- **Cost reporting cadence.** `EventSink` currently fires only on assistant steps, tool boundaries, and usage limits. Cost is post-hoc via `ParseResult`. If we want live cost ticks for the TUI, add `OnCostUpdate(usd float64)` later. Out of scope for #500.

## What's deliberately NOT in the contract

- No method to list events from history. ParseResult is the only post-hoc accessor; if we need richer replay, add it when there's a caller.
- No structured tool-use schema. `OnToolStart(name, inputSummary)` passes a displayable summary string; the supervisor doesn't need the parsed input. Structured tool data stays in the raw log via the `Native` field on `Result`.
- No retry policy on the interface. Retry/backoff lives in the supervisor where it can coordinate across stages, not in the runtime.
