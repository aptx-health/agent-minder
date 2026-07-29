# Agent guidance in the Opus 5 era — findings and recommendations

Research spike for [#549](https://github.com/aptx-health/agent-minder/issues/549). Research only —
no template rewrites are included here. Follow-up implementation is proposed at the end.

## Verdict

Our agent guidance was written for models that needed to be pushed. Opus 5 needs to be *bounded*
instead. Three things are true at once:

1. **We over-scaffold.** Five of our six agent contracts contain a step-by-step verification block,
   and four of them prescribe a second verification pass after the first. Anthropic's Opus 5 guide
   explicitly says to delete that scaffolding: it causes over-verification and burns tokens with no
   quality gain.
2. **We under-specify the things that actually still need specifying.** Output length, narration
   cadence, scope boundaries, subagent limits, and human-facing formatting are all left to the
   model's defaults — and Opus 5's defaults are longer and more expansive than earlier models'.
3. **We have four divergent copies of the same instructions**, and the file every contract tells the
   agent to read first (`CLAUDE.md`) is materially wrong about the schema version, the built-in
   agent list, and the runtime architecture.

The highest-value work is not "loosen the prose." It is: collapse the duplication, fix the factually
wrong guidance, delete the verification scaffolding, and add the four short control blocks that
Opus 5 actually responds to.

## Evidence base

Fetched 2026-07-29 from `docs.claude.com` (Markdown variants of the live docs):

| Source | Used for |
|---|---|
| [Prompting Claude Opus 5](https://docs.claude.com/en/docs/build-with-claude/prompt-engineering/prompting-claude-opus-5) | Verbosity, deliverable length, over-verification, scope creep, subagent spawning, self-correction, thinking-disabled artifacts |
| [Prompting best practices](https://docs.claude.com/en/docs/build-with-claude/prompt-engineering/claude-4-best-practices) | Positive-instruction framing, output format control, overeagerness, migration ("tune anti-laziness prompting") |
| [Skill authoring best practices](https://docs.claude.com/en/docs/agents-and-tools/agent-skills/best-practices) | "Concise is key", degrees-of-freedom model (narrow bridge vs. open field) |

The five behaviours from the Opus 5 guide that map directly onto our files:

- **Over-verification.** "If your prompt contains explicit verification instructions … remove them:
  instructions like these cause over-verification on Claude Opus 5, and removing them reduces wasted
  tokens with no loss in quality. The same applies to legacy harness scaffolding that adds separate
  verification steps."
- **Literal instruction following in review.** "If your review prompt says 'only report
  high-severity issues' or 'be conservative,' the model may follow that instruction literally and
  report less; ask it to report everything and filter in a separate pass instead."
- **Written deliverable length.** "Files that Claude Opus 5 writes to disk (reports, Markdown
  documents, summaries) are often longer than on prior models."
- **Scope expansion.** "Claude Opus 5 can also expand the scope of a task, adding steps that weren't
  requested."
- **Subagent spawning.** "Claude Opus 5 delegates to subagents more readily than prior models … Do
  not delegate work you can finish yourself in a handful of tool calls, and do not use subagents to
  verify or double-check your own work."

Plus the general-principles guidance we violate systematically: **tell the model what to do instead
of what not to do**, and **only add context the model doesn't already have**.

## Current state: where guidance lives

| Surface | Size | Consumed by |
|---|---|---|
| `CLAUDE.md` | 1,098 words | Every agent, by instruction ("read CLAUDE.md first") |
| `.claude/agents/autopilot.md` | 1,048 words | This repo's autopilot runs (system prompt) |
| `.claude/agents/bug-fixer.md` | 742 | bug-fixer runs |
| `.claude/agents/reviewer.md` | 712 | review stage |
| `.claude/agents/security-scanner.md` | 712 | scheduled scans |
| `.claude/agents/doc-updater.md` | 719 | scheduled doc runs |
| `.claude/agents/dependency-updater.md` | 538 | scheduled dep runs |
| `internal/supervisor/templates.go` | 3,006 words total (incl. 1,158-word `securityScannerBody`) | Every *other* repo we enroll |
| `internal/supervisor/prompt.go` `defaultAgentDef` / `defaultReviewerDef` | 512 / 150 | `minder agents list`, contract fallback |
| `cmd/enroll.go` `buildEnrollSystemPrompt` | 1,528 words | `minder enroll` |
| `agents/*.md` (repo root) | 4,834 words across 5 files | Nothing — documentation/distribution copies |
| `internal/supervisor/context.go` | generated per run | User prompt for every job |

That is roughly **16,000 words of agent-facing instruction** across four independently-maintained
locations, plus a runtime-generated layer.

## Findings

### P0-1 — `CLAUDE.md` is wrong in ways that will actively mislead agents

Every contract says "read `CLAUDE.md` for architecture, package map, DB schema, and test commands."
Verified drift as of this branch:

| `CLAUDE.md` claims | Reality |
|---|---|
| "DB schema (internal/db) — currently v5", migrations listed to v5 | `schemaVersion = 8` (`internal/db/schema.go:12`) |
| Built-in agents: `autopilot`, `reviewer`, `designer`, `onboarding`, `dependency-updater`, `security-scanner`, `doc-updater` | Registry is `autopilot`, `reviewer`, `bug-fixer`, `dependency-updater`, `security-scanner`, `doc-updater`, `spike`. `designer` appears nowhere in Go source; `onboarding` is not a template. `bug-fixer` and `spike` are undocumented. |
| "LLM: Claude Code CLI … All LLM calls go through `internal/claudecli`" | `internal/runtime/` now has three doer runtimes (`claudecode`, `codex`, `opencode`); `claudecli` is only used for analysis calls (depgraph, review summarisation, lessons) |
| Package map | Omits `internal/runtime`, `internal/auth`, `internal/picker`, `internal/reaper` |

This is a correctness bug in the single highest-leverage guidance file, and it explains a class of
agent confusion that no amount of prompt tuning will fix. It also interacts badly with Opus 5's
stronger literal instruction following: a model told the schema is v5 will write a `v5→v6` migration.

**Recommendation:** treat `CLAUDE.md` as generated-adjacent. Add a test (or `doc-updater` trigger)
that asserts the documented schema version matches `schemaVersion`, and that the documented built-in
agent list matches `AgentTemplates()`. Cheap, mechanical, prevents recurrence.

### P0-2 — Four divergent copies of the autopilot/reviewer contract

| Copy | State |
|---|---|
| `.claude/agents/autopilot.md` | Current, project-specific, 1,048 words |
| `internal/supervisor/prompt.go` `defaultAgentDef` | 512 words, used by `contract.go:235,302` and tests |
| `internal/supervisor/templates.go` autopilot `DefaultBody` | ~200 words, what enrolled repos actually get |
| `agents/autopilot.md` | **Stale.** Frontmatter has no `mode`/`output`/`stages`/`context`. Still carries the "more than 8 files → bail" rule. |

The stale copy directly contradicts the live one:

- `agents/autopilot.md`: "If ANY of the following are true, do NOT proceed with implementation: The
  change requires modifying more than 8 files"
- `.claude/agents/autopilot.md`: "File count alone is NOT a reason to bail — a 20-file rename is
  simpler than a 3-file architecture change."

`agents/README.md` documents the 8-file threshold as a current, customisable knob. And the two
sync instructions we ship disagree with each other and with the code:

- `.claude/agents/autopilot.md:57`: "`defaultAgentDef` … must stay in sync with
  `.claude/agents/autopilot.md`"
- `CONTRIBUTING.md:117`: "The repo-level `agents/autopilot.md` file and the `defaultAgentDef`
  constant in `internal/autopilot/prompt.go` must stay in sync. There is a drift-prevention test
  that enforces this." — `internal/autopilot/` does not exist (it is `internal/supervisor/`), and no
  such drift test exists.

**Recommendation:** pick one source of truth per agent. The natural choice is
`internal/supervisor/templates.go` (it is what ships to users) with `.claude/agents/*.md` as this
repo's local override. Delete `agents/*.md` or reduce `agents/README.md` to a pointer at
`minder agents show <name>`. Either add the drift test `CONTRIBUTING.md` claims exists, or delete
the claim.

### P0-3 — `.claude/agents/bug-fixer.md` contradicts the shipped bug-fixer template

Repo contract (`.claude/agents/bug-fixer.md`):

> "Bugs you cannot reproduce reliably should not be 'fixed' — you risk introducing unrelated
> changes." … "Write the reproducing test first — never skip this step."

Shipped template (`internal/supervisor/templates.go`):

> "Always attempt the fix if you understand the root cause, even if you can't reproduce it. You're
> running headless … Code analysis is sufficient when the bug is clear from reading the code."
> "Write a regression test when possible, but don't bail just because you can't."

These are opposite policies. Under Opus 5's stronger literal following, the repo contract will
produce bails on exactly the headless-only bugs the template was rewritten to handle. The template
version reflects the newer, better-reasoned decision; the repo contract is the regression.

### P1-1 — Verification scaffolding is our largest single source of waste

Anthropic's guidance is unusually blunt here: remove it. Our contracts are dense with it.

`.claude/agents/autopilot.md`, "Implementing the change" — 13 numbered steps, of which 2, 3, 4, 5, 6
and 11 are verification, including an explicit re-verification after rebase:

```
2. Run `go build ./...` — fix any compilation errors before proceeding
3. Run `go test ./...` — fix any test failures
4. Run `golangci-lint run ./...` — fix lint issues …
5. Run `gofmt -l .` — fix any formatting issues with `gofmt -w <file>`
6. You may retry failing checks up to 3 times; if still failing after 3 attempts, bail
…
11. After a successful rebase, re-run `go test ./...` and lint to verify nothing broke
```

`.claude/agents/bug-fixer.md` goes further with a five-command "Step 5 — Verify end-to-end" block
that re-runs the narrow test, then the package suite, then the full suite, then all three
pre-commit gates. Note that lefthook already runs build, fmt, and lint on every commit — the model
is being told to do by hand what the hook does deterministically, and CLAUDE.md says so.

**Before** (autopilot, steps 1–13 condensed to the verification parts, ~120 words):

```markdown
2. Run `go build ./...` — fix any compilation errors before proceeding
3. Run `go test ./...` — fix any test failures
4. Run `golangci-lint run ./...` — fix lint issues; if a check fires pervasively, update
   `.golangci.yml` rather than adding per-file suppressions
5. Run `gofmt -l .` — fix any formatting issues with `gofmt -w <file>`
6. You may retry failing checks up to 3 times; if still failing after 3 attempts, bail
...
11. After a successful rebase, re-run `go test ./...` and lint to verify nothing broke
```

**After** (~45 words — keeps the two facts the model cannot infer, drops the choreography):

```markdown
The gates are `go build ./...`, `go test ./... -timeout 5m`, `golangci-lint run ./...`, and
`gofmt -l .`; lefthook runs the last three on every commit. Two project-specific rules: when a
lint check fires pervasively, fix `.golangci.yml` rather than adding per-file `//nolint`; give up
after three failed attempts at the same gate and bail instead.
```

The "after" keeps the *information* (which commands, the `.golangci.yml` policy, the retry budget)
and drops the *procedure*. The retry budget is worth keeping — it is a deterministic cap, not a
verification instruction.

### P1-2 — Our review and scan prompts tell Opus 5 to under-report

The Opus 5 guide calls this out by name. We have three instances:

`.claude/agents/reviewer.md`:
> "Do NOT make fixes that require design decisions, change the public API surface, or touch more
> than 3 files." / "Keep fixes minimal. Reviewer scope creep is itself a code quality problem."

`.claude/agents/security-scanner.md`:
> "False positives waste review time — only report if you can trace the taint path"
> "Minimum threshold: gosec `HIGH` confidence or `MEDIUM` severity + `HIGH` confidence"

Meanwhile the newer `securityScannerBody` in `templates.go` gets it right: "**When in doubt, file
it.** A human will triage." — another repo-contract-vs-template contradiction, and this time the
repo copy is the one that suppresses findings.

We already *have* the separate filter pass Anthropic recommends: the risk tier
(`low-risk`/`needs-testing`/`suspect`) and the supervisor-side auto-merge gate. So the fix is
structural, not just wording.

**Before:**
```markdown
- False positives waste review time — only report if you can trace the taint path
- Minimum threshold: gosec `HIGH` confidence or `MEDIUM` severity + `HIGH` confidence
```

**After:**
```markdown
Report everything you find, then triage it yourself in the output: mark each finding
`confirmed` (you traced the path), `probable`, or `speculative`. The supervisor and the human
reviewer filter on that field — suppressing findings at source removes information they need.
```

Same move for the reviewer: keep the *fix* budget narrow (3 files is a fine deterministic cap on
edits) but stop coupling it to how much the reviewer is willing to *say*.

### P1-3 — Nothing anywhere constrains human-facing output length or shape

Opus 5 writes longer documents and narrates more. Our human-facing artefacts are PR bodies, issue
comments, bail reports, the review comment (`internal/supervisor/review.go:43-60`), and
security-scanner issues. The only length guidance we have anywhere is `doc-updater`'s "keep it
concise", and that instruction is attached to a factually wrong reason:

> "For `CLAUDE.md`: keep it concise — it's injected into every agent prompt. Every unnecessary
> sentence costs tokens."

`CLAUDE.md` is *not* injected into the prompt (see `AssembleContext` in
`internal/supervisor/context.go` — the providers are issue, repo_info, file_list, recent_commits,
sibling_jobs, dep_graph, plus lessons via `--append-system-prompt`). Agents are told to read it.
The advice is right; the stated reason is wrong, which is exactly the kind of thing the docs say
degrades generalisation ("Claude is smart enough to generalize from the explanation" — so give it a
true one).

Proposed house style, below. This is the item most directly responsive to the issue's ask.

### P1-4 — No subagent budget, and one place that encourages unbounded fan-out

Opus 5 delegates readily. `.claude/agents/security-scanner.md` and the `security-scanner` template
both invite `Task` fan-out ("You may dispatch **parallel sub-agents (Task tool)**"), and
`buildEnrollSystemPrompt` (`cmd/enroll.go:282-293`) instructs one `claude -p --model sonnet`
research subprocess *per installed agent*, run in parallel with `&`/`wait`. With seven templates
that is up to seven concurrent Sonnet sessions on a single `minder enroll`.

The enroll fan-out is defensible — the tracks are genuinely independent, which is precisely the case
Anthropic says delegation pays off. But there is no cap anywhere, and no agent is told "do not use
subagents to verify your own work," which is the failure mode the guide flags.

**Recommendation:** one shared block in every contract (see house style), plus a hard cap on enroll
research subprocesses.

### P2-1 — Negative framing dominates our constraint sections

Every contract ends in a "Constraints" list that is almost entirely prohibitions: autopilot has 5,
bug-fixer 7, dependency-updater 6, doc-updater 5, reviewer 4, security-scanner 5. Anthropic's
guidance is to state the desired behaviour instead. Several of ours are also redundant with
deterministic enforcement (the worktree boundary is enforced by the harness; `gh pr merge` should be
an allowlist decision, not a prompt request).

**Before** (`.claude/agents/autopilot.md`):
```markdown
## Constraints
- Only modify files within your worktree directory
- Do not keep retrying if stuck — bail early with good context is better than thrashing
- Do not over-engineer. Implement exactly what the issue asks for.
- Do not run `gh pr merge` — only create draft PRs; let a human merge
```

**After:**
```markdown
## Scope

Deliver what the issue asks, at the scope intended. Make routine judgment calls yourself and
check in only when different readings of the request would lead to materially different work. If
the issue seems mistaken or a better approach exists, say so in a sentence in the PR body and
implement what was asked. Work inside your worktree, and finish at a draft PR — a human merges.
```

That is the Opus 5 guide's own scope-control wording, adapted. It replaces four prohibitions with
one positive instruction and is shorter.

### P2-2 — Contracts restate `CLAUDE.md`, then tell the agent to read `CLAUDE.md`

`.claude/agents/autopilot.md` steps 4–5 say "Read `CLAUDE.md` … Explore the relevant code paths
before touching anything", and then the next two sections ("Project conventions", "Architecture
orientation", ~330 words) restate the module path, Go version, all four gate commands, the migration
rule, the SQLite single-writer rule, the package map, and the contract-format note — all of which
are already in `CLAUDE.md`. `bug-fixer.md` ("Module context"), `doc-updater.md` ("Key facts to keep
accurate", 8 bullets), `dependency-updater.md` ("Key dependencies", 6 bullets) and
`security-scanner.md` (4 dependency bullets) do the same at smaller scale.

This is pure duplication with a maintenance cost — and where the copies drift, the agent gets two
answers. Skill-authoring guidance applies: "Only add context Claude doesn't already have."

**Recommendation:** contracts carry *behaviour* (how to decide, when to stop, what to produce).
`CLAUDE.md` carries *facts* (architecture, commands, invariants). Where a contract needs a fact,
point at it rather than copying it.

### P2-3 — Contracts are Claude-Code-shaped but now run on three runtimes

`internal/runtime/codex/codex.go:74` materialises the selected contract as `AGENTS.md`;
`opencode` does something similar. Contract bodies reference Claude-Code-specific machinery: the
`Task` tool, `WebSearch`/`WebFetch`, "using the Write tool", `--append-system-prompt` lesson
injection. `buildEnrollSystemPrompt` already half-acknowledges this ("Claude Code skills are not
automatically portable to Codex"), but the contracts themselves do not.

**Recommendation:** keep tool-specific instructions in a clearly-marked optional section, or express
them capability-first ("if you have a web-fetch tool…"). Low urgency, but it will get worse.

### P2-4 — One Opus-5-specific compatibility note on the bail protocol

Our bail protocol depends on the model emitting a raw `<bail-report>` XML-ish tag in its final
visible text, and `.claude/agents/autopilot.md` emphasises "must NOT be inside a code fence."

Two things from the Opus 5 guide are worth knowing here. First, with thinking disabled the model can
leak internal XML tags into visible output — so if we ever add a "do not emit internal or system XML
tags" instruction (the guide's recommended mitigation), it would break our bail parsing. Second, the
guide's advice against naming specific tags cuts both ways: our instruction names ours, which is
correct for us but means the general "no internal tags" mitigation is unavailable.

No change needed today. Worth a comment near `ExtractBailReport` so nobody adds the conflicting
instruction later.

## Proposed standard for human-facing deliverables

One shared block, injected once per contract (or better: once, by
`internal/supervisor/context.go`, so it cannot drift). It answers the issue's "what/why up front,
bullets over dense prose" ask while staying compatible with what the docs say actually works
(explicitly specify the format you want; positive examples beat prohibitions).

```markdown
## Writing for humans

PR bodies, issue comments, and review comments are read by people deciding whether to trust your
work. Lead with the outcome.

- **First line answers "what happened."** One sentence, no preamble.
- **Then "why," in one short paragraph.** The reason the change was needed, not a narration of
  your process.
- **Then the details, as bullets.** One idea per bullet. Reach for a table when you have three or
  more items with the same shape (files changed, versions bumped, findings).
- **Match length to substance.** Cover what matters and stop. No redundant summary section, no
  restating the issue back, no "Next steps" unless there are real ones.
- **Say what you did not do.** Skipped tests, unverified paths, and known gaps go in the body, not
  in a comment thread later.

A good PR body for a two-file bug fix is under 150 words. A good one for a schema migration might
be 400. Neither has a section that exists only because a template said it should.
```

Note the deliberate divergence from one piece of Anthropic guidance: the prompting doc's
anti-markdown snippet argues for flowing prose over bullets. That advice targets conversational and
long-form explanatory output. For PR bodies and review comments — scanned, not read, often on a
phone — bullets and tables are the right structure. The transferable principle is not "prose good,
bullets bad"; it is *specify the format explicitly rather than accepting the default*, which is what
the block above does.

## Proposed target structure

A four-layer model, each layer owning exactly one thing:

| Layer | Owns | Lives in |
|---|---|---|
| **Facts** | Architecture, package map, schema version, commands, invariants | `CLAUDE.md` (drift-tested against code) |
| **Task context** | Issue, repo info, branch, base, test command, siblings, dep graph, ready-to-run commands | `internal/supervisor/context.go` (already correct) |
| **Behaviour** | How this agent decides, when it stops, what it produces | `.claude/agents/<name>.md` body |
| **House style** | Deliverable format, scope control, narration cadence, subagent budget | One shared block, injected once |

Applying the degrees-of-freedom model from the skills guide: bail criteria, DB migrations, and the
bail-report format are **narrow bridges** — keep them exact. Investigation strategy, review depth,
and how to structure a fix are **open fields** — give direction and stop. Most of what we should cut
is exact instruction applied to open fields.

Rough size target: autopilot's contract should land near 450–550 words (from 1,048), with the cuts
coming from duplicated facts (~330) and verification choreography (~200), partly offset by ~120
words of new scope/length/subagent guidance.

## Follow-up issues to file

1. **Fix `CLAUDE.md` accuracy and add a drift test** (P0, small). Schema v5→v8, correct the built-in
   agent list, document `internal/runtime` and the three doer runtimes, add the missing packages.
   Test asserts documented schema version == `schemaVersion` and documented agent list ==
   `AgentTemplates()`.
2. **Collapse the duplicate agent definitions to one source of truth** (P0, medium). Delete or
   demote `agents/*.md`; reconcile `defaultAgentDef` with the template registry; fix the
   `CONTRIBUTING.md:117` stale path and either add the drift test it claims exists or drop the
   claim. Resolve the bug-fixer reproduce-first contradiction in favour of the template's policy.
3. **Strip verification scaffolding from all six contracts and both built-in defaults** (P1,
   medium). Per the Opus 5 guide. Keep retry caps and gate *facts*; delete step-by-step
   verification and re-verification.
4. **Add the four Opus 5 control blocks** (P1, small). Scope, deliverable length + house style,
   narration cadence, subagent budget — as one shared block injected from
   `internal/supervisor/context.go` so it cannot drift across contracts.
5. **Stop suppressing review and scan findings** (P1, small). Replace severity floors with
   report-everything-plus-self-triage; keep the reviewer's edit budget as a deterministic cap.
6. **Cap enroll research fan-out** (P2, small). Bound the parallel `claude -p` research
   subprocesses in `buildEnrollSystemPrompt`.
7. **Make contracts runtime-neutral** (P2, medium). Isolate Claude-Code-specific tool instructions
   now that contracts are materialised as `AGENTS.md` for Codex and opencode.

Suggested order: 1 and 2 first — they are correctness bugs and they shrink the surface that 3 and 4
have to touch.

## Open questions

- **Effort parameter.** The Opus 5 guide treats `effort` as the primary cost/latency lever and says
  to re-run an effort sweep rather than carrying over prior defaults. We control cost with
  `--max-turns` and `--max-budget-usd` only. Whether the runtimes expose an effort knob, and whether
  per-agent effort belongs in the contract frontmatter, needs its own spike.
- **Measuring the change.** These recommendations predict fewer tokens per job at equal or better
  outcomes. We store `cost_usd` per job, so a before/after comparison on cost and
  bail/`suspect` rates over a fixed issue set is feasible — worth doing rather than assuming.
- **Per-model contracts.** The skills guide notes Haiku needs more guidance than Opus. If we ever
  run cheaper doer models, a single trimmed contract may under-serve them.
