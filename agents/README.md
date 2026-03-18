# Agent Definitions

This directory contains Claude Code [agent definitions](https://docs.anthropic.com/en/docs/claude-code/sub-agents) for agent-minder.

## Agents

| File | Purpose | Scope |
|------|---------|-------|
| [`autopilot.md`](autopilot.md) | Implements GitHub issues in isolated worktrees | Per-issue, runs many times |
| [`onboarding.md`](onboarding.md) | Interactive repo onboarding — gathers context and generates config | Per-repo, runs once |

---

## Autopilot (`autopilot.md`)

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

---

## Onboarding (`onboarding.md`)

The onboarding agent performs the interactive portion of `agent-minder repo enroll`. After the CLI runs a mechanical inventory scan, it launches this agent to:

1. Ask the user targeted questions about things that can't be mechanically detected (secrets management, test commands, special tooling)
2. Generate three artifacts:
   - `.agent-minder/onboarding.yaml` — structured onboarding file with user-provided context
   - `.claude/settings.json` — Claude Code permissions derived from detected tooling
   - `.claude/agents/autopilot.md` — project-specific autopilot agent definition (if none exists)
3. Review artifacts with the user before writing to disk

### Installing

Install globally (recommended — onboarding is repo-independent):

```bash
mkdir -p ~/.claude/agents
cp agents/onboarding.md ~/.claude/agents/onboarding.md
```

### How it works

The CLI (`agent-minder repo enroll`) orchestrates the process:

1. CLI scans the repo mechanically → writes initial `.agent-minder/onboarding.yaml` with inventory
2. CLI launches: `claude --agent onboarding -p "<repo path + inventory summary>"`
3. Onboarding agent asks user ≤5 targeted questions
4. Agent generates and writes configuration files after user approval

Unlike autopilot, onboarding runs once per repo and doesn't need the repo→user→built-in failover chain. It lives at `~/.claude/agents/onboarding.md` only.
