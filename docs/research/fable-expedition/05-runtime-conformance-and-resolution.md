# 05 — Runtime Conformance and Configuration Resolution

**Expedition V — The Three Masks.** Research only. No production code changed.

| Field | Value |
|---|---|
| Reviewed SHA | `55a3308b57387ac1c098828b45f2d31de6c3eb93` (`origin/main`) |
| Reviewed date | 2026-08-16 |
| Scope | `internal/runtime` (3 adapters), `internal/supervisor`, `internal/db`, `internal/controlapi`, `internal/daemon`, `cmd/` |
| Live evidence | `claude` CLI 2.1.233 stream-json captured and parsed on this host. `codex` and `opencode` binaries are **not** installed here; their findings are code-read plus tests, not live capture. |
| Closes | #592 |
| Gates | #635, #636, #646 |

---

## TL;DR — read this first

1. **Job cost is Claude-Code-only.** `parseCostFromLog` scrapes the string `"total_cost_usd"` from the raw log (`internal/supervisor/supervisor.go:901`). Codex and opencode logs never contain that key. So `jobs.cost_usd` stays 0, and `--total-budget` never stops a codex or opencode fleet. **This is the biggest leak.** (high)
2. **#528 is confirmed, but its stated cause is stale.** `buildArgs` *does* pass `--model` now (`internal/runtime/claudecode/claudecode.go:172`). The live cause is two other lines: `AgentContract` has no `model` field (`contract.go:15-37`), and the model is dropped unless the stage agent equals the job agent (`supervisor/runtime.go:107`). (high)
3. **`claude --resume` runs in the wrong directory.** `Resume` passes an empty working directory (`claudecode.go:107`). Claude Code stores sessions per project directory — verified on disk at `~/.claude/projects/<slug-of-cwd>/`. Usage-limit resume therefore cannot find the worktree session. (high)
4. **Resume loses the credential.** Codex and Claude Code both pass `nil` env on `Resume` (`claudecode.go:107`, `codex/codex.go:96`), so `GITHUB_TOKEN` is gone in the resumed process. (high)
5. **`num_turns` means three different things** — a CLI counter, a `turn.completed` tally, and a count of `step-start` parts in one message. `max_turns` compares incomparable numbers. (high)
6. **`stop_reason` is double-encoded for Claude Code.** Verified by running the real parser on real output: `Result.StopReason == "\"end_turn\""`, with literal quotes. Codex and opencode emit plain text. The value reaches the v1 wire. (high)
7. **Contract `timeout:` and stage `timeout:` are parsed and never enforced.** `runtime.Limits.Timeout` is never set anywhere. (high)
8. **v1 exposes `current_tool` and `tool_input` that are always `null`** (`controlapi/service.go:293`, golden file line 69). #646 must fill or remove them.

---

## 1. What the abstraction is

**Verdict: the interface is sound. The leaks are all *outside* it — in the supervisor and in the wire contract.**

`AgentRuntime` (`internal/runtime/runtime.go:44-86`) has seven methods: `Name`, `PrepareAgentDef`, `Run`, `Resume`, `ParseResult`, `ClassifyOutcome`, `ExtractBailReport`. All three adapters implement all seven. `Resume` may return `ErrNotSupported`; no adapter does today.

One contract, three materializations:

| Runtime | Agent def written to | Code |
|---|---|---|
| claude-code | `.claude/agents/<name>.md` | `claudecode.go:72-79` |
| codex | `AGENTS.md` (overwrites) | `codex/codex.go:74-77` |
| opencode | `.opencode/agent/<name>.md` | `opencode/opencode.go:100-107` |

⚠ **Trap (medium):** Codex writes the whole contract to `AGENTS.md`. A repo that already has an `AGENTS.md` loses it in the worktree. Worktrees are ephemeral, so the loss is not permanent, but the agent never sees the repo's own `AGENTS.md`.

---

## 2. Conformance matrix — configuration in

