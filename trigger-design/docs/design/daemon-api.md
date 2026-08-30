---
title: "Daemon and API surface (design)"
status: draft
date: 2026-08-29
tags: [design, daemon, api, mcp, sse, interface, authority]
related: [[0004-daemon-interface-split]], [[0007-agent-controllable-mcp-server]], [[0014-answer-authority]], [[0015-principal-by-transport]], [[0011-internal-pubsub-two-buses]]
---

# Daemon and API surface (design)

Where the supervisor, the interfaces, MCP elicitation, authority checks, and the event stream
meet. Elaborates [[0004-daemon-interface-split]] (daemon is the only authority; interfaces read
the API) and [[0007-agent-controllable-mcp-server]] (MCP is one such interface).

## One Service, several thin faces

The daemon exposes a single internal `Service` — its operations. Every face is a thin adapter
over it; none reimplements logic or touches SQLite directly.

```
   CLI / TUI / GUI ──HTTP (unix socket)──┐
   Orchestrator    ──MCP────────────────┤──► Service ──► supervisor + WorkQueue/EventLog
   External CI/hook ──HTTP webhook route─┘         (single authority, single writer)
```

## Transport and principal

**The transport implies the principal, and the principal drives authority** — the binding rule
is fixed in [[0015-principal-by-transport]]; the reversible details below (socket path, whether
TCP ships in v1) live here.

- **Local Unix domain socket** (default) — CLI, TUI, GUI. The caller is the **human /
  operator**: full authority. No network exposure; filesystem permissions guard it.
- **MCP** — the **orchestrator**: charter-bounded authority. Its identity is the MCP client
  identity; beyond-charter requests escalate to the human.
- **Optional localhost TCP + token** — only if a GUI or remote view needs it; off by default,
  gated by a token in the Keychain ([[0006-secrets-and-agent-permissions]]).
- **Webhook route** — inbound only; it produces a `Fire`, never answers or reads state. Auth
  per R2/R6.

This means the authority check for answering a parked run is partly structural: a socket caller
is the human; an MCP caller is the orchestrator whose grant is checked against the charter.

## Endpoint groups (HTTP)

| Group | Endpoints | Notes |
| --- | --- | --- |
| Daemon | `GET /healthz`, `GET /version`, `POST /reload` | reload re-reads config, reconciles sources ([[trigger-abstraction]]) |
| Jobs | `GET /jobs`, `GET /jobs/{name}` | configured jobs (read-only view of config) |
| Runs | `GET /runs?status=&job=`, `GET /runs/{id}`, `POST /runs`, `POST /runs/{id}/cancel` | `POST /runs` fires a manual job (a `manual` trigger); `GET /runs/{id}` includes steps + recent events |
| Parking | `GET /parked`, `GET /parked/{id}`, `POST /parked/{id}/answer`, `POST /parked/{id}/release` | `answer` resumes `awaiting_input`; `release` requeues `blocked`; both authority-checked |
| Events | `GET /events?since={cursor}` | SSE stream + cursor replay (below) |
| Ingress | `POST /hooks/{path}` | webhook trigger; emits a `Fire`, returns 202 |

Secrets are never returned by any endpoint ([[0006-secrets-and-agent-permissions]]); a job view
may list *which* secret names it needs, never values.

## MCP surface (façade over the same Service)

Tools mirror the Service: `fire_job`, `list_runs`, `get_run`, `list_parked`, `answer_parked`,
`subscribe_events`. Plus the **elicitation** path for `awaiting_input`
([[0013-ask-and-resume-instead-of-bail]]):

- When the orchestrator itself fired the run and it parks *during* that call, surface it as an
  MCP `elicitation/create` in-band.
- For runs that park **asynchronously**, the orchestrator discovers them via `list_parked` /
  `subscribe_events` and responds with `answer_parked`. (Whether to also push server-initiated
  elicitation for async parks is an open detail — depends on the orchestrator holding an open
  session. See open questions.)

## Event streaming: SSE + durable cursor

The event bus ([[0011-internal-pubsub-two-buses]]) is exposed as **Server-Sent Events** —
simple, one-way, fits best-effort fan-out. Each event carries the durable log id as its cursor
(harvest agent-minder's autoincrement event id). A client reconnects with `?since={cursor}` to
replay missed events from the durable log, then continues live. This gives the TUI live updates
and crash-safe catch-up with one mechanism.

## Service interface (sketch)

```go
type Principal struct {
    Kind      string // human | orchestrator
    ID        string // operator, or MCP client identity
    Authority string // full | charter-bounded (mode from ADR 0014)
}

type Service interface {
    Status(ctx context.Context) (Status, error)
    Reload(ctx context.Context) error

    ListJobs(ctx context.Context) ([]Job, error)
    GetJob(ctx context.Context, name string) (Job, error)

    ListRuns(ctx context.Context, f RunFilter) ([]Run, error)
    GetRun(ctx context.Context, id string) (Run, error)
    FireJob(ctx context.Context, p Principal, req FireRequest) (Run, error)
    CancelRun(ctx context.Context, p Principal, id string) error

    ListParked(ctx context.Context, f ParkFilter) ([]Run, error)
    AnswerParked(ctx context.Context, p Principal, id string, ans Answer) error // authority-checked
    ReleaseBlocked(ctx context.Context, p Principal, id string, note string) error

    SubscribeEvents(ctx context.Context, since int64) (<-chan Event, error)
}
```

The HTTP handlers and the MCP tools both call this one interface; authority is enforced inside
`AnswerParked`/`FireJob`/`CancelRun` using the `Principal`, so no face can bypass it.

## Harvest

agent-minder's `internal/daemon` (HTTP API + client, PID file, heartbeat) is the pattern to
lift, reshaped around the `Service` interface and the Unix-socket-first transport. Its event id
cursor and store-first event log carry the SSE replay.

## Open questions

- Unix socket path and permissions; whether TCP+token ships in v1 or waits for a GUI.
- Async elicitation: push server-initiated elicitation vs. orchestrator long-poll/subscribe.
- MCP client identity/authorization model (folds into R6).
- Whether `GET /events` needs topic filters (by job/run) in v1 or just a firehose + client-side
  filter.
