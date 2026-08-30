---
title: "Harvest: checkout command + keyring auth"
status: accepted
date: 2026-08-29
tags: [harvest, tui, auth, secrets, transplant]
source: agent-minder cmd/checkout.go, cmd/resume.go, internal/auth/keyring.go
related: "[[0006-secrets-and-agent-permissions]], [[0013-ask-and-resume-instead-of-bail]]"
---

# Harvest: `checkout` + `internal/auth`

Human-side of the job loop: summon a job's worktree locally and jump to the work.
The harvest map verdict: **keep the UX in the TUI**, harvest the mechanics.

## checkout mechanics worth keeping

1. **Three selectors**: job ID, `#issue` (most-recent-job-wins, pick if ambiguous),
   interactive picker (excludes `queued`/`blocked`), plus substring grep filter.
2. **Action menu per job**: checkout worktree / resume / view logs / open issue / open PR.
3. **Warn before acting on a `running`/`reviewing` job** — the agent is actively
   working in that worktree.
4. **Branch candidates in priority order**: recorded `job.branch` → `agent/issue-<N>`
   → `agent/<name>`; first that exists locally or on origin wins. The fallback list
   matters because recorded paths/branches go stale after redeploys.
5. **Worktree-gone recovery**: `Fetch` + `WorktreePrune`, prefer existing local branch,
   else delete stale local branch and `worktree add` from `origin/<branch>`. Default
   location `~/.minder/worktrees/checkout/<job-name>` (fresh path, never the agent's
   worktree path).
6. **Remote daemon mode**: same flow, branch info fetched from the API — the client
   never needs the daemon's DB. Present path via clipboard (fall back to printing
   `cd <path>`).

## Resume prompt pattern (feeds [[0013-ask-and-resume]])

`buildResumePrompt` is deliberately dumb: dump everything known — metadata (agent,
status, cost), issue/PR/branch, review risk, failure reason + truncated detail (1500c),
issue body (3000c), result JSON (3000c), final agent output parsed from the log
(1500c) — then "review git log, diff, PR comments; what would you like to do?"
Session resume uses the runtime's `session_id` from the last `agent_runs` row when
available; the context-dump prompt is the fallback for sessions that can't resume.

## `internal/auth` — keyring token storage

~55 lines; one external dep (`zalando/go-keyring`). Precedence:
`GITHUB_TOKEN` env > `GH_TOKEN` env > OS keyring (service=`agent-minder`,
account=`github-token`). `SetToken`/`DeleteToken`/`HasKeyringToken` for the `minder auth`
command. This is the pattern for [[0006-secrets-and-agent-permissions]]: env wins for
CI, keyring for interactive users; tokens never live in config files or the DB.

## Transplant note

`internal/auth` lifts verbatim (rename service/account constants). checkout's
picker/menu is bubbletea-free and simple — rebuild inside the TUI using the same
selectors + branch-candidate + warn-when-running logic. Keep `candidateBranchNames`
logic in **one** place shared by checkout/resume/daemon paths (agent-minder has it
duplicated twice — don't reproduce that).