**Verdict: only Claude Code receives every field of `Invocation`. Codex silently drops tools. All three ignore `Limits.Timeout`.**

| Capability | claude-code | codex | opencode |
|---|---|---|---|
| Model flag | `--model <name>` (`claudecode.go:173`) | `--model <name>` (`codex.go:188`) | `Model{ProviderID, ModelID}`, **needs `provider/model`** (`opencode.go:249`) |
| Model with no slash | works (alias) | works | **silently dropped**, falls back to opencode default (`opencode.go:270-280`) |
| Provider choice | implicit (Anthropic) | implicit (OpenAI) | explicit, any models.dev provider |
| System prompt | `--append-system-prompt` (`claudecode.go:185`) | `-c developer_instructions=<quoted>` (`codex.go:198`) | `params.System` (`opencode.go:247`) |
| User prompt | positional after `--` | positional | one text part |
| Allowed tools | `--allowedTools <csv>` (`claudecode.go:182`) | **dropped — no equivalent** (`codex.go:82-83`) | `params.Tools` map (`opencode.go:255-261`) |
| Permission model | tool allowlist | `--ask-for-approval never` + `--sandbox workspace-write` (`codex.go:180-192`) | `OPENCODE_PERMISSION` env, set **once at server start** (`permission.go:36-44`) |
| Network in sandbox | host default | forced on: `sandbox_workspace_write.network_access=true` (`codex.go:192`) | host default |
| Working directory | `cmd.Dir` | `--cd <dir>` **and** `cmd.Dir` | `directory` param per session (`opencode.go:129`) |
| Git metadata access | inherited | `--add-dir` for the real gitdir + commondir (`codex.go:194-196`) | inherited |
| Env / credentials | `os.Environ()` + `inv.Env` | `os.Environ()` + `inv.Env` | **server env only, first start wins** (`server.go:42-46`) |
| `Limits.MaxTurns` | `--max-turns N`, CLI enforces | **no flag**; adapter cancels the process at the limit (`scanner.go:79-81`) | **not sent**; post-run check only |
| `Limits.MaxBudgetUSD` | `--max-budget-usd F`, CLI enforces | **not sent**; post-run check only | **not sent**; post-run check only |
| `Limits.Timeout` | **never set by any caller** | same | same |

**Bold decision:** `Invocation.AllowedTools` and `Limits.Timeout` are **not part of the real contract today**. Either enforce them per runtime or delete them. Do not report them to operators as active.

---

## 3. Conformance matrix — observability out

**Verdict: `Result` has one shape and three incompatible meanings. Every consumer of `NumTurns`, `TotalCostUSD`, and `StopReason` is comparing unlike things.**

| Field | claude-code | codex | opencode |
|---|---|---|---|
| `SessionID` | `result.session_id` (UUID) | `thread_id` from `thread.started` | opencode session id |
| Session scope | **per project directory** (verified: `~/.claude/projects/<slug>/`) | thread id, directory-independent | server-side, directory carried per prompt |
| `NumTurns` | CLI `num_turns` (observed: 1 for one assistant message) | count of `turn.completed` events (`scanner.go:110`) | count of `step-start` parts **in the final message only** (`result.go:126-128`) |
| `TotalCostUSD` | **exact**, from `total_cost_usd` | **estimated**, static price table (`pricing.go:23-41`) | **exact**, `info.cost` (`result.go:101`) |
| Unknown model cost | n/a | **assumes the most expensive rate** (`pricing.go:33`) | n/a |
| `FinalText` | `result.result` | last `agent_message` item text | joined `text` parts, reasoning excluded |
| `StopReason` | **`"end_turn"` with literal quotes** (`scanner.go:21` + `claudecode.go:216`) | `turn.completed` / `turn.failed: …` | error name, or empty on success |
| `IsError` | `is_error` | any `error` / `turn.failed` event | error name set, or non-zero exit |
| `PermissionDenials` | populated | always nil | always nil |
| Multiple runs in one log | **first result wins** (`scanner.go:179`) | **all events accumulate** (`codex.go:345-356`) | **last record wins** (`result.go:59-69`) |
| Live `OnAssistantStep` | per assistant message | per `turn.completed` (coarser) | per `step-start` part |
| Live `OnToolStart` | last `tool_use` block of a message | `item.started` / `response_item` | per tool part, deduped by call id (`stream.go:132-155`) |
| Live `OnUsageLimit` | `system/api_retry` with `rate_limit`/`billing_error` | keyword scan of error text (`scanner.go:292-300`) | keyword scan of event JSON (`stream.go:108-114`) |
| Usage-limit signal quality | **structured** | **string match** | **string match** |

