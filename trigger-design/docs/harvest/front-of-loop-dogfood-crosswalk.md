---
title: "Front-of-loop dogfood crosswalk — the codex-runtime run-through as charter-workflow input spec"
status: accepted
date: 2026-08-30
tags: [harvest, research, charter, front-of-loop, conformance, evidence]
source: pr-triage docs/experiments/issue-129-front-of-loop-report.md
related: "[[charter-workflow]], [[0016-trigger-owns-proactive-loop]], [[0018-ratified-contract-protection]], [[0019-human-attention-budget-conformance-layers]], [[0020-expected-red-and-topology-agnostic-review]], [[charter-gate-presentation]]"
---

# Front-of-loop dogfood crosswalk

[[0016-trigger-owns-proactive-loop]] and [[open-questions]] D5 said the charter workflow's
design would be **grounded in a real run-through, not invented**. That run-through happened:
the pr-triage codex-runtime chunk (issue #129) was executed as a full manual pass of
`ground → charter gate → red tests → red gate → implement → verify → PR`, instrumented
against the earlier OpenCode chunk as a baseline. Source of truth for the numbers:
`pr-triage/docs/experiments/issue-129-front-of-loop-report.md`.

This doc is the *why* behind the charter ADRs and design — the same role
[[fable-expedition-crosswalk]] plays for the harvest. It records what the run proved and maps
each finding to the decision it drove.

## Verdict

**Largely a success, and the process earned its keep.** The chunk landed green first-pass at
the ratified gate; the charter held with no drift; one post-green defect was caught by an
independent verifier, not the builder. The failures were about *presentation and governance*,
not about whether front-loading works — and those failures are exactly what ADRs 0018–0019
fix.

## Headline numbers (vs. OpenCode baseline)

| Measure | Dogfood | Baseline | Read |
| --- | ---: | ---: | --- |
| First-pass GREEN at ratified gate | yes (1 attempt) | — | Grounded contracts + red bindings gave a stable target. |
| Things not considered ahead of time | 6 | 9 | Grounding absorbed most runtime unknowns pre-code. |
| Tool-contract unknowns | 1/6 | 5/9 | The primary hypothesis: **grounding pays**. |
| Findings invisible to plain unit tests | 62.5% | ~40% | Residual set shifted to topology/identity/governance seams — the drift class unit tests can't see. |
| Post-green rework cycles | 1 | — | Independent verifier found an ignored-write bug. |

## Findings → decisions

### Grounding has the highest ROI → [[charter-workflow]] step 1
Grounding turned CLI/auth/sandbox/JSONL/pricing unknowns into ratified decisions and caught a
stale credential name and a non-existent shared price table in the draft charter. Moving
tool-contract unknowns 5/9 → 1/6 is the single strongest result. **Decision:** grounding is a
first-class first step, not optional prep.

### The gate drowned the human in bindings → [[0019-human-attention-budget-conformance-layers]], [[charter-gate-presentation]]
32 bindings for 9 scenarios were presented as 32 equal decisions; the human spot-checked.
**Decision:** three conformance layers with distinct owners; gates present scenarios (~≤12),
not bindings; progressive disclosure; ≤5 decisions.

### Ratified tests were launderable → [[0018-ratified-contract-protection]]
The scanner flagged deleted tests / new `t.Skip` but not modified assertions or changed
goldens, and excluded all `testdata/`. Protection was manual (a hand-taken checkpoint commit
+ manual diff). Yet a *legitimate* additive test appeared during smoke — freezing would have
blocked it. **Decision:** immutable checkpoint + digest manifest + deterministic
`ratified_contract_changed` signal that **escalates, not freezes**; traced additions allowed.

### Expected RED vs. broken, and review-intent-by-branch → [[0020-expected-red-and-topology-agnostic-review]]
Red-first output is indistinguishable from breakage if CI is treated as truth; and a PR to
`main` was force-classified `chunk_completion`, with only one base selector per repo, so the
dogfood PR was invisible until the base was hand-changed. **Decision:** `expected_red` is a
durable phase; review intent is explicit data; GitHub is a projection; multi-base selectors
are needed. (Mirrors pr-triage ADR 0010.)

### Builder ≠ verifier caught a real bug → [[charter-workflow]] (roles)
The builder (Gemini) missed an ignored envelope-write error; an independent reviewer (Codex),
then a second (DeepSeek), found/confirmed it. **Decision:** tdd-agent, implementer, verifier
are distinct agent steps with distinct runtimes.

### Agent prose counts are unreliable → [[charter-gate-presentation]]
The same suite was reported as "19", "32", and "34" by different agents. **Decision:** use
deterministic machine inventory (`go test -list`, `rg`) for all counts at gates.

### Orchestrator context bloat → informs [[run-lifecycle-and-slots]] / [[daemon-api]]
Streaming a sub-agent's verbose NDJSON into the trunk context was pure cost; duplicating
routine review "for confidence" wasted the attention the tool exists to save. **Decision
(engine-side, already Trigger's model):** the supervisor attends to bounded terminal handoffs
— PR identity, checks, contract-integrity verdict, review findings — not transcripts. This
validates the store-first event log + snapshot-then-stream API already in the design.

## What did not transfer (and why)

- **The trunk/two-level PR topology** (draft chunk PR ← child issue PRs) is one *useful
  projection*, not a required topology — folded into ADR 0020, not adopted as machinery.
- **Autopilot fan-out** (N sub-issues → parallel red-green) stays a separately-sequenced open
  question ([[open-questions]] D2); the dogfood was a single linear chunk and does not settle
  it.
- **Reactive review** stays in pr-triage for now ([[0016-trigger-owns-proactive-loop]]); the
  dogfood confirms the charter travels to the reviewer as the oracle across the loose GitHub
  seam.

## Residual open questions carried into the ADRs

Manifest location/owner and digest scheme (0018); deterministic vs. agent classification of
additive-vs-weakening (0018); multi-base selector syntax and `expected_red` rendering (0020);
whether the ~12-scenario / ≤5-decision targets are caps or guidance (0019).
