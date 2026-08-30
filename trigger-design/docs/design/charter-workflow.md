---
title: "Charter workflow (design)"
status: draft
date: 2026-08-30
tags: [design, charter, workflow, front-of-loop, tdd, conformance]
related: "[[0016-trigger-owns-proactive-loop]], [[0017-engine-is-workflow-general]], [[0008-workflows-deterministic-steps]], [[0013-ask-and-resume-instead-of-bail]], [[0014-answer-authority]], [[0018-ratified-contract-protection]], [[0019-human-attention-budget-conformance-layers]], [[0020-expected-red-and-topology-agnostic-review]], [[charter-gate-presentation]], [[front-of-loop-dogfood-crosswalk]], [[db-schema]], [[run-lifecycle-and-slots]]"
---

# Charter workflow (design)

The design doc [[0016-trigger-owns-proactive-loop]] deferred "until the codex exercise
learnings land." They landed: the pr-triage front-of-loop dogfood (codex-runtime, issue
#129) was a full manual run-through of this process, and its report is the input spec.
[[front-of-loop-dogfood-crosswalk]] maps that evidence onto every decision below.

**Draft.** This is content on the engine, not new engine machinery
([[0017-engine-is-workflow-general]]): a workflow definition + agent defs + config, using
steps ([[0008-workflows-deterministic-steps]]), parking/ask-and-resume
([[0013-ask-and-resume-instead-of-bail]]), authority ([[0014-answer-authority]]), and the
three new charter ADRs (0018–0020, all `deferred` pending ratification). Sequencing:
**fast-follow after the v1 core**, and Trigger's flagship first workflow.

## What the charter workflow is

The proactive half of the loop: turn a piece of work into an agreed, testable specification
*before* code exists, prove the tests fail for the right reason, drive them green, verify
independently, and open a PR carrying the charter as the reviewer's oracle. It is the direct
defense against AI drift — confident code that solves the wrong problem, which unit tests
cannot catch because they assert the code does what the code does.

The charter is the single thread: the human-verifiable definition of done, the implementer's
target, and (handed to pr-triage) the reviewer's grading oracle.

## The pipeline as ordered steps

A linear step sequence on ADR 0008 — a dumb, deterministic conductor over durable steps; the
judgment lives *inside* the agent steps. Two steps are ratification gates that park on
`awaiting_input` (ADR 0013) and resume on a human/authorized answer (ADR 0014).

| # | Step | kind | Role | Parks? | Notes |
| --- | --- | --- | --- | --- | --- |
| 1 | **Ground** | agent | charter-agent | — | Read impacted ADRs, domain facts, nearest implementation, and **detect the repo's test standard**. Probe unresolved external/tool contracts with minimal examples; retain sanitized fixtures. Output: a grounded charter draft + a list of resolved unknowns. |
| 2 | **Charter gate** | — | human | **awaiting_input** | Human ratifies the observable behaviors, non-goals, and escalation boundaries. Records `charter_version` + signer. (ADR 0019: ~≤12 scenarios; ≤5 decisions.) |
| 3 | **Author red tests** | agent | tdd-agent | — | Map each scenario to concrete bindings + goldens in the repo's test standard. One behavior per test, sentence-style names, concrete expected values, a two-line reader breadcrumb per test. |
| 4 | **Verify red** | agent | verifier | — | Independently confirm every new test fails **for the intended reason** (builder≠verifier). Register the immutable checkpoint + protected manifest (ADR 0018). |
| 5 | **Red gate** | — | human | **awaiting_input** | Human ratifies scenario coverage + goldens (scenario-first, ADR 0019). On approval the run enters the `expected_red` phase (ADR 0020). |
| 6 | **Implement to green** | agent | implementer | — | Fix *code* until the red tests pass. No authority to weaken a protected binding/golden; a genuine fixture change escalates (ADR 0018). Traced additive tests allowed. |
| 7 | **Verify green** | agent | verifier | — | Independently confirm green + full checks, and run the **contract-integrity check** against the manifest/checkpoint (ADR 0018). Drift → escalate. |
| 8 | **Open PR** | script/agent | — | — | Open the PR carrying the charter as the oracle; record its actual head/base refs. Review intent is explicit data, not the base branch (ADR 0020). Reactive review is pr-triage's job ([[0016-trigger-owns-proactive-loop]]). |

Steps 1, 3, 4, 6, 7 are `agent` steps with their own runtime/model; step 8 is mostly
`script`. Prefer script wherever the work is mechanical (ADR 0008). The conductor never
routes on an LLM decision — sequencing is config.

### Grounding (step 1) is where the ROI is

The dogfood's primary hypothesis held: grounding moved tool-contract unknowns from the
OpenCode baseline's **5/9 to 1/6**. It converted CLI/auth/sandbox/JSONL/pricing unknowns
into ratified decisions *before* implementation, and caught that the draft charter named a
stale credential and a shared price table that did not exist. Grounding is not optional
prep — it is the step that makes the charter gate cheap and the implementation first-pass.

### The two gates park on `awaiting_input`

Both gates are exactly ADR 0013's parking: the run suspends with its resume cursor, a human
(or charter-bounded orchestrator, ADR 0014) answers, and the run resumes in place — no
restart, no lost work. `human_only` / `interactive` / `orchestrator` modes apply; a
beyond-charter answer always escalates. The gate payload is the scenario-first packet of
[[charter-gate-presentation]], not a wall of test code (ADR 0019).

