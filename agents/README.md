# Autopilot Agent Definition

This directory contains a Claude Code [agent definition](https://docs.anthropic.com/en/docs/claude-code/sub-agents) for the autopilot system.

## What it does

When autopilot launches a Claude Code agent to work on a GitHub issue, it checks whether the target repository has `.claude/agents/autopilot.md`. If found, the agent definition provides the behavioral instructions (workflow, constraints, bail conditions) and the supervisor passes only the dynamic task context (issue number, worktree path, commands) as the prompt.

If no agent definition is found, autopilot falls back to its built-in full prompt — everything works exactly as before. **The agent definition is additive, never required.** It's a thin layer to help make agent behaviors more predictable and customizable throughout the lifecycle.

## Installing

Copy `autopilot.md` into a target repository:

```bash
mkdir -p <your-repo>/.claude/agents
cp agents/autopilot.md <your-repo>/.claude/agents/autopilot.md
```

Or install it globally for all repositories:

```bash
mkdir -p ~/.claude/agents
cp agents/autopilot.md ~/.claude/agents/autopilot.md
```

Then commit as appropriate. The next time autopilot creates a worktree from that repo, it will detect the definition and use it.

## Customizing

The agent definition is plain markdown with YAML frontmatter. You can customize it per-repo to:

- Adjust the complexity threshold (default: 8 files)
- Add project-specific constraints or conventions
- Change the bail criteria
- Add steps to the workflow (e.g., "run `make lint` before committing")

The `tools` field in the frontmatter controls which Claude Code tools the agent can use. The default set (`Bash, Read, Edit, Write, Glob, Grep`) covers typical development work.

## How it works

Without agent definition (default):
```
claude -p --max-turns 50 --max-budget-usd 3.00 --dangerously-skip-permissions "<full prompt with behavior + context>"
```

With agent definition:
```
claude --agent autopilot -p --max-turns 50 --max-budget-usd 3.00 --dangerously-skip-permissions "<task context only>"
```

The agent definition becomes the system prompt; the task context becomes the user prompt.