⚠ **Trap (high):** the "multiple runs in one log" row is a live defect. The supervisor appends resume output to the same log file. After one resume, Claude Code reports the **pre-limit** result, and Codex reports **summed** turns and cost across both attempts. Only opencode is correct.

### Live evidence — Claude Code, observed 2026-08-16

Command: `claude -p --output-format stream-json --verbose --model haiku --max-turns 2`.

The `system` / `init` event carries the values #635 asks for, with no extra process call:

| Key | Observed value |
|---|---|
| `model` | `claude-haiku-4-5-20251001` (alias `haiku` resolved) |
| `claude_code_version` | `2.1.233` |
| `permissionMode` | `default` |
| `cwd`, `tools`, `agents`, `apiKeySource` | all present |

The `result` event also carries `modelUsage`, keyed by concrete model id, with `costUSD`, `canonicalModel`, and `provider`. **Neither event is read by any adapter today.**

---

## 4. The resolution chain — traced

**Verdict: runtime resolves through a clean four-tier hierarchy. Model does not. Model has no repo layer, no user layer, no deployment layer, and no agent layer.**

### 4.1 Runtime — works

| Order | Source | Code |
|---|---|---|
| 1 | `jobs.yaml` `runtime:` → `jobs.runtime` | `scheduler/config.go:37`, `scheduler.go:58` |
| 2 | `--runtime` flag | `cmd/deploy.go:127-131` |
| 3 | repo `.agent-minder/config.yaml` | `runtime/config.go:31` |
| 4 | user `~/.agent-minder/config.yaml` | `runtime/config.go:40` |
| 5 | default `claude-code` | `runtime/config.go:48` |

⚠ **Trap (medium):** two resolvers exist. `runtimeForJobLocked` prefers the supervisor's cached runtime (`supervisor.go:175`); `EffectiveRuntime` prefers `deploy.Runtime` (`db/models.go:142`). `beginAgentRun` records the **second** while `Run` uses the **first** (`jobmanager.go:581`). They agree today only because `cmd/deploy.go:362` builds the cached runtime from `deploy.Runtime`. Any future divergence writes a false `agent_runs.runtime`.

### 4.2 Model — broken

| Layer | Does it exist? | Evidence |
|---|---|---|
| `jobs.yaml` `model:` | **yes** | `scheduler/config.go:38`, `scheduler.go:169` |
| Watch route `Model` | **yes** | `supervisor/watch.go:32,276` |
| `jobs.model` column | **yes** (schema v8) | `db/models.go:154` |
| Agent frontmatter `model:` | **NO — field does not exist** | `supervisor/contract.go:15-37` |
| Stage `model:` | **NO** | `supervisor/contract.go:40-47` |
| Deployment doer model | **NO** — only `analyzer_model` | `db` schema; `cmd/deploy.go:82` |
| Repo/user `config.yaml` model | **NO** — only `runtime:` | `runtime/config.go:20-22` |
| `--model` CLI flag | **exists, but it is the *analyzer* model** | `cmd/deploy.go:82` |

### 4.3 #528 — cause confirmed

The issue text says the runtime never passes `--model`. **That part is now stale.** `claudecode.go:172-174` appends `--model`. Two live causes remain:

