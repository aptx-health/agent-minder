---
title: "ADR 0007 — Agent-controllable: machine-first API and MCP server"
status: accepted
date: 2026-08-29
tags: [architecture, mcp, agents, api, interoperability]
superseded_by:
---

## Context

Trigger should be usable by an **orchestrating agent**, not only by a human at a TUI. In
August 2026 "agent-friendly" has a concrete meaning: agents discover and call tools over
**MCP (Model Context Protocol)**, which the ecosystem has adopted as the default
tool-discovery interface (Claude Code, opencode, Cline, and others speak it natively).
Being agent-controllable is therefore not a vague nicety — it is "expose an MCP server with
a stable, machine-first contract."

This clarifies Trigger's place between two protocols:

- **Downward** Trigger drives coding-agent runtimes — it is an **ACP client**
  ([[0003-acp-runtime-seam]]).
- **Upward** Trigger is driven by orchestrating agents — it is an **MCP server** (this ADR).

## Decision

**Trigger exposes an MCP server so an orchestrating agent can control it**, and its whole
control surface is **machine-first**: stable, structured (JSON), and idempotent.

- The MCP server is another **interface over the daemon's local API**, never a second
  authority ([[0004-daemon-interface-split]]). It surfaces the same operations a human
  interface has: create/list jobs, fire a one-off, read status and results, stream events.
- An orchestrating agent firing a job is the **manual/external trigger path**
  ([[0005-trigger-source-agnostic]]) reached over MCP — a new caller, not a new transport.
- Every operation is **idempotent and safe to retry** (agents retry), consistent with the
  local-state-authoritative invariant ([[0002-local-sqlite-source-of-truth]]).
- The CLI mirrors the same contract with `--json` output, so the tool is scriptable and
  agent-consumable without the MCP server.

The exact MCP surface (tool set, verbs, event streaming, local auth) is a design task —
see [[open-questions]] R6 — but the decision to be an MCP server is accepted now.

## Consequences

- An orchestrating agent (Claude, opencode, a custom controller) can schedule and fire
  Trigger jobs as tool calls — Trigger becomes a building block in a larger agent loop, not
  a dead end.
- Machine-first is a real constraint on the API: stable schemas, versioned, no scraping a
  TUI for state. The human interfaces benefit too — they read the same clean contract.
- Two protocols coexist without conflict: MCP on top (driven), ACP below (driving), the
  daemon and local API in the middle as the single authority.
- Local-auth for the MCP surface ties into [[0006-secrets-and-agent-permissions]] — an
  agent controlling Trigger must itself be scoped; fold this into R5/R6.
