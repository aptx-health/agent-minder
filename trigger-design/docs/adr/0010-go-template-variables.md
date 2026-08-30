---
title: "ADR 0010 — Go-template variable interpolation"
status: accepted
date: 2026-08-29
tags: [architecture, templating, config, context, pr-triage]
superseded_by:
---

## Context

Steps need variables: the issue number that fired a trigger, an output from a prior step,
an env value. agent-minder already renders context and prompts with Go templates; pr-triage
can adopt the same. Standardizing on one templating convention is also a cross-tool
alignment lever ([[0009-cross-tool-boundary-shared-conventions]]) — a shared convention that
costs nothing at the boundary and lets prompt/config snippets move between the tools.

Templating is also the concrete mechanism for the **bounded, explicit context passing**
between workflow steps that [[0008-workflows-deterministic-steps]] rule 4 requires.

## Decision

**Interpolate variables with Go `text/template`** in step `run` commands, agent `prompt`
text, and `with` values. A defined, bounded variable context is exposed:

- `.trigger` — data from the firing trigger (e.g. `.trigger.github.issue.number`).
- `.steps.<name>` — a prior step's recorded result (output/status), read from the run
  record. Referencing a step that has not run yet is a load/validation error.
- `.env` — resolved environment for the job/step.
- `.job`, `.step` — identity/metadata of the current job and step.

Rules:

1. **Templating is explicit and bounded.** Only the context above is available — no ambient
   global scratch space (consistent with ADR 0008 rule 4).
2. **Secrets are never templated into visible strings.** A `run` or `prompt` references a
   secret by name in `secrets:`; the daemon injects it at execution, out of band. Templates
   must not interpolate secret values, so logs and the event stream never capture them
   ([[0006-secrets-and-agent-permissions]]).
3. **Resolve at run start, store the rendered result.** Rendering happens once when the step
   runs, using resolved config ([[config-resolve-once]]); the rendered command/prompt is
   recorded on the run.
4. **Missing variable is a hard error, not an empty string.** A reference that cannot resolve
   fails the step loudly rather than silently rendering blank.

## Consequences

- Prior-step outputs flow forward through a named, inspectable channel — the workflow's
  context passing has a concrete, safe mechanism.
- One templating convention shared with pr-triage: prompt and config snippets are portable
  between the tools without translation.
- The `text/template` choice is deliberate: standard library, no dependency, familiar from
  agent-minder. Sprig-style function packs are out of v1 scope — add only on evidence.
- Load-time validation must parse templates and check variable references against the
  declared context, so a bad reference fails before the daemon runs anything.