| # | Cause | Line | Effect |
|---|---|---|---|
| A | `AgentContract` has no `model` field; YAML ignores unknown keys | `contract.go:15-37` | `model:` in an agent `.md` is silently discarded |
| B | Model is applied only when the stage agent equals the job agent | `runtime.go:107-109` | every non-primary stage (for example `review`) runs on the CLI default |
| C | No deployment-level doer default | `db` schema | an operator cannot set a fleet model at all |

**One-line precedence fix:** delete the `agentName == job.Agent` gate and resolve the model through one helper with the order in §5.

⚠ **Trap (high):** `internal/daemon/server.go:321` reports `Model: deploy.AnalyzerModel` as the deployment's model. Operators read the analyzer model and believe it governs implementers. This single line is why #528 reads as a config bug rather than a missing feature.

---

## 5. Recommended precedence model

**Verdict: one helper, one order, applied identically to every stage. Most specific wins. Empty means "runtime default", never "claude default".**

```
resolveExecution(deploy, job, contract, stage, agentDef) -> Effective{Runtime, Model}
```

| Rank | Source | New? |
|---|---|---|
| 1 | stage `model:` / `runtime:` in the agent contract | new field |
| 2 | agent-def frontmatter `model:` (the agent the **stage** runs) | new field |
| 3 | `jobs.model` / `jobs.runtime` (job override, from `jobs.yaml` or watch route) | exists |
| 4 | `deployments.doer_model` / `deployments.runtime` | **`doer_model` is new** |
| 5 | repo `.agent-minder/config.yaml` | model key is new |
| 6 | user `~/.agent-minder/config.yaml` | model key is new |
| 7 | empty → runtime's own default | exists |

Three rules that make it truthful:

1. **Resolve once, at stage start.** Store the result on the `agent_run` row. Never re-derive it in a display path.
2. **Never reuse `analyzer_model` for doers.** Add `deployments.doer_model`. Rename the `--model` flag help text to say "analyzer model".
3. **Resolve per stage, not per job.** A review stage and an implement stage may legitimately differ.

| | |
|---|---|
| **Strongest competing option** | Keep model job-scoped only. Drop layers 1, 2, 5, 6. |
| **Tradeoff** | Much less code and no new frontmatter parsing. But it does not satisfy #528's core goal — cheap model for mechanical agents, strong model for hard ones — because the agent is what decides which is which. |
| **Revisit trigger** | If, six months after landing, no repo in use sets a frontmatter `model:`, collapse layers 1–2 into layer 3 and delete the parsing. |

---

## 6. Exact effective config to persist per `agent_run` (spec for #635)

**Verdict: today the row records the *requested* string, not the *resolved* one, and resume attempts write no row at all.**

Current state: `beginAgentRun` writes `runtime`, `model`, `max_turns`, `max_budget_usd`, `log_path` (`jobmanager.go:568-592`). `model` is `inv.Model`, which is empty whenever nothing is configured — which is the common case. `finishAgentRun` fills `session_id`, `cost_usd`, `final_turns`, `step_count`, `status`, `stop_reason`, `final_text`.

### What to add

| Field | Type | Written when | Source per runtime |
|---|---|---|---|
| `model_requested` | TEXT | run start | resolved string from §5 (may be empty) |
| `model_resolved` | TEXT | first observation | **claude-code:** `system`/`init` `.model`. **codex:** `thread.started` `.model` (already captured, `scanner.go:99`). **opencode:** `provider/model` from the assistant message |
| `model_source` | TEXT | run start | which precedence rank won: `stage`/`agent`/`job`/`deployment`/`repo`/`user`/`runtime_default` |
| `runtime_version` | TEXT | first observation | **claude-code:** `claude_code_version` from init. **codex/opencode:** `--version` at server or process start |
| `cost_basis` | TEXT | run end | `exact` \| `estimated` \| `unavailable` (see §8) |
| `turn_basis` | TEXT | run end | `cli_turns` \| `completed_turns` \| `message_steps` |
| `limits_enforced` | TEXT (JSON) | run start | which limits the runtime actually enforces, from the capability table in §9 |

