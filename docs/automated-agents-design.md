# Automated Agents Design

Extends agent-minder to launch Claude Code agents that work on GitHub issues in isolated git worktrees.

## Overview

From the TUI, the user triggers "autopilot" (`a` key) which:

1. Converts open tracked items (excluding `no-agent` label) into autopilot tasks
2. Fetches live GitHub status for each item (not cached labels)
3. Extracts a dependency graph (one LLM call, includes all tracked items as context)
4. Launches up to N agents on unblocked issues, each in its own worktree
5. Agents self-triage: do the work or bail with questions
6. On completion/bail, the worktree is cleaned up and the slot is refilled
7. Dynamic discovery: every 60s, checks for new tracked items to add to the task pool

## Prerequisites

Autopilot delegates work to Claude Code agents running in isolated worktrees. Minder does not handle quality checks, linting, testing, or merge conflicts — it generates a dependency graph and assigns work. The target repo must already have proper tooling in place.

### Repository requirements

- **Pre-commit hooks / linters**: Agents respect existing git hooks (husky, pre-commit, etc.). If the repo has no hooks, agents won't run linters automatically.
- **Test suites**: Agents will run tests if the repo has them and the issue context suggests it. No test suite = no test validation.
- **CI/CD**: Draft PRs opened by agents should trigger the same CI pipeline as human PRs. This is your safety net.
- **Branch protection**: Recommended on the default branch. Agents push to `agent/issue-<N>` branches and open draft PRs — they never push to main directly.
- **CLAUDE.md**: A `CLAUDE.md` in the target repo helps agents understand project conventions, build commands, test commands, and architecture. Without it, agents rely solely on issue context and codebase exploration.

### Minder requirements

- **Tracked items**: Issues must be tracked in the TUI (via GitHub integration) before autopilot can pick them up.
- **GitHub token**: `GITHUB_TOKEN` env var or configured integration token — agents use `gh` CLI for labels, comments, and PRs.
- **Claude CLI**: `claude` must be on `$PATH` and working.

### What minder does NOT do

- Run linters, formatters, or test suites itself
- Resolve merge conflicts between concurrent agent branches
- Validate code quality beyond what the repo's own hooks enforce
- Retry failed agents automatically (bailed tasks stay bailed)
- Manage CI/CD pipelines or deployment

## Core Principles

- **Minder is the coordinator, agents are workers.** Agents don't know about agent-msg, the bus, or each other. They just do claude code things: read issues, explore code, commit, push, open PRs.
- **GitHub is the source of truth.** Issue status, comments, PRs, and labels are the primary state. Minder observes these through its existing poll/sweep cycle.
- **Worktrees are ephemeral.** Created for a task, deleted when the agent exits. No long-lived worktrees.
- **Agents self-triage.** No separate triage phase. The agent explores the codebase and decides whether to proceed or bail.
- **Always fresh start.** `Prepare()` clears old tasks and rebuilds from scratch — no stale state resume.

## User Flow

```
User in TUI → presses 'a'
  → Minder shows: "12 issues, 8 unblocked. Launch 3 agents? [y/n]"
  → User confirms with 'y'
  → Agents start working
  → TUI shows slot status: "Slot 1: #42 Title (3m)", "Slot 2: #38 Title (1m)", "Slot 3: idle"
  → As agents finish, slots refill from unblocked queue
  → Every 60s, new tracked items are discovered and added to the task pool
  → Every 30s, review tasks are checked for PR merge → promoted to done
  → 'A' to stop (with y/n confirmation)
  → Existing poll cycle picks up new commits, PRs, comments naturally
```

## Task Lifecycle

```
queued → running → review (PR opened) → done (PR merged)
                 → bailed (no PR, agent gave up)
```

### Startup
1. Minder creates worktree: `git worktree add ~/.agent-minder/worktrees/<project>/issue-<N> -b agent/issue-<N>`
2. Stale branch from prior run is deleted first if it exists
3. Minder launches claude code in print mode in that directory
4. Agent's first actions:
   - Add `in-progress` label: `gh issue edit <N> --add-label "in-progress"`
   - Post comment: "Agent starting work on this issue"
   - Read the full issue and explore the codebase

### Decision Point

**Fork A — "I can do this"**
- Implement the changes
- Commits pass repo's own quality gates (husky, pre-commit hooks, tests)
- Push branch, open draft PR referencing the issue (`Fixes #N`)
- Exit

**Fork B — "I can't/shouldn't do this"**
- Post GH issue comment with:
  - What was explored and learned
  - Specific questions or blockers
  - A ready-to-paste prompt for a manual claude code follow-up session
