# Agent Definitions

Most agent definitions do **not** live here. This directory holds only the one agent that is not
part of the built-in registry.

## Where agent definitions come from

Agent contracts are resolved at run time by `resolveAgentDefByName`
(`internal/supervisor/prompt.go`) through a three-tier failover:

1. **Repo-level** — `<repo>/.claude/agents/<name>.md`
2. **User-level** — `~/.claude/agents/<name>.md`
3. **Built-in** — the template registry in `internal/supervisor/templates.go`, installed into the
   repo on first use

So the built-in definitions are in Go source, not in this directory. To read one:

```bash
minder agents list
minder agents show autopilot
```

`minder enroll` installs the required agents (`autopilot`, `reviewer`) into `.claude/agents/` and
offers the optional ones (`bug-fixer`, `dependency-updater`, `security-scanner`, `doc-updater`,
`spike`). Editing the installed file is how you customize an agent for a project — the repo-level
copy wins over the built-in one.

### Runtimes

One resolved definition serves all three doer runtimes. `SlotContext.EnsureAgentDef` hands the
resolved body to the active runtime's `PrepareAgentDef`, which writes it where that runtime looks
for it:

| Runtime | Materialized as |
|---------|-----------------|
| `claude-code` | `.claude/agents/<name>.md` |
| `codex` | `AGENTS.md` |
| `opencode` | `.opencode/agent/<name>.md` |

Nothing needs to be duplicated per runtime.

## `designer.md`

The designer agent runs a design interview on an issue before implementation starts and posts a
structured design plan as an issue comment.

It lives here rather than in the registry because it is **interactive** — it expects a human in the
conversation — while the supervisor runs agents headless. It is not installed by `minder enroll`
and is not dispatched by the supervisor. Install it by hand:

```bash
mkdir -p <your-repo>/.claude/agents
cp agents/designer.md <your-repo>/.claude/agents/designer.md
```

Or at `~/.claude/agents/designer.md` for every repo. Then invoke it directly through Claude Code.

## Frontmatter

The YAML frontmatter is the machine-readable contract, parsed by `internal/supervisor/contract.go`:
`mode`, `output`, `stages`, `context`, and `dedup` drive orchestration, so changing them changes
behavior. The instruction body below the closing `---` is yours to customize freely.

Contracts carry **behavior** — how the agent decides, when it stops, what it produces. Project
**facts** belong in `CLAUDE.md`, and the shared working agreement (scope, human-facing output
format, delegation) is injected at prompt-assembly time by `renderHouseStyle`
(`internal/supervisor/context.go`). Restating either in a contract only creates drift.