### Three rules

1. **Never write `model_resolved` from configuration.** It must come from the runtime's own output, or stay null.
2. **Log a visible warning on mismatch** between `model_requested` and `model_resolved`, as #635 asks.
3. **Every resume attempt gets its own row.** Today `resumeThroughRuntime` runs outside `beginAgentRun` (`jobmanager.go:821`), so the resumed work has no cost, no session, and no step count anywhere. Give it `attempt = attempt + 1` and set `stop_reason = "resumed_from:<prior run id>"`.

⚠ **Coordinate before writing code:** PR **#663** (open, draft, branch `agent/issue-635`) already adds `agent_runs.runtime_version`, a v13→v14 migration, and per-runtime metadata providers. Reconcile this section with that PR rather than opening a competing migration. (high)

---

## 7. Unsupported and leaky abstraction points

**Verdict: eight leaks. Four are load-bearing today.**

| # | Leak | Evidence | Severity |
|---|---|---|---|
| L1 | **Job cost is scraped for one runtime.** `parseCostFromLog` searches for `"total_cost_usd"` | `supervisor/supervisor.go:901` | **load-bearing** |
| L2 | **Total-budget ceiling is therefore claude-code-only.** `TotalSpend` sums `jobs.cost_usd` | `db/queries.go:494`, `supervisor.go:744-758` | **load-bearing** |
| L3 | **Deployment API reports the analyzer model as *the* model** | `daemon/server.go:321` | **load-bearing** |
| L4 | **`current_tool` / `tool_input` are always null on the v1 wire** | `controlapi/service.go:293`; golden line 69 | **load-bearing** |
| L5 | **`stop_reason` is double-quoted for claude-code only.** Verified by running the parser | `claudecode.go:216` | medium |
| L6 | **`ClassifyOutcome` is copied three times with the same 95 % budget rule** — including for codex, where the number is an estimate | `claudecode.go:243`, `codex.go:293`, `result.go:149` | medium |
| L7 | **Contract and stage `timeout:` are parsed and never applied** | `contract.go:102-124` | medium |
| L8 | **Dangling design references.** `design/opencode-mapping.md` and `design/codex-mapping.md` are cited by code and by Expedition II but do not exist at this SHA | `opencode.go:8`, `server.go:26` | low |

**Fix order:** L1 before L2 (L2 is a consequence). L3 and L4 are one-line honesty fixes. L5 is a one-line trim. L7 is a decision, not a bug: enforce or delete.

---

## 8. Rules for UI labels

**Verdict: a number with no basis label is a lie. Every cost, turn, and model field on the wire needs a companion basis field.**

| Label | Meaning | Use when |
|---|---|---|
| `exact` | The runtime computed this value | claude-code cost, opencode cost |
| `estimated` | Minder computed it from tokens and a local price table | codex cost |
| `unsupported` | The runtime cannot produce it | codex permission denials, opencode permission denials |
| `runtime-defined` | Configured as empty; the runtime chose | model when nothing is set |
| `unavailable` | Should exist but was not observed this run | model resolution when the init event is missing |

Four display rules:

1. **Never sum an `exact` value with an `estimated` one into one unlabeled total.** If a deployment mixes runtimes, show the split.
2. **Show `estimated` costs with a warning marker.** Codex uses a static table last verified 2026-06-23 (`pricing.go:11`) and charges unknown models at the highest known rate (`pricing.go:33`) — the estimate is deliberately pessimistic.
3. **Do not print a model name the runtime did not confirm.** Print `runtime-defined` instead of guessing.
4. **Never label a turn count comparable across runtimes.** Show `turn_basis` next to it, or show only the ratio against that run's own `max_turns`.

---

## 9. Capability negotiation

**Verdict: add a static, declared capability table. Do not probe the binaries at run time.**

