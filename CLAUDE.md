# agent-minder

Go CLI coordination layer on top of [agent-msg](../agent-msg). Monitors git repos, watches the message bus, and uses a two-tier LLM pipeline to analyze changes and coordinate agents.

## Quick orientation

- **Module**: `github.com/dustinlange/agent-minder`
- **Go version**: 1.25+ (bubbletea v2 requirement)
- **State**: SQLite at `~/.agent-minder/minder.db` (WAL mode, foreign keys)
- **agent-msg DB**: `~/repos/agent-msg/messages.db` (or `AGENT_MSG_DB` env var)
- **LLM**: `ANTHROPIC_API_KEY` env var required

## Architecture

### Two-tier LLM pipeline (internal/poller)

Each poll cycle with new activity runs two sequential LLM calls:

1. **Tier 1 — Haiku** (`project.LLMSummarizerModel`): Summarizes raw git + bus data. 512 max tokens. Plain text output.
2. **Tier 2 — Sonnet** (`project.LLMAnalyzerModel`): Analyzes tier 1 summary + active concerns + project context. 1024 max tokens. Returns structured JSON:

```json
{
  "analysis": "status update text",
  "concerns": [{"severity": "warning", "message": "..."}],
  "bus_message": {"topic": "project/coord", "message": "..."}
}
```

- `parseAnalysis()` in `analysis.go` handles raw JSON, markdown-fenced JSON, and plain text fallback
- Concerns are deduplicated before insert via `isDuplicateConcern()` (50%+ keyword overlap with active concerns)
- Bus messages are published via `msgbus.Publisher` only when the analyzer includes `bus_message`

### Bus integration (internal/msgbus)

- `Client` — read-only connection (`?mode=ro`) for polling messages
- `Publisher` — read-write connection for publishing messages back to agent-msg
- Both work against the same `messages.db` file; agent-msg bash scripts remain compatible

### TUI (internal/tui)

Bubbletea v2 dashboard. Key bindings: `p` pause, `r` poll now, `e` expand, `m` broadcast, `t` theme, `q` quit.

- Spinner (bubbles/v2/spinner MiniDot) shown during manual poll (`r`) and broadcast send
- Concerns capped at 5 displayed with "+N more" indicator
- Event log dynamically sized to remaining terminal height
- Broadcast mode: `m` opens textinput, Enter sends through tier 2 LLM → publishes to bus

### DB schema (internal/db) — currently v2

**projects**: name, goal_type, goal_description, refresh_interval_sec, message_ttl_sec, auto_enroll_worktrees, minder_identity, llm_provider, llm_model, llm_summarizer_model, llm_analyzer_model

**polls**: project_id, new_commits, new_messages, concerns_raised, llm_response (legacy), tier1_response, tier2_response, bus_message_sent, polled_at

**Also**: repos, worktrees, topics, concerns (see `schema.go` for full DDL)

Migration v1→v2 adds `llm_summarizer_model`/`llm_analyzer_model` to projects and `tier1_response`/`tier2_response`/`bus_message_sent` to polls, copies existing data into new columns.

`Poll.LLMResponse()` accessor returns tier2 > tier1 > raw (backward compat).

## Package map

| Package | Purpose | Notes |
|---------|---------|-------|
| `cmd/` | Cobra commands | init, start, status, enroll, pause, resume |
| `internal/db` | SQLite schema + CRUD | `Store` wraps sqlx.DB, migrations in `schema.go` |
| `internal/llm` | LLM provider interface | Anthropic + OpenAI adapters, `Provider.Complete()` |
| `internal/poller` | Poll loop + LLM pipeline | `poller.go`, `analysis.go` (parsing + dedup) |
| `internal/tui` | Bubbletea dashboard | `app.go` (model/update/view), `styles.go` (themes) |
| `internal/git` | Git CLI wrappers | `LogSince()`, `Branches()`, `WorktreeList()` |
| `internal/discovery` | Repo scanning | `ScanRepo()`, `DeriveProjectName()`, `SuggestTopics()` |
| `internal/msgbus` | Agent-msg client + publisher | Read-only `Client`, read-write `Publisher` |

### Legacy packages (still present, unused by v2)

`internal/config` (YAML), `internal/state` (markdown parser), `internal/claude` (CLI wrapper), `internal/prompt` (Go templates)

## Commands

- `init <repo-dir> [...]` — Interactive wizard → SQLite. Sets goal, topics, poll interval, LLM models.
- `start <project>` / `resume <project>` — TUI + poller. Creates publisher for bus writes.
- `status <project>` — CLI text summary, no LLM call.
- `enroll <project> <repo-dir>` — Add repo to project.

## Testing

```bash
go test ./...                           # All unit tests
go test ./internal/db/... -v            # DB + migration tests (9 tests)
go test ./internal/poller/... -v        # Analysis parsing + concern dedup (9 tests)
go test ./internal/msgbus/... -v        # Client + publisher tests (9 tests)

# Integration test (requires ANTHROPIC_API_KEY + existing agent-test project):
go test -tags integration -run TestIntegrationTwoTierPipeline -v ./internal/poller/ -timeout 90s
```

## Key patterns

- `Poller.doPoll()` is the main loop body — gathers git + bus data, runs both LLM tiers, publishes, records
- `Poller.Broadcast()` is the user-initiated broadcast path — gathers context, calls tier 2, publishes
- All TUI async operations use bubbletea Cmd pattern (return `func() tea.Msg`), not raw goroutines
- Spinner ticks flow through the standard bubbletea Update loop via `spinner.TickMsg`
- Theme is global mutable state (package-level `themeIndex`), cycled via `cycleTheme()`

## Anthropic SDK notes

- System prompt uses `TextBlockParam{Text: "..."}` (NOT `NewTextBlock()` which returns `ContentBlockParamUnion`)
- SDK reads `ANTHROPIC_API_KEY` from env by default
- Model IDs: `claude-haiku-4-5`, `claude-sonnet-4-6`, `claude-opus-4-6`
