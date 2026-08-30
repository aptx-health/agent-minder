---
title: "Charter gate presentation — delivering test evidence to a human"
status: accepted
date: 2026-08-30
tags: [guidance, charter, attention, tui, gates, human-in-the-loop]
related: "[[0019-human-attention-budget-conformance-layers]], [[charter-workflow]], [[0013-ask-and-resume-instead-of-bail]], [[tui]], [[event-observability]], [[front-of-loop-dogfood-crosswalk]]"
---

# Charter gate presentation

How a charter ratification gate ([[charter-workflow]] steps 2 and 5) shows a busy human
enough to *decide well* without drowning them. This is the concrete "how to deliver test
information to a human" guidance the front-of-loop dogfood asked for, and the applied form of
[[0019-human-attention-budget-conformance-layers]]. It is workflow-general presentation
guidance, not charter-only engine behavior.

## The failure this prevents

The dogfood's red gate initially presented **32 executable bindings as 32 equal decisions**.
The human spot-checked instead of reading — which silently defeats a ratification gate. The
fix was not fewer tests; it was presenting the **human's layer (scenarios)**, with the
**machine's layer (bindings)** available underneath as progressive-disclosure detail.

## Principles

1. **Scenario-first.** The primary surface is ~≤12 scenario cards, each: the observable
   must/must-not behavior, its red evidence, and a scenario→binding count. Full test code is
   one keystroke away, never the front page.
2. **Progressive disclosure.** scenario card → sentence-style test name + breadcrumb → one
   representative right-reason RED output → per-scenario summary → full suite + fixtures on
   demand.
3. **Evidence, not conclusions.** Show the actual failing assertion and expected value; let
   the human conclude. Never "trust me, it's red."
4. **Bounded decisions.** Aim for ≤5 distinct questions at a gate (see the five below).
5. **Deterministic counts.** Use machine test inventory (`go test -list`, `rg '^func Test'`),
   never an agent's prose count — the dogfood saw agents report "19", "32", "34" for the same
   suite. Machine inventory is authoritative.
6. **Untracked files count.** `git diff` hides new files; a new test package lives in
   untracked files at the red gate. Always pair `git status` + `git ls-files --others` with
   the diff.

## The five decisions at the red gate

Distilled from the dogfood, the human should decide only:

1. Do the test names read as the intended behavior?
2. Are the captured success/failure goldens credible and minimally normalized?
3. Are any adapter-local contracts (e.g. an envelope schema) acceptable?
4. Are the known/unknown/unavailable cost (or equivalent) semantics honest?
5. Is any charter behavior missing or over-specified?

Approval turns goldens into protected contracts ([[0018-ratified-contract-protection]]);
later golden changes become an escalation.

## Companion-terminal inspection path

A gate should hand the human a copyable sequence, broadest → narrowest. From the isolated
worktree:

```bash
# Complete inventory — diff alone hides untracked files.
git status --short
git diff --stat
git ls-files --others --exclude-standard

# Intent, broad → narrow.
bat <charter-file>
rg -n '^func Test' <test-glob>
bat <one-representative-test-file>
bat <goldens>

# Evidence in shrinking slices.
go test -list . <pkg>                      # inventory, no execution
go test -count=1 -v <pkg>/...              # whole suite RED
go test -count=1 -v <pkg> -run '^TestOneRepresentativeContract$'
```

Untracked-file viewing without staging:
`git diff --no-index -- /dev/null <newfile> | less -R` (exit 1 means "different", not error).
Detected local viewers worth surfacing: `bat`, `git diff --color=always | less -R`,
`lazygit`.

## For the TUI

The [[tui]] should render the gate as scenario cards with a red/green badge and a drill-down,
not a scrollback of raw test output. The event stream ([[event-observability]]) carries the
per-scenario red/green transitions so the red→green turn is visible live — the "watch red
turn to green" experience the front-of-loop was designed around. Keep raw logs as detail, not
the headline.