Proposed addition to the interface, additive and non-breaking:

```go
type Capabilities struct {
    ResumeSessions      bool
    ResumeNeedsWorkdir  bool
    ModelSelection      string // "alias" | "provider-qualified" | "none"
    ToolAllowlist       bool
    EnforcesMaxTurns    bool
    EnforcesMaxBudget   bool
    CostBasis           string // "exact" | "estimated"
    UsageLimitSignal    string // "structured" | "heuristic"
    PermissionDenials   bool
    SharedProcess       bool
}
```

Values as of this SHA:

| Capability | claude-code | codex | opencode |
|---|---|---|---|
| `ResumeSessions` | yes | yes | yes (re-prompt, not true continuation) |
| `ResumeNeedsWorkdir` | **yes** | no | no |
| `ModelSelection` | `alias` | `alias` | `provider-qualified` |
| `ToolAllowlist` | yes | **no** | yes |
| `EnforcesMaxTurns` | yes (CLI) | partly (adapter cancels) | **no** |
| `EnforcesMaxBudget` | yes (CLI) | **no** | **no** |
| `CostBasis` | `exact` | `estimated` | `exact` |
| `UsageLimitSignal` | `structured` | `heuristic` | `heuristic` |
| `PermissionDenials` | yes | **no** | **no** |
| `SharedProcess` | no | no | **yes** |

| | |
|---|---|
| **Strongest competing option** | Probe each CLI at start-up (`--version`, `--help`) and derive capabilities. |
| **Tradeoff** | Probing tracks upstream changes automatically, but adds process launches to every deploy, fails on a slow or missing binary, and produces a capability set no test can pin. A declared table is testable and reviewable. |
| **Revisit trigger** | If a runtime ships a capability change that breaks a Minder guarantee twice in one quarter, add version-gated entries to the table before adding probing. |

⚠ Two entries must be marked **medium confidence**: the codex and opencode rows come from code reading and unit tests, not from live capture. Those binaries are not installed on this host. Confirm on a host that has them before treating the table as final.

---

## 10. What is now stale

**Verdict: three prior claims no longer hold. Correct them where they are cited.**

| Claim | Status | Correction |
|---|---|---|
| #528: "the claude-code runtime never passes `--model`" | **stale** | It does, at `claudecode.go:172`. The live causes are contract parsing and the stage gate (§4.3). |
| #528 step 5: "persist the chosen model per job — the `jobs` table has no model column" | **stale** | `jobs.model` landed in schema v8; `agent_runs.model` landed in v11. |
| #646: "recent activity is not observable at all" | **partly stale** | `agent_runs.step_count` and `last_activity_at` are durable and served by v1 (#648). Only the tool ring is missing. |
| Expedition I risk 10: "opencode computes real USD; claude-code and codex differ" | **still true, now sharper** | claude-code is also exact. Codex is the only estimate. The bigger divergence is `num_turns`, not cost. |
| Expedition IV §5 taxonomy | **still binding** | Tool events belong in the ephemeral class. §11 below follows it. |
| `design/opencode-mapping.md`, `design/codex-mapping.md` | **missing at this SHA** | Cited by `opencode.go:8`, `server.go:26`, and Expedition II. Either restore them or drop the references. |

