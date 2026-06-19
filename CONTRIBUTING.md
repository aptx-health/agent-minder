# Contributing to agent-minder

Thanks for your interest in contributing to agent-minder! This document covers everything you need to get started.

## Development setup

### Prerequisites

- **Go 1.25+** (required for bubbletea v2)
- **golangci-lint** — install via `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest` or [other methods](https://golangci-lint.run/welcome/install/)
- **lefthook** — git hook manager; install via `go install github.com/evilmartians/lefthook@latest` or [other methods](https://github.com/evilmartians/lefthook/blob/master/docs/install.md)
- **[Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code)** — agent execution (no Anthropic API key needed; the CLI handles auth)
- **`GITHUB_TOKEN`** — GitHub API token (env var or `minder auth login`)

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
go test ./internal/supervisor/... -v
go test ./internal/scheduler/... -v
go test ./internal/daemon/... -v

# Supervisor scenario integration suite (end-to-end pipeline scenarios)
make integration
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
| `MINDER_DB` | Override database path (default: `~/.agent-minder/v2.db`) |
| `MINDER_LOG` | Override debug log path (default: `~/.agent-minder/debug.log`) |
| `MINDER_DEBUG=1` | Enable structured JSON debug logging |
| `GITHUB_TOKEN` | GitHub API token (required for agent execution) |
| `MINDER_API_KEY` | API key for remote daemon access |

## Code style and linting

- All Go code must be formatted with `gofmt` (enforced by pre-commit hook).
- Linting is handled by `golangci-lint` with the project's default configuration.
- Follow existing patterns in the codebase. When in doubt, look at neighboring code for conventions.
- All TUI changes must follow the conventions in [`TUI-UX-GUIDE.md`](TUI-UX-GUIDE.md).
- Async TUI operations use the bubbletea `Cmd` pattern — never raw goroutines.

## Architecture overview

The codebase is organized into focused internal packages. See the **Package map** section in [`CLAUDE.md`](CLAUDE.md) for a full breakdown of every package, its purpose, and key files.

High-level:

- **`cmd/`** — Cobra CLI commands (`deploy`, `status`, `stop`, `enroll`, `lesson`, `jobs`, `agents`, `auth`, `tui`, etc.)
- **`internal/supervisor`** — Job manager for concurrent Claude Code agents (contracts, context providers, dedup, review, dep graph, stage executor)
- **`internal/runtime`** — `AgentRuntime` contract + registry; `claudecode/` is the default doer implementation
- **`internal/daemon`** — PID/heartbeat lifecycle and HTTP API server + client
- **`internal/scheduler`** — Cron parser, `jobs.yaml`, scheduled job firing
- **`internal/db`** — SQLite schema, migrations, and CRUD operations
- **`internal/claudecli`** — Claude Code CLI wrapper (`claude -p` / `claude --agent`)
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

Built-in agent definitions live under `agents/` at the repo root and are also embedded into the binary via `internal/supervisor/templates.go` (`AgentTemplates()`). Both copies must stay in sync — drift-prevention tests in `internal/supervisor` enforce this.

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