### Execution modes: interactive vs. autonomous (per step, emergent)

> Formalized by [[0021-step-execution-and-done]]: the `execution` step attribute
> (`agent | human | [agent, human]`) and the station/claim model. This section is the prose it
> came from; the engine mechanics live in [[execution-model-brief]].

The steps differ in how much human involvement they want, and Trigger accommodates the whole
spectrum **without hosting an interactive session** (it stays a headless orchestrator, not a
babysitter):

- **Interactive step** = a **park-and-handoff**. The workflow parks (`awaiting_input`); the
  human does the live human+agent work *in their own tool* (Claude Code / opencode / Codex),
  using the checkout handoff (worktree + WIP draft, [[checkout-and-auth]]); then resumes the
  workflow with the produced artifact. A **gate** and an **interactive step** are the *same
  `awaiting_input` mechanism* at different richness — a gate is a decision-only park, an
  interactive step is a produce-an-artifact park. No new engine kind (ADR 0008/0017-clean).
- **Autonomous step** = a headless `agent` run the supervisor dispatches (steps 3, 4, 6, 7 can
  run this way).
- **The same logical step can run either way.** The red tests (step 3) can be authored in the
  human's interactive charter session *or* handed to the tdd-agent solo — execution mode is a
  **per-run choice**, not fixed in the workflow definition.

Observed leaning from the design discussion: steps 1–2 (ground, charter) lean **interactive**;
the gates are **notification-driven** ("hey, look at this"); the middle steps vary. The actual
human/interactive/solo split per step is **emergent** — resolve it on evidence, not now
([[open-questions]] D6). Gate timeouts are long/none by default because the human may be away
for days ([[run-lifecycle-and-slots]]).

This spectrum is related to but distinct from authority modes ([[0014-answer-authority]]):
authority is about *who may answer* a gate; execution mode is about *who does the work* in a
step.

### Builder ≠ verifier

The tdd-agent, implementer, and verifier are **distinct agent steps with distinct runtimes**
(the dogfood used Gemini to build, Codex/DeepSeek to verify). The verifier confirms red-for-
the-right-reason (step 4) and green + contract-integrity (step 7). The dogfood proved this
separation catches real defects (a reviewer found an ignored-write bug the builder missed)
and is enforceable on the current runtime seam.

## State this workflow needs

Beyond the generic run/step columns ([[db-schema]]), the charter workflow carries:

- `charter_version` + ratification records (who/when/mode/within-charter — the ADR 0014 audit
  trail, in the event log).
- The **protected checkpoint** (immutable object) + **manifest** of protected-artifact
  identities/digests (ADR 0018).
- The **phase**, including `expected_red` (ADR 0020) — durable, not inferred from CI.
- The **verifier verdict** and the **review intent** (explicit data, ADR 0020).

None of these privilege "charter" in an engine primitive (ADR 0017): the checkpoint/manifest
is a general ratified-artifact capability, `expected_red` is one value of a general phase,
and review-intent is a general field. The charter workflow is their first consumer.

## Endpoint-type playbook

A chunk's endpoint decides what a "behavioral test" is. From the front-of-loop spike, carried
forward:

- **Input validation** → assert accept/reject + the reason, at the boundary.
- **Calculation → known output** → **golden fixture** (the codex cost calc: captured usage →
  exact expected cost). Goldens are protected (ADR 0018).
- **UI component** → assert rendered intent/state, not pixels.
- **TUI** → assert the view model / snapshot, not the terminal bytes.
- **Background side-effect** → assert the durable effect (row written, event emitted, file
  created), not internal calls.

The dogfood exercised input-validation, calc→golden, and background side-effect; no UI/TUI
endpoint appeared. The scenario is the portable artifact; the test is the stack-local binding
— standardize test *properties* (readable as intent, higher resolution than unit tests,
golden contracts), not a framework, since Trigger will run this against Go, TS/Node, and more.

## Agent definitions (three, + the implementer)

Content, not machinery — each is an agent def + config on the engine:

- **charter-agent** — grounds and drafts the charter (steps 1). House style: ask instead of
  guess (ADR 0013) when a scope/fact is missing.
- **tdd-agent** — authors red-first bindings + goldens from ratified scenarios (step 3).
- **verifier** — independent red/green + contract-integrity confirmation (steps 4, 7);
  never the builder.
- **implementer** — drives red to green without touching protected artifacts (step 6).

Model/runtime is per-step (ADR 0008); the dogfood showed availability and cost differ enough
that pinning per role matters.

## Non-goals (v1 of this workflow)

- No arbitrary DAG / parallel fan-out across sub-issues — linear-first (ADR 0008). Charter-
  aware autopilot fan-out is the separately-sequenced open question ([[open-questions]] D2).
- No absorbing pr-triage's reactive review into Trigger — that stays standalone for now
  ([[0016-trigger-owns-proactive-loop]]).
- No mechanically-decided "within charter" judgment yet (ADR 0014 open question).

## Open questions

- Where the protected manifest lives and who computes/verifies digests (ADR 0018).
- Whether "additive vs. weakening" classification is deterministic or delegated to the
  verifier (ADR 0018).
- Multi-base trigger selector syntax and how `expected_red` renders in status (ADR 0020).
- How autopilot fan-out (N sub-issues) composes with this linear pipeline when it arrives.
