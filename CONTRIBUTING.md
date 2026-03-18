# Contributing to agent-minder

Thanks for your interest in contributing to agent-minder! This document covers everything you need to get started.

## Development setup

### Prerequisites

- **Go 1.25+** (required for bubbletea v2)
- **golangci-lint** — install via `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest` or [other methods](https://golangci-lint.run/welcome/install/)
- **lefthook** — git hook manager; install via `go install github.com/evilmartians/lefthook@latest` or [other methods](https://github.com/evilmartians/lefthook/blob/master/docs/install.md)
- **An `ANTHROPIC_API_KEY`** environment variable (needed at runtime and for integration tests)

### Clone and build

```bash
git clone https://github.com/aptx-health/agent-minder.git
cd agent-minder
go mod download
go build ./...
```

### Install git hooks

```bash
lefthook install
```

This registers pre-commit hooks that run automatically on every commit:

- `gofmt` — ensures all `.go` files are formatted
- `go build ./...` — ensures the project compiles
- `golangci-lint run ./...` — runs the configured linters

All three must pass before a commit is accepted.

## Running tests

```bash
# All unit tests
go test ./...

# Package-specific tests (verbose)
go test ./internal/db/... -v
go test ./internal/poller/... -v
go test ./internal/msgbus/... -v

# Integration test (requires ANTHROPIC_API_KEY + an existing agent-test project)
go test -tags integration -run TestIntegrationTwoTierPipeline -v ./internal/poller/ -timeout 90s
```

## Running a local dev instance

Use the provided test environment script to run an isolated instance with its own database and log file:

```bash
source scripts/test-env.sh <project-name>
go run . start "$MINDER_PROJECT"
```

This auto-derives paths from the current branch name, copies the production DB on first run, and enables debug logging. See `scripts/test-env.sh` for details.

**Key environment variables:**

| Variable | Description |
|----------|-------------|
| `MINDER_DB` | Override database path (default: `~/.agent-minder/minder.db`) |
| `MINDER_LOG` | Override debug log path (default: `~/.agent-minder/debug.log`) |
| `MINDER_DEBUG=1` | Enable structured JSON debug logging |
| `ANTHROPIC_API_KEY` | Required for LLM calls |

## Code style and linting

- All Go code must be formatted with `gofmt` (enforced by pre-commit hook).
- Linting is handled by `golangci-lint` with the project's default configuration.
- Follow existing patterns in the codebase. When in doubt, look at neighboring code for conventions.
- All TUI changes must follow the conventions in [`TUI-UX-GUIDE.md`](TUI-UX-GUIDE.md).
- Async TUI operations use the bubbletea `Cmd` pattern — never raw goroutines.

## Architecture overview

The codebase is organized into focused internal packages. See the **Package map** section in [`CLAUDE.md`](CLAUDE.md) for a full breakdown of every package, its purpose, and key files.

High-level:

- **`cmd/`** — Cobra CLI commands (`init`, `start`, `status`, `enroll`, etc.)
- **`internal/poller`** — Two-tier LLM pipeline (Haiku summarizer → Sonnet analyzer)
- **`internal/tui`** — Bubbletea v2 terminal dashboard
- **`internal/db`** — SQLite schema, migrations, and CRUD operations
- **`internal/autopilot`** — Supervisor for concurrent Claude Code agents
- **`internal/msgbus`** — Integration with the agent-msg message bus
- **`internal/git`** — Git CLI wrappers

## Making changes

### Branching

- Create a feature branch from `main`.
- Use descriptive branch names (e.g., `fix-concern-dedup`, `add-filter-by-repo`).

### Commit messages

- Write clear, concise commit messages.
- **Always reference the issue number** with `#N` (e.g., `Fix concern dedup logic #42`). This enables cross-referencing by the sweep agent.
- Use `Fixes #N` in the commit that resolves an issue to auto-close it.

### Database migrations

If your change modifies the SQLite schema:

1. Edit `internal/db/schema.go`.
2. Increment the version number.
3. Add a migration case in the migration switch.

### Agent definitions

The repo-level `agents/autopilot.md` file and the `defaultAgentDef` constant in `internal/autopilot/prompt.go` must stay in sync. There is a drift-prevention test that enforces this.

## Pull requests

1. Ensure all tests pass (`go test ./...`) and pre-commit hooks are satisfied before opening a PR.
2. Open a PR against `main` with a clear title and description.
3. Include a summary of what changed and why.
4. Link the related issue (e.g., "Fixes #42" or "Relates to #42").
5. PRs are reviewed for correctness, style consistency, and test coverage.
6. Draft PRs are welcome for early feedback on work-in-progress.

## Reporting issues

- Search [existing issues](https://github.com/aptx-health/agent-minder/issues) before opening a new one.
- Include steps to reproduce, expected behavior, and actual behavior.
- For bugs, include your Go version (`go version`), OS, and any relevant log output.
- For feature requests, describe the use case and how it fits into the existing architecture.

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).
