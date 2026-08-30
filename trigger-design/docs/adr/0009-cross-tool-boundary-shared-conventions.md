---
title: "ADR 0009 — Cross-tool boundary: shared conventions over shared code"
status: accepted
date: 2026-08-29
tags: [architecture, boundary, reuse, portability, pr-triage]
superseded_by:
---

## Context

Trigger and pr-triage are separate tools with opposite theses (Trigger = scheduling and
triggering headless work; pr-triage = review, attention, and behavioral conformance). They
share substrate: agent runtimes, config primitives, the ACP seam, and — the sharp worry —
**agent types and learnings useful to both.** Dustin's fear: he improves an agent type or
learns something in one repo and must hand-port the change to the other, and the two drift.

The two products must stay separate ([[project boundary]] — do not merge them), so the fix
is not one codebase. It is a discipline for what crosses the boundary and how.

pr-triage already has the seed: its Locked "portable agent definitions" ADR defines each
agent once in a neutral `agents/<name>.agent.yaml` and generates every tool's native format,
with CI failing on drift. That solved hand-duplicated agent files *within* one repo. The
cross-repo problem is the same idea one level up.

## Decision

**Prefer shared conventions and formats over shared code. Make the artifacts that both tools
value portable by format, so a useful change is a copy, not a re-implementation.**

1. **Agent definitions are portable artifacts.** Both tools consume the same neutral agent
   source format (pr-triage's `agents/<name>.agent.yaml`). An agent type useful for both is a
   single file that drops into either repo unchanged — no code port. This is the primary
   answer to the duplication worry.
2. **Shared conventions, not shared runtime, are the default coupling.** The neutral agent
   format, the Go-template variable convention ([[0010-go-template-variables]]), ACP as the
   runtime seam ([[0003-acp-runtime-seam]]), and the config resolve-once discipline
   ([[config-resolve-once]]) are all *conventions* both tools follow independently. Following
   the same convention costs nothing at the boundary; sharing a live dependency costs a
   versioned interface.
3. **Extract shared *code* only on evidence.** When the same non-trivial code must genuinely
   live in both (an ACP client, the agent-def generator, a templating helper), extract a small
   shared Go module — but only after the same change is made in both repos a second time
   (extract-on-force, per the pr-triage spike §7 modular-monolith rule). Do not pre-build a
   shared library.
4. **The "glue" is a thin, explicit seam, never a merge.** When something must cross that a
   format cannot carry, write a narrow adapter/shared module at that one point. Keep the seam
   small and named; resist letting it grow into a shared core that recouples the products.

## Consequences

- The common case (a better agent type, a shared prompt convention) crosses as a portable
  file — cheap, no drift-by-reimplementation.
- The two products stay independently deployable and independently evolvable.
- A shared Go module may appear later (extract-on-force) for the runtime/ACP/generator layer;
  that is expected and fine — it is a small library, not a merged app.
- Learnings/lessons cross as documentation or portable knowledge-base entries (same front
  matter discipline), not as code both tools must import.
- Design rule: before duplicating anything across the two repos, ask "can this be a portable
  artifact or a shared convention instead of copied code?" Copy code only when the answer is
  no, and then prefer extracting it once the duplication is proven.
