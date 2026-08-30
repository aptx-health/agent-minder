---
title: "TUI mockup brief — prompt for a design agent"
status: reference
date: 2026-08-29
tags: [design, tui, prompt, mockups]
related: [[tui]]
---

# TUI mockup brief (paste this to a design agent)

The block below is a self-contained prompt. Paste it into a design agent that has read access
to this repo. It produces terminal-UI mockup directions for Trigger's TUI.

---

You are a senior terminal-UI / TUI designer. Design the TUI for **Trigger**, a local Go daemon
that runs scheduled and triggered agent workflows. Produce **mockups and interaction design**,
not code.

**First, read these files in this repo for full context (do not skip):**
- `trigger-design/docs/design/tui.md` — the design brief (intent, requirements, priorities). This is your primary spec.
- `trigger-design/docs/design/event-observability.md` — the event stream and taxonomy the TUI renders.
- `trigger-design/docs/design/run-lifecycle-and-slots.md` — run states, including the `blocked` and `awaiting_input` parking states.
- `trigger-design/docs/adr/0013-ask-and-resume-instead-of-bail.md` and `0014-answer-authority.md` — the "agent asks a question / requests scope" flow the TUI must let the human answer.
- `trigger-design/README.md` — orientation.

**Hard requirements (from the brief):**
- The TUI is an **attention-routing** surface: at a glance, "does anything need me, and can I act here?"
- **Parked runs are the headline** — `awaiting_input` (agent asked a question / requested scope) and `blocked` (failure). They must be impossible to miss and **actionable in place** (answer, grant scope, release).
- Preserve a strong **live activity / log stream** view — the operator's historical primary UI was watching supervisor logs scroll.
- Preserve and extend the **"checkout" interaction**: summon a run's worktree locally, jump to its PR, open its raw log — first-class.
- Progressive disclosure (minimal default, detail on demand); evidence over conclusions; distinguish durable state from ephemeral progress; never show secrets.
- Assume a **bubbletea/lipgloss** terminal app (but design ideas first, framework second).

**Deliver:**
1. **2–3 distinct layout/navigation directions** (e.g. adaptive single view; tabbed panes; master-detail list). For each: an **ASCII/box-drawing mockup** of the main screen, a one-paragraph rationale, and how it handles the tension between "parked runs headline" and "live log stream."
2. For your recommended direction: mockups of **the parked-run detail + answer flow** (answering a question, granting a scope request, releasing a blocked run) and **the checkout/actions menu**.
3. A **keyboard interaction model** (keys for navigate, checkout, open log, jump to PR, answer, release, fire, cancel).
4. A short **"what shows by default vs. on demand"** progressive-disclosure map.
5. Call out anything in the design docs that constrains or conflicts with good UX, so the humans can resolve it.

Keep it concrete and skimmable. Prefer showing a mockup over describing one. State a clear recommendation.
