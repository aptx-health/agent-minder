# Security Policy

## Reporting a Vulnerability

Please report security vulnerabilities through [GitHub Security Advisories](https://github.com/aptx-health/agent-minder/security/advisories/new). This keeps the report private until a fix is available.

Do **not** open a public issue for security vulnerabilities.

## Credential Handling

agent-minder handles API keys and tokens (Anthropic, OpenAI, GitHub). Credentials are resolved in this order:

1. **OS keychain** (macOS Keychain, Linux Secret Service, Windows Credential Manager)
2. **Environment variables** (`ANTHROPIC_API_KEY`, `GITHUB_TOKEN`, etc.)
3. **Config file** (`~/.agent-minder/config.yaml`, stored with `0600` permissions)

The `setup` command stores credentials in the OS keychain. Storing credentials in the config file is supported but discouraged — a warning is emitted at startup.

## Sensitive Data in Logs

All log files are created with `0600` permissions (owner-only access).

- **Debug logs** (`MINDER_DEBUG=1`): Contain full LLM prompts and responses, which may include git diffs and commit content. Only enable for troubleshooting.
- **Agent logs** (`~/.agent-minder/agents/`): Contain full Claude Code session output, including tool calls and results. May contain file contents the agent reads during execution.
- **Bus messages** (`messages.db`): Stored as plaintext in a local SQLite database.

No credential masking is applied to log output. Avoid committing secrets to repos monitored by agent-minder.

## Spawned Processes

Autopilot agents (Claude Code subprocesses) inherit the parent process environment plus `GITHUB_TOKEN`. Be mindful of sensitive environment variables in your shell.