- Add `blocked` label, remove `in-progress` label
- Exit without code changes

### Cleanup (Minder handles this, not the agent)
1. Agent process exits
2. Minder inspects outcome:
   - PR detected → status `review`, swap `in-progress` label to `needs-review`
   - No PR → status `bailed`, remove worktree and branch
3. PR merge detected (30s review check) → status `done`, remove `needs-review` label, clean worktree
4. Check dependency graph → fill slot with next unblocked issue

### Label Management

| Event | Label added | Label removed |
|-------|-------------|---------------|
| Agent starts | `in-progress` | — |
| PR opened (review) | `needs-review` | `in-progress` |
| PR merged (done) | — | `needs-review` |
| Agent bails | `blocked` | `in-progress` |

The `no-agent` label (configurable via `autopilot_skip_label`) excludes issues from the task pool entirely.

## Dependency Graph

### Extraction
One LLM call when autopilot is triggered. Input: all autopilot task issue titles + bodies, plus all other tracked items (closed, skipped, etc.) as context for cross-references. Output:

```json
{
  "42": [],
  "38": [42],
  "55": [42, 38],
  "61": []
}
```

The graph is computed once at `Prepare()` time. Fallback if LLM fails: sequential ordering by issue number.

### Unblock Detection
- **Internal deps** (other autopilot tasks): blocked until dep task reaches `done` status
- **External deps** (tracked items not in autopilot): blocked while tracked item is `open`; unblocked when closed/merged
- Dynamically discovered tasks get empty dependencies (start unblocked)

## Agent Invocation

```bash
claude --agent autopilot -p \
  --max-turns 50 \
  --max-budget-usd 3.00 \
  --allowedTools Read --allowedTools Edit --allowedTools Write \
  --allowedTools Glob --allowedTools Grep \
  --allowedTools "Bash(git *)" --allowedTools "Bash(gh *)" \
  "<task context prompt>"
```

Allowed tools are loaded from `.agent-minder/onboarding.yaml` if available, otherwise the default set shown above is used. Environment: `GITHUB_TOKEN` is set for `gh` CLI access.

### Prompt Template

```
You are working on GitHub issue #<NUMBER>: <TITLE>

<ISSUE BODY>

You are in a git worktree at: <WORKTREE_PATH>
Branch: agent/issue-<NUMBER> (already checked out)
Base branch: <BASE_BRANCH>
Repository: <OWNER>/<REPO>

## Your first steps

1. Move the issue to "In Progress" — run: gh issue edit <NUMBER> --add-label "in-progress" -R <OWNER>/<REPO>
2. Post a comment: gh issue comment <NUMBER> --body "Agent starting work on this issue" -R <OWNER>/<REPO>
3. Read the full issue and any linked issues for context
4. Explore the codebase to understand the relevant code

## Your decision

After exploring, decide:

### If you can confidently complete this work:
- Implement the changes
- Ensure all tests pass and pre-commit hooks are satisfied
- If tests or hooks fail, you may retry up to 3 times
- Commit with "Fixes #<NUMBER>" in the commit message
- Push the branch
- Open a draft PR

### If you cannot proceed (too risky, blocked, unclear, or failing after retries):
- Do NOT make code changes
- Post a comment on #<NUMBER> with:
  - What you explored and learned about the codebase
  - Your specific questions or what's blocking you
  - A follow-up prompt that could be pasted into a future claude code session
- Add the "blocked" label: gh issue edit <NUMBER> --add-label "blocked" -R <OWNER>/<REPO>
- Remove "in-progress" label: gh issue edit <NUMBER> --remove-label "in-progress" -R <OWNER>/<REPO>

## Important constraints

- Only modify files within this worktree directory
- Do not keep retrying if you are stuck — bail early with good context
- Do not over-engineer. Implement exactly what the issue asks for.
- Quality gates: this repo may have pre-commit hooks, linters, or test suites. Respect them.
```

## Resource Limits

| Limit | Default | Configurable | Purpose |
|-------|---------|--------------|---------|
| `--max-turns` | 50 | `autopilot_max_turns` | Prevents agents from spinning forever |
| `--max-budget-usd` | 3.00 | `autopilot_max_budget_usd` | Cost safety net per issue |
| Max concurrent agents | 3 | `autopilot_max_agents` | Worktree/resource cap |
| Skip label | `no-agent` | `autopilot_skip_label` | Exclude issues from task pool |
| Retry budget (in prompt) | 3 attempts | — | For test/hook failures |

## Agent Communication

Agents do NOT use agent-msg. Their artifacts are observed by minder through existing mechanisms:

