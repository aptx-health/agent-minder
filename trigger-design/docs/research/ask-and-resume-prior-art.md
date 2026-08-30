---
title: "Prior art — pause-to-ask/escalate, then resume (2026)"
status: reference
date: 2026-08-29
tags: [research, reference, human-in-the-loop, mcp, acp, agents]
related: [[0013-ask-and-resume-instead-of-bail]]
---

# Prior art: "pause to ask/escalate, then resume"

Research backing [[0013-ask-and-resume-instead-of-bail]]. Captured so the findings survive a
repo move.

## Mechanism comparison

| Mechanism | Who/what | How pause+resume works | Name |
| --- | --- | --- | --- |
| **MCP elicitation** | MCP server → client | Server sends `elicitation/create` (JSON Schema) mid-request; client returns accept/decline/cancel; same handler continues | Elicitation |
| **LangGraph `interrupt()`** | Graph node | `interrupt(value)` freezes state via checkpointer; caller resumes with `Command(resume=value)`; node re-runs from top, `interrupt()` returns the value | Interrupt / HITL checkpoint |
| **OpenAI Agents SDK** | Tool `needs_approval` | Run pauses, returns pending interruptions + `RunState`; caller approves/rejects, passes state back; same run resumes | Interruptions / "paused run, not a new turn" |
| **Claude Agent SDK** | `canUseTool` / `can_use_tool` | Per-tool callback blocks until `{behavior: allow|deny}`; in-process await | Permission callback |
| **ACP `session/request_permission`** | Agent → client | Tool call + `PermissionOption[]`; client returns outcome; same turn continues | Permission request |
| **Temporal signals** | Workflow | `wait_condition()` suspends (zero compute), state persisted; a Signal/Update injects the decision; replay resumes at the wait point; durable timer for timeout | Signal / durable wait |
| **AutoGen / CrewAI** | Agent / task | `human_input_mode` (NEVER/TERMINATE/ALWAYS); `human_input=True` review gate; blocking prompt | Human-input mode |

## The hard constraint for Trigger

**No protocol Trigger speaks natively supports full ask-and-resume.**

- **ACP (downward, to runtimes):** only a yes/no permission gate; **no free-form mid-turn
  question, no scope-expansion request.** An agent asking just ends its turn → restart, not
  resume. → Trigger must detect a structured `needs_input` turn result and resume the session.
- **MCP (upward, to orchestrators):** **elicitation** is the native fit for asking a question
  or requesting scope. Spec note: elicitation MUST NOT request sensitive data; clients may
  decline/cancel, so "declined" is a first-class outcome.

## Design takeaways (applied in ADR 0013)

1. Model "asking" as a first-class parking sub-state, not a new job (discriminator +
   `request_json`/`answer_json`).
2. Persist the resume point; resume in place (session id + step cursor); keep the pre-question
   step boundary idempotent.
3. Answer injection reuses the `blocked` release path (CLI + MCP).
4. Bound every wait with a durable timeout and a default action.
5. Guard the answer channel: validate against the declared schema; treat as data, not
   instructions; cap questions per run.
6. Separate "ask a question" from "request scope" — scope escalation has little native prior
   art; model it as a typed request whose grant mutates the run's permission record.

## Sources

- MCP elicitation: https://modelcontextprotocol.io/specification/2025-06-18/client/elicitation ·
  https://workos.com/blog/mcp-elicitation
- LangGraph interrupt: https://reference.langchain.com/python/langgraph/types/interrupt
- OpenAI Agents SDK HITL: https://openai.github.io/openai-agents-js/guides/human-in-the-loop/
- Claude Agent SDK user input: https://code.claude.com/docs/en/agent-sdk/user-input
- ACP schema: https://agentclientprotocol.com/protocol/schema
- Temporal HITL: https://docs.temporal.io/ai-cookbook/human-in-the-loop-python
- AutoGen human input: https://microsoft.github.io/autogen/0.2/docs/tutorial/human-in-the-loop/