Newer merged work that this document assumes: v1 contract (#647, `93738cb`), v1 read endpoints (#648, `93e2d49`), durable events (#644, `008b10e`), durable SSE (#649, `d65b39a`), Unix socket (#650, `1764ba6`).

---

## 11. Guidance for #646 — activity fidelity

**Verdict: classify the tool ring as ephemeral, per Expedition IV §5. Do not add a schema. Fix the null fields instead.**

Per-runtime fidelity, which decides how much the ring can promise:

| Runtime | Tool start | Tool end | Balanced pairs? | Input summary source |
|---|---|---|---|---|
| claude-code | last `tool_use` in a message | on a message with no `tool_use` | **no** — only the last tool of a parallel batch is reported (`scanner.go:91-98`) | keys `command`/`file_path`/`pattern`/`prompt`/`query`/`description`, else raw JSON |
| codex | `item.started`, `response_item` | `item.completed`, `*_output` | approximately | same key list, else `command`, else `call_id` |
| opencode | tool part, deduped by call id | terminal state, start synthesized if missed | **yes** — explicitly balanced (`stream.go:132-155`) | `state.title`, else raw input JSON |

Three consequences for the schema shape:

1. **The ring must tolerate missing starts and missing ends.** Only opencode guarantees pairs. Do not model it as a stack.
2. **Parallel tool calls are unrepresentable for claude-code today.** A ring built now records one of N. Either accept that and label it, or change the adapter to emit one `OnToolStart` per `tool_use` block — a production change, out of scope here.
3. **Redaction is mandatory and already half-designed.** All three adapters truncate to 80 characters and prefer named keys. That is truncation, not redaction. A `Bash` command containing a token still reaches the sink verbatim. Define the redaction rule once, in `internal/runtime`, and test it against all three summary functions.

Fix L4 in the same change: `runDTO` (`controlapi/service.go:293`) never populates `RunActivity.CurrentTool` or `ToolInput`, so both are permanently `null` on the wire. Serve them from live state or delete them from the contract.

---

## 12. Runtime-specific traps

⚠ **claude-code — resume runs in the wrong directory (high).** `Resume` passes `workDir == ""` (`claudecode.go:107`). Sessions are stored per project directory; verified on this host at `~/.claude/projects/-home-user-agent-minder/<session>.jsonl`. Fix by threading the workspace through `Resume`.

⚠ **claude-code — `ParseResult` returns the first result event (high).** `readResultEvent` returns inside its loop (`scanner.go:179`). The log is opened in append mode, so a resumed run reports pre-resume cost and turns. Scan to the last event.

⚠ **claude-code — `StopReason` keeps its JSON quotes (medium).** Verified: `Result.StopReason == "\"end_turn\""`. Unmarshal the token instead of casting the raw bytes.

⚠ **codex — no tool allowlist (high).** `AllowedTools` is dropped. The only boundary is `--sandbox workspace-write`, and network access is forced on (`codex.go:192`). Do not tell operators the allowlist applies under codex.

⚠ **codex — cost is a guess against a fixed table (medium).** `pricing.go` was last verified 2026-06-23. An unknown model bills at the highest known rate. Budget classification uses this number (`codex.go:293`).

⚠ **codex — resume loses cwd, env, and limits (high).** `Resume` passes `""`, `nil`, and `runtime.Limits{}` (`codex.go:96`). The resumed run has no `GITHUB_TOKEN` and no turn cap.

⚠ **opencode — a model without a slash is silently ignored (high).** `splitModel` returns `ok=false` and the model is simply omitted (`opencode.go:270-280`). The operator sees no error and gets opencode's default. Validate the form at config time.

⚠ **opencode — server env is frozen at first start (high).** `ensure` applies env only when it starts the process (`server.go:42-46`). A per-job credential never reaches the server. Treat provider keys as deployment-level, as README already says.

⚠ **opencode — `Resume` is a new prompt, not a continuation (medium).** It sends a fixed nudge string (`opencode.go:217`). The session history survives, but the model receives a new instruction. Label this differently from a true resume in any UI.

⚠ **opencode — `NumTurns` counts steps in one message (high).** `digestParts` counts `step-start` parts in the final assistant message only (`result.go:126`). Against a `max_turns` of 30 this almost never trips. Turn limits are effectively absent under opencode.

---

## 13. Do this next

Ordered. Each line is one task a cheaper agent can execute.

1. **Fix L3.** Change `daemon/server.go:321` to stop reporting `analyzer_model` as the deployment model. Add a separate `analyzer_model` field or drop the field.
2. **Fix L5.** Unmarshal `stop_reason` into a string in `claudecode/scanner.go` so the value carries no quotes. Add a golden test.
3. **Fix the claude-code resume directory.** Add a workspace parameter to `AgentRuntime.Resume` and pass the worktree. This is a signature change — one PR, all three adapters.
4. **Fix resume env.** Pass `inv.Env` through `Resume` for claude-code and codex.
5. **Fix `ParseResult` last-wins for claude-code.** Scan to the final `result` event, matching opencode's behavior.
6. **Add `deployments.doer_model`.** One migration. Do not reuse `analyzer_model`.
7. **Add `model:` to `AgentContract` and `StageContract`.** Parser only; no behavior yet.
8. **Land the §5 resolver.** One helper, used by `runtimeInvocationFor`. Delete the `agentName == job.Agent` gate. This closes #528.
9. **Land #635 on top of PR #663.** Add `model_resolved`, `model_source`, `cost_basis`, `turn_basis`. Read the claude-code `init` event for the resolved model and CLI version.
10. **Give every resume attempt its own `agent_runs` row.** Prerequisite for #636.
11. **Replace `parseCostFromLog` with the runtime's `Result.TotalCostUSD`.** This closes L1 and L2 together.
12. **Add the §9 capability table.** Then make #636 read `ResumeSessions` and `UsageLimitSignal` from it.
13. **Decide contract `timeout:`.** Enforce it through `Limits.Timeout`, or delete both fields. Do not leave it parsed and dead.
14. **Then start #646.** Fix the null `current_tool`/`tool_input` first; decide the ring shape second.

### Fixed decisions — do not re-litigate

- The `AgentRuntime` interface stays. The leaks are in the supervisor, not the seam.
- Model precedence is most-specific-wins, resolved once per **stage**.
- `analyzer_model` never governs doers.
- An empty model means "runtime default", never "claude default".
- Tool activity is **ephemeral** (Expedition IV §5). No new table for it.
- Cost, turns, and model each need a basis label on the wire.

### Open decisions and owners

| Decision | Owner | Blocking |
|---|---|---|
| Enforce or delete contract `timeout:` | maintainer | item 13 |
| Emit one `OnToolStart` per parallel `tool_use` for claude-code | maintainer | #646 shape |
| Whether `deployments.doer_model` or repo `config.yaml` is the primary operator surface | maintainer | items 6, 8 |
| How to price codex once the table goes stale | maintainer | L1 follow-up |

### Verification checklist — per landed task

- [ ] `go build ./...` and `go test ./...` pass.
- [ ] A per-runtime test asserts the configured model appears in the constructed argv or prompt params.
- [ ] A test asserts a **stage** agent that differs from the job agent still receives the resolved model.
- [ ] A test asserts an agent-frontmatter `model:` reaches the invocation.
- [ ] A test asserts `agent_runs.model_resolved` is null when the runtime reported nothing.
- [ ] A test asserts `jobs.cost_usd` is non-zero for a fake codex and a fake opencode run.
- [ ] The v1 golden files are regenerated and reviewed when any contract field changes.
- [ ] `TestClaudeMDSchemaVersion` passes after any migration.
- [ ] CLAUDE.md schema section updated in the same commit as any migration.

### Suggested issue boundaries

| Proposed issue | Contents | Size |
|---|---|---|
| A | Items 1, 2 — honesty fixes on existing wire fields | small |
| B | Items 3, 4, 5 — resume correctness across all three runtimes | medium |
| C | Items 6, 7, 8 — the precedence model; **closes #528** | medium |
| D | Items 9, 10 — resolved-config persistence; **feeds #635**, coordinate with PR #663 | medium |
| E | Item 11 — runtime-native cost; unblocks multi-runtime budgets | small |
| F | Items 12, 13 — capability table and the timeout decision; **feeds #636** | medium |
| G | Item 14 — activity ring; **is #646** | medium |

Do B before D. Do C before D. Do E before any multi-runtime budget work. Do F before #636.