| Agent action | How minder sees it |
|---|---|
| Commits + push | Git polling (existing) |
| Draft PR opened | `inspectOutcome()` checks via `gh pr list --head <branch>` |
| Issue comment posted | Visible on GH issue |
| Issue label changed | Tracked items sweep (existing) |
| Process exit | Supervisor goroutine `cmd.Wait()` |
| PR merged | Review check ticker (30s) via `gh pr view` |

The coordinator (minder) posts to the bus on behalf of agents when relevant — the tier 2 analyzer sees autopilot status and can include it in bus messages.

## DB Schema (v9)

### `autopilot_tasks` table

```sql
CREATE TABLE autopilot_tasks (
    id INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id),
    issue_number INTEGER NOT NULL,
    issue_title TEXT NOT NULL DEFAULT '',
    issue_body TEXT NOT NULL DEFAULT '',
    dependencies TEXT DEFAULT '[]',  -- JSON array of issue numbers
    status TEXT NOT NULL DEFAULT 'queued',  -- queued, running, review, done, bailed
    worktree_path TEXT DEFAULT '',
    branch TEXT DEFAULT '',
    pr_number INTEGER DEFAULT 0,
    agent_log TEXT DEFAULT '',
    started_at DATETIME,
    completed_at DATETIME,
    UNIQUE(project_id, issue_number)
);
```

### Additions to `projects` table

```sql
ALTER TABLE projects ADD COLUMN autopilot_max_agents INTEGER DEFAULT 3;
ALTER TABLE projects ADD COLUMN autopilot_max_turns INTEGER DEFAULT 50;
ALTER TABLE projects ADD COLUMN autopilot_max_budget_usd REAL DEFAULT 3.00;
ALTER TABLE projects ADD COLUMN autopilot_skip_label TEXT DEFAULT 'no-agent';
```

Note: `autopilot_filter_type` and `autopilot_filter_value` columns exist in schema but are unused — autopilot works from tracked items directly.

## Supervisor Architecture

```
Minder process
  ├── TUI (bubbletea) ← shows slot status, handles a/A keys
  ├── Poller loop (existing) ← detects agent artifacts, injects StatusBlock()
  └── Autopilot Supervisor
       ├── Main loop (fills slots, handles events)
       ├── Review check ticker (30s) ← promotes review → done
       ├── Discovery ticker (60s) ← finds new tracked items
       ├── Slot 1: goroutine → exec.Cmd (claude process)
       ├── Slot 2: goroutine → exec.Cmd (claude process)
       └── Slot 3: (idle, waiting for unblocked work)
```

Each slot goroutine:
1. Deletes stale branch if exists
2. Creates worktree + branch
3. Renders prompt template with issue context
4. Launches `claude -p` via `exec.Command` with `GITHUB_TOKEN` env
5. Blocks on `cmd.Wait()`
6. Inspects result: checks for PR via `gh pr list --head <branch>`
7. Updates `autopilot_tasks` status (review if PR, bailed if not)
8. Manages labels (swap in-progress → needs-review, or add blocked)
9. Removes worktree (keeps branch if PR opened, deletes if bailed)
10. Emits event to TUI
11. Clears slot, triggers `fillSlots()` for newly unblocked work

Agent stdout/stderr logged to `~/.agent-minder/agents/<project>-issue-<N>.log`.

## Polling During Autopilot

The existing poller continues running during autopilot. Two adjustments:

### Increased poll frequency
When autopilot starts, poll interval is halved (minimum 30s). Original interval restored when autopilot ends.

### Analyzer context injection
`Supervisor.StatusBlock()` is injected into the tier 2 analyzer input via `poller.SetAutopilotStatusFunc()`:

```
## Autopilot Status
- Slot 1: #42 "Add pagination" (agent/issue-42, running 4m)
- Slot 2: #38 "Fix auth refresh" (agent/issue-38, running 1m)
- Slot 3: idle
Task summary: 2 queued, 1 running, 0 in review, 1 done, 1 bailed
```

## Future Considerations

- **Agent-to-agent awareness**: Agents working on related issues could benefit from knowing what other agents are doing. Could inject "FYI: agent is also working on #38 which touches the auth module" into the prompt.
- **Partial completion**: Agent did 80% but hit a wall. Currently this is "bail" — could support pushing a WIP branch with a comment explaining what's left.
- **Auto-retry after unblock**: When a blocked issue is unblocked (questions answered), automatically re-queue it without user intervention.
- **Cost reporting**: Aggregate `--max-budget-usd` actuals across all agent runs.
- **Split poll loops** (issue #65): Separate mechanical status checks (30s) from AI analysis (5m) to reduce LLM costs during autopilot.
