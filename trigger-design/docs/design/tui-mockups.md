---
title: "TUI mockups and interaction design (design)"
status: draft
date: 2026-08-29
tags: [design, tui, ux, mockups, attention, observability]
related: [[tui]], [[event-observability]], [[run-lifecycle-and-slots]], [[0013-ask-and-resume-instead-of-bail]], [[0014-answer-authority]], [[checkout-and-auth]], [[daemon-api]]
---

# TUI mockups and interaction design

Concrete mockup directions for Trigger's TUI, answering the brief in [[tui]] and
`tui-mockup-brief.md`. It states one recommendation and shows two alternatives. Design ideas
first; bubbletea/lipgloss is the assumed base.

> **Recommendation in one line:** a **persistent-panel base** (lazygit-style) — a fixed
> attention strip over a stable live-log stream, with the parked-run answer flow and the
> checkout menu as **summoned overlays**, and a k9s-style **drill-down** for run detail.

## Design provenance

This design harvests the current TUI wave rather than inventing from scratch:

- **lazygit / lazydocker** — persistent multi-panel, number-key panel jumps, single-letter
  actions. Source of the *spatial-consistency* rule: panels never move.
- **k9s** — drill-down stack (`list → detail → log`) with `:` power-user jumps. Source of the
  run-detail navigation.
- **btop** — Unicode block glyphs and semantic status symbols (`● ○ ◐`).
- **Bubble Tea v2** (Feb 2026) — Mode-2026 synchronized output removes tearing on a scrolling
  stream; the stable base for the live view.
- **Charm Lipgloss** — semantic style tokens, rounded borders, layered surfaces.
- **hyperb1iss/hyperskills `tui-design` skill** — the ranked pattern/anti-pattern library this
  doc's foundations follow; a useful reference at implementation time.

---

## 1. Design foundations (modern-maximal, degrades to 16-color)

The look targets Ghostty-class terminals (true color, Nerd Fonts, synchronized output) and
degrades cleanly: **usable monochrome → readable 16-ANSI → beautiful true-color**. Nothing is
color-only; every state also carries a glyph and a label.

### Semantic color tokens (not hex in code)

Define a token layer; themes swap the palette underneath.

| Token | Role | True-color intent | 16-color fallback |
| --- | --- | --- | --- |
| `bg.base` | app background | near-black | default bg |
| `bg.surface` | panels (~6% lighter) | raised panel | — |
| `bg.overlay` | modals (~12% lighter) | floating card | reverse |
| `text.primary` / `text.muted` | body / secondary | white / grey | default / dim |
| `status.needs` | **awaiting_input** | amber | yellow |
| `status.blocked` | **blocked / failed** | red | red |
| `status.running` | in flight | cyan | cyan |
| `status.ok` | succeeded | green | green |
| `accent.primary` | focus / selection | violet | magenta |
| `evidence.fact` vs `evidence.infer` | deterministic vs AI | normal vs italic-dim | — |

### State visual language (state machine → glyph + color + style)

Each run state ([[run-lifecycle-and-slots]]) reads at a glance:

| State | Glyph | Color | Card style |
| --- | --- | --- | --- |
| `awaiting_input` | ` ` (question) | amber | loud, top of attention strip |
| `blocked` | ` ` (stop) | red | loud, top of attention strip |
| `running` | `◐` (animated) | cyan | live |
| `queued` | `○` | muted | quiet |
| `succeeded` | `✓` | green | quiet |
| `failed` | `✗` | red | quiet, with failing step |

### Motion (subtle, purposeful)

- `running` glyph is a slow spinner (`◐◓◑◒`); a live step line pulses dim→normal.
- New attention items **slide in and flash once**, then settle — never blink continuously.
- **Durable vs ephemeral** ([[event-observability]]): state transitions render solid;
  progress telemetry renders dim/italic and is **id-less** — it is never counted as history.
- Async rule: no action ever freezes the UI; `Esc` always returns control. Checkout runs in
  the background with a spinner.

### Connection and integrity (crash-safe)

The header carries a live indicator and the `(epoch, id)` cursor. On reconnect the client
resumes from its cursor; on **epoch mismatch** it discards the cursor, resyncs current state,
and shows a one-line toast — it never silently drifts ([[event-observability]]).

