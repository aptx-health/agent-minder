---
title: "ADR 0015 — Principal-by-transport authority binding"
status: accepted
date: 2026-08-29
tags: [architecture, security, authority, api, mcp, daemon]
superseded_by:
---

## Context

Every state-changing call — firing a job, cancelling a run, answering an `awaiting_input`
question, releasing a `blocked` run — must be attributed to a principal, because authority
depends on who is acting ([[0014-answer-authority]]): the human has full authority; an
orchestrator has charter-bounded authority. The daemon exposes several faces over one
`Service` ([[daemon-api]], [[0004-daemon-interface-split]]). The question is how a call's
principal is established, reliably and without a heavy auth layer for a single-user local tool.

## Decision

**The transport a call arrives on establishes its principal, and the principal drives
authority.**

- **Local Unix domain socket** (CLI / TUI / GUI) → principal is the **human/operator**, full
  authority. The socket is not exposed to the network; filesystem permissions are the access
  control.
- **MCP** → principal is the **orchestrator**, charter-bounded authority
  ([[0014-answer-authority]]). Its identity is the MCP client identity; beyond-charter requests
  escalate to the human.
- **Optional localhost TCP** (only if a GUI/remote view needs it; off by default) → requires a
  token stored in the Keychain ([[0006-secrets-and-agent-permissions]]); the token maps to a
  declared principal. No token, no access — fail closed.
- **Webhook route** is **not** a principal — it is inbound-only, may only emit a `Fire`, and can
  never answer, release, cancel, or read state.

Authority is enforced **inside** the `Service` methods using the resolved principal, so no face
can bypass it. The transport is the *only* thing that assigns a principal; a face cannot claim a
principal it did not arrive as.

## Consequences

- Authorization is simple and robust for a local single-user daemon: the door you came through
  is your identity, backed by OS-level guarantees (socket permissions) rather than a
  hand-rolled auth stack.
- The human path (socket) and the agent path (MCP) are cleanly separated, which is exactly the
  split [[0014-answer-authority]] needs — full vs. charter-bounded.
- Adding a network face is a deliberate, gated act (TCP + Keychain token), not an accident; the
  default posture exposes nothing.
- The webhook surface cannot escalate into a control channel, closing an obvious attack path.
- Requires the MCP client identity/authorization model to be defined (folds into R6); until it
  is, MCP principals are treated as a single charter-bounded orchestrator.
- Reversible details (socket path, whether TCP ships in v1) live in [[daemon-api]]; this ADR
  fixes only the binding rule.
