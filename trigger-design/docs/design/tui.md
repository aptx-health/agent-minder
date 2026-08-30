---
title: "TUI design brief (design)"
status: draft
date: 2026-08-29
tags: [design, tui, ux, observability, attention]
related: [[0007-agent-controllable-mcp-server]], [[0004-daemon-interface-split]], [[event-observability]], [[run-lifecycle-and-slots]], [[0013-ask-and-resume-instead-of-bail]], [[checkout-and-auth]]
---

# TUI design brief

Requirements and intent for the terminal UI. **Not** visual mockups — those are commissioned
from a design agent using this brief (see `tui-mockup-brief.md`). Implementation-neutral, but
bubbletea/lipgloss is the assumed base (harvest-friendly, Dustin's prior preference).

## What the TUI is for

An **attention-routing** surface, not a dashboard for its own sake. It exists to answer, at a
glance: *does anything need me right now, and can I act on it here?* This follows the product
thesis ([[0007-agent-controllable-mcp-server]] / the pr-triage attention principle): route the
operator's limited attention to what matters, present evidence not conclusions.

It is a **thin client over the daemon API** ([[0004-daemon-interface-split]]) — an SSE stream
consumer ([[event-observability]]) that holds no authoritative state. The daemon remains the
source of truth; the TUI is a view plus a set of actions.

## Ground truth about the operator (Dustin)

- His **primary UI historically was watching the supervisor logs** stream by. So a strong,
  legible live activity/log view is not optional — it is the thing he actually used.
- The **one interaction he loved** in the old minder TUI was `checkout` — summon a run's
  worktree locally and jump to the PR / do useful things with the work. Keep that class of
  action first-class.
- He is **not** attached to the rest of the old layout. Better ideas are welcome.

## What it must surface (in attention priority)

1. **Parked runs — the headline.** `awaiting_input` (an agent asked a question or requested
   scope, [[0013-ask-and-resume-instead-of-bail]]) and `blocked` (a failure needs diagnosis,
   [[run-lifecycle-and-slots]]). These are where attention is owed; they must be impossible to
   miss and **actionable in place** (answer / grant scope / release). This is the biggest
   advance over the old logs-only workflow.
2. **Live activity.** Running runs and their current step, streamed. Distinguish **durable
   state transitions** from **ephemeral progress telemetry** (Expedition IV: progress is
   live-only, id-less — do not treat it as state). Keep raw log access one keystroke away.
3. **Recent outcomes.** Succeeded / failed, with the failing step and evidence for failures.
4. **Jobs & schedules.** Configured jobs, next cron fires, what is queued.

## Interactions (the "checkout" class, kept and extended)

- **Checkout / summon** a run's worktree locally; **jump to the PR**; **tail/open the raw
  agent log** (his primary view — first-class, not buried). Harvest [[checkout-and-auth]].
- **Answer** an `awaiting_input` question / **grant** a scope request; **release** a `blocked`
  run — inline, from the parked item. The operator on the local socket is the human with full
  authority ([[0015-principal-by-transport]] / [[0014-answer-authority]]).
- **Fire** a manual job; **cancel** a run.

## Principles the design must honor

- **Progressive disclosure.** Default view is the minimal signal ("N need you / all clear");
  detail on demand. Curate, do not dump.
- **Evidence over conclusions;** mark deterministic facts vs. AI inferences; show basis labels
  honestly (cost/turns/model provenance, [[fable-expedition-crosswalk]]).
- **Crash-safe.** Reconnect resumes from the `(epoch, id)` cursor; on epoch mismatch, resync
  and re-render rather than silently drift ([[event-observability]]).
- **Never the source of truth,** never blocks the daemon, never shows secrets.

## Open questions (for the design agent to explore)

- Overall layout/navigation: single adaptive view vs. tabbed panes vs. a master-detail list.
- How the parked-runs headline coexists with the live log stream Dustin likes.
- How to render a run as its workflow steps (and, later, behavioral scenarios going red→green —
  the north-star from pr-triage's spike).
- Keyboard model for the checkout/answer/release actions.