```
  ● live · epoch 3        (green dot = streaming)
  ◌ reconnecting…         (amber, retrying from cursor)
  ⟳ resynced (epoch 3←2)  (toast after epoch rotation)
```

Secrets are never rendered — `summary`/`data` are already secret-free at the source
([[event-observability]]).

---

## 2. Recommended direction — persistent panels + overlay

One stable screen. A fixed **attention strip** sits above a stable **live stream**; neither
moves as counts change (that is the lazygit spatial-consistency rule — a growing banner would
shove the log around and reset the operator's spatial map). Detail and actions arrive as
**overlays** summoned with `enter` / `c`, dismissed with `Esc`.

```
╭─ Trigger ───────────────────────────────── ● live · epoch 3 ─╮
│                                                              │
│   2 NEED YOU        ◐ 3 running        ✓ 12 ok · ✗ 1 failed  │
│                                                              │
├─ Needs you ──────────────────────────────────────────────────┤
│    run#41  nightly-triage    "which base branch?"      3m    │
│    run#38  deps-update       tests failed · step build 12m   │
├─ Live ───────────────────────────────────────────────────────┤
│  12:04:11  run#42  step.started    build                     │
│  12:04:09  run#42  ▪ compiling packages…            (live)   │
│  12:03:58  run#40  ✓ run.succeeded      PR #211              │
│  12:03:41  run#39  ↻ step.retrying      push (2/3)           │
│  12:03:02  run#37  ✓ run.succeeded                          │
│  12:02:40  run#36  ⚑ authority.escalated  beyond charter    │
│  …                                                          │
╰─ ↑↓ move · enter act · c checkout · l log · f fire · ? help ─╯
```

**Why this wins.** It keeps the scrolling log stream as the resting state (the view Dustin
actually used), and it makes parked runs a strip he cannot scroll past — impossible to miss,
yet it never displaces the log. The attention strip is fixed-height and scrolls internally if
more than a few runs park, so the layout below stays put. When zero runs need attention the
strip collapses to a single calm line:

```
│    all clear · nothing needs you                             │
```

**Handling the tension (parked headline vs. live stream):** they are *stacked, not
competing*. The strip owns the top few rows and only the attention family; the stream owns the
rest and is a pure firehose. `Tab` moves focus between the two panels (footer keys change with
focus — contextual keybindings); `enter` on a strip item opens the answer overlay, `enter` on
a stream line opens that run's detail.

### 2a. Parked-run detail + answer overlays

`enter` on a parked run opens a centered overlay (`bg.overlay`, rounded). Three shapes share
one release path ([[0013-ask-and-resume-instead-of-bail]], [[run-lifecycle-and-slots]]); the
header states the parking reason and the authority mode.

**A question (`awaiting_input`, free-form or schema):**

```
        ╭─  run#41 · awaiting_input ──────────────────────╮
        │  job   nightly-triage        parked  3m ago     │
        │  step  choose-base           timeout in 57m     │
        │  authority  interactive · you may answer        │
        │                                                 │
        │  Question                                       │
        │    Which base branch should the PR target?      │
        │                                                 │
        │  Answer  (schema: string)                       │
        │    ┌───────────────────────────────────────┐   │
        │    │ main▎                                   │   │
        │    └───────────────────────────────────────┘   │
        │  Evidence  repo default is `main`; release/*    │
        │            branches exist (2)         [l] log   │
        │                                                 │
        ╰─ enter submit · Esc cancel · l open log ────────╯
```

The answer is validated against the declared schema before submit, injected as **data, not
instructions**, and the same runtime session resumes in place — no restart.

**A scope request (grant mutates policy, then resumes):**

```
        ╭─  run#38 · awaiting_input · scope request ──────╮
        │  job   deps-update           parked  1m ago     │
        │  authority  orchestrator · beyond charter →     │
        │             escalated to you                    │
        │                                                 │
        │  Requests                                       │
        │     write  package-lock.json                    │
        │     run    `npm audit fix`                      │
        │  Reason  transitive CVE fix needs a lockfile     │
        │          write outside the declared path        │
        │                                                 │
        ╰─ g grant (scoped to this run) · d deny · Esc ───╯
```

Granting updates the run's permission record for the approved continuation, then resumes
([[0006-secrets-and-agent-permissions]]). A beyond-charter request is flagged and always
routes to the human, whatever the mode ([[0014-answer-authority]]).

**A blocked run (diagnose, then release):**

```
        ╭─  run#38 · blocked ─────────────────────────────╮
        │  failing step  test          attempt 3/3        │
        │  class  logic (on_failure: escalate)            │
        │                                                 │
        │  failure_detail                                 │
        │    2 tests failed in api/handlers_test.go       │
        │    · TestClaim_RaceFree  · TestClaim_PerJobCap   │
        │                                                 │
        │  Release note                                   │
        │    ┌───────────────────────────────────────┐   │
        │    │ flaky claim test, re-run▎               │   │
        │    └───────────────────────────────────────┘   │
        │                                                 │
        ╰─ r release (→queued) · c checkout · l log · Esc ╯
```

Release is `blocked → queued`, resets attempts, attaches the note (who/why), and is
idempotent — the same path as ask-and-resume.

### 2b. Checkout / actions menu (the interaction Dustin loved, extended)

`c` on any run opens a compact actions overlay. Checkout runs in the background (async rule);
the menu shows progress and does not block the stream.

```
        ╭─  run#40 · actions ─────────────────────────────╮
        │   w  checkout worktree   ~/.trigger/wt/run-40   │
        │   p  open PR             #211  ↗                 │
        │   l  tail agent log      (follow)               │
        │   L  open raw log        $PAGER                  │
        │   d  cd shell here                              │
        │   x  cancel run                                 │
        │                                                 │
        │   provenance  claude-code · 1m48s · $0.06 · 14t  │
        ╰─ key to run · Esc close ────────────────────────╯
```

The `provenance` line is an **evidence/basis label** (model, cost, turns) — facts, not
conclusions ([[tui]]). It harvests [[checkout-and-auth]] for the worktree + keyring flow.

### 2c. Run detail (drill-down, k9s-style)

`enter` on a stream line (or a picked run) shows the run as its **workflow steps** — the
north-star from the brief. Steps are deterministic facts; the log tail is live.

```
╭─ run#42 · nightly-triage · running ─────────── ◐ step 2/4 ─╮
│  ✓ 1 checkout      0.4s                                    │
│  ◐ 2 build         compiling…            (live)            │
│  ○ 3 test          queued                                  │
│  ○ 4 open-pr       queued                                  │
├─ log (follow) ─────────────────────────────────────────────┤
│  12:04:11 build: go build ./...                            │
│  12:04:12 build: ▪ internal/db…                            │
╰─ Esc back · l full log · c checkout · x cancel ────────────╯
```

Later, behavioral scenarios going red→green slot into the same step list as sub-rows.

---

## 3. Alternative directions (considered, not recommended)

### B — Master-detail drill-down (k9s-style throughout)

Runs list (attention-sorted) on the left; selected run's steps + log tail on the right. The
log becomes **per-run** rather than a global firehose.

```
╭─ Runs ────────────┬─ run#41 · awaiting_input ─────────────╮
│    41  await  3m  │  Q: which base branch?                │
│    38  block  12m │  build ✓  test ✓  choose-base         │
│  ◐ 42  run    0m  │  ── log ──────────────────────────    │
│  ✓ 40  ok     1m  │  12:04 asking question…       (live)  │
│  ↻ 39  run    2m  │                                       │
│  ✓ 37  ok     5m  │  [ans] answer  [r] release  [c] co    │
╰───────────────────┴───────────────────────────────────────╯
```

**Rationale / tension:** parking sorts to the top of the list and the detail pane answers in
place — clean and power-user friendly. The cost: there is **no global live stream**; the log
Dustin watched is now gated behind selecting a run. Strong for triage, weaker for his
ambient-watching habit. Good as the *drill-down layer* (§2c), not the home screen.

### C — Tabbed panes (Attention / Live / Jobs)

```
╭ [ Attention ●2 ] [ Live ] [ Jobs ] ───────── ● live ─╮
│    run#41  which base branch?          [answer]      │
│    run#38  tests failed · step build   [release]     │
│                                                      │
│  (the live stream lives under the Live tab)          │
╰─ 1/2/3 tab · enter act · ? help ─────────────────────╯
```

**Rationale / tension:** cleanest separation and the least busy screen. But it **hides parked
runs behind a tab**, which fights "impossible to miss" — it needs the persistent `●2` badge to
compensate, and even then attention lives one keystroke away instead of on screen. Rejected as
the default for exactly that reason; the tab badge idea is worth borrowing for a narrow
terminal fallback.

---

## 4. Keyboard interaction model

Four progressive layers (footer shows the live set; `?` shows all; keys are **contextual** —
they change with the focused panel/overlay).

**L0 — universal (always in footer):**
`↑↓`/`jk` move · `enter` act/drill-in · `Esc` back/cancel · `Tab` switch panel · `q` quit ·
`?` help.

**L1 — home screen actions:**

| Key | Action |
| --- | --- |
| `enter` | open parked answer (on strip) / run detail (on stream) |
| `c` | checkout / actions menu |
| `l` | tail agent log (follow) · `L` open raw log in pager |
| `p` | jump to PR |
| `f` | fire a manual job (opens job picker) |
| `x` | cancel the selected run |
| `/` | filter the stream (by run/job/severity) |
| `g` | go-to: `:run N`, `:job name` (k9s-style jump) |

**L2 — inside the answer overlay:**
`enter` submit answer · `g` grant scope · `d` deny · `r` release blocked · `l` log · `Esc`
cancel.

**L3 — power:** command palette (`:`), saved filters, theme switch. Documented, not on screen.

---

## 5. Progressive disclosure map (default vs. on demand)

| Layer | Shows by default | On demand |
| --- | --- | --- |
| **Attention strip** | counts + the parked runs (job, one-line ask/failure, age) | full request payload, evidence, schema (overlay) |
| **Live stream** | durable transitions + the current live step line | full step list, per-step timing, log tail (drill-in) |
| **Run** | state glyph, job, age | steps, provenance (model/cost/turns), worktree/PR (actions) |
| **Failures** | failing step name | `failure_detail`, attempts, class (overlay) |
| **Progress telemetry** | dim/italic, live-only | never promoted to history (id-less) |
| **Footer** | 3–5 keys for the focused context | `?` full keymap; `:` command palette |
| **Secrets** | never | never |

Default answer to "does anything need me?": the strip's headline count. Everything else is one
keystroke deeper.

---

## 6. Constraints and conflicts to resolve

Points where the design docs pull against good UX — flagged for the humans:

1. **Firehose vs. server-side filters is unresolved.** [[event-observability]] leaves open
   whether the TUI gets server-side topic filters or filters a firehose client-side. The
   recommended stream + `/` filter assumes a **firehose for v1**; a busy daemon could outrun a
   client-side filter. *Resolve:* confirm firehose-for-v1, or the `/` filter needs a
   server-side `?type=`/`?run=` on the SSE endpoint ([[daemon-api]]).

2. **Authority mode must be visible, but the docs don't say where it lives per run.**
   [[0014-answer-authority]] defines `human_only` / `orchestrator` / `interactive` and says
   beyond-charter escalates. The overlay shows the mode and the charter verdict — but "within
   charter" may be an LLM judgment (ADR 0014 open question). *Resolve:* the TUI needs a
   deterministic field to render (`within_charter: true|false|unknown`); an `unknown` must not
   look like an approval.

3. **Answered-by-orchestrator runs still need a trace, not silence.** In `orchestrator` mode
   the human is not interrupted, yet [[0014-answer-authority]] requires a durable audit trail.
   *Resolve:* auto-answered runs should still flash through the stream (`run.answered` with
   who/mode) so the passenger-seat operator sees what was decided, even when they did not act.

4. **Timeout-to-blocked can surprise the operator.** An `awaiting_input` run silently becomes
   `blocked` on timeout ([[run-lifecycle-and-slots]]). *Resolve:* the overlay shows the live
   countdown (done, §2a); the strip should sort *soonest-to-expire first* so a dying question
   is not buried.

5. **Epoch resync must be legible, not a flicker.** [[event-observability]] mandates
   discard-and-resync on epoch mismatch. A full re-render can look like a crash. *Resolve:*
   the `⟳ resynced` toast (done, §1) plus preserving scroll position where possible.

---

## References

- [[tui]] — the brief this doc answers.
- [[event-observability]] · [[run-lifecycle-and-slots]] · [[0013-ask-and-resume-instead-of-bail]]
  · [[0014-answer-authority]] · [[checkout-and-auth]] · [[daemon-api]].
- External provenance: Charm Bubble Tea v2 / Lipgloss; lazygit; k9s; btop;
  hyperb1iss/hyperskills `tui-design` skill; the "TUI Renaissance 2026" writeups.
