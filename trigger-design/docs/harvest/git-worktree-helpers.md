---
title: "Harvest: git worktree helpers + provisioning"
status: accepted
date: 2026-08-29
tags: [harvest, git, worktrees, transplant]
source: agent-minder internal/git/git.go, internal/supervisor/worktreeinclude.go, jobmanager.go:168-268
related: "[[0008-workflows-deterministic-steps]]"
---

# Harvest: worktree helpers

**Applies only if Trigger jobs use worktrees** ([[0008-workflows-deterministic-steps]]).
Three pieces: `internal/git` primitives, the `.agent-minder/worktreeinclude` copier,
and the `setup.sh` hook.

## `internal/git` — worktree surface

- `WorktreeAdd(repo, path, branch, startPoint...)` — new branch from `origin/<base>`
  (never HEAD; fresh worktrees must start from the current base).
- `WorktreeAddExisting` (re-checkout an existing branch), `WorktreeRemove` (`--force`),
  `WorktreePrune`, `WorktreeRemoveByBranch`, `DeleteBranch`.
- `Worktrees` parses `worktree list --porcelain`; **first entry is the main worktree**.
- Plain `exec` wrappers with stderr in errors; no shelling through `sh -c`.

## Worktree setup sequence (`SetupWorktree`)

```
mkdir parent → (under a package-level mutex, since concurrent worktree
adds race):
  WorktreeRemoveByBranch(branch)   # may live under another deploy's dir
  WorktreePrune()                  # stale bookkeeping
  DeleteBranch(branch)             # stale branch from previous run
  WorktreeAdd(..., "origin/"+base)
→ copyWorktreeIncludes → UpdateJobWorktree (path + branch on job row)
```

Cleanup-first is the load-bearing part: stale worktrees/branches from killed runs are
removed *by branch*, not by path, because a previous run may have used a different
directory.

## `worktreeinclude` — copying untracked local files into fresh worktrees

Tracked `.agent-minder/worktreeinclude` = gitignore-style allowlist (negation `!`,
`/`-anchored, trailing `/` dir-only, `**` via hand-rolled doublestar→regexp). Candidates
are **untracked regular files only** (`git ls-files` set difference + walk). Copy
safety rules, each from a real failure mode:

- **Files only** — symlinks and non-regular files skipped (symlink escapes).
- **`O_EXCL` create** — never overwrite an existing worktree file.
- **`os.SameFile` recheck after open** — source replaced mid-copy is skipped.
- **Unused patterns warn** ("matched no untracked files") — typo'd patterns are
  visible instead of silently doing nothing.
- Failures are *warnings*, never job failures.

## `setup.sh` hook — provisioning before the agent starts

If the worktree contains tracked `.agent-minder/setup.sh`, run
`env bash <relpath>` from the worktree (no exec bit required) before starting the
agent. Hardening:

- **Non-zero exit or timeout blocks the job** (`failure_reason=setup_hook`) — agents
  never start in a half-provisioned workspace; output tail (32KB log / 4KB detail)
  lands in `failure_detail`.
- Timeout via `context.WithTimeout`, default 5m, configurable (`context.setup_timeout`).
- Start/finish markers + stdout/stderr go to the job log.

## Related bonus

`FetchWithRetry` (5s/15s/30s backoff) + `IsNetworkError` string patterns — fetch is
the one git op that hits the network and must retry transiently.

## Transplant note

`internal/git` lifts near-verbatim (drop the GitHub-specific extras if unused).
`worktreeinclude.go` is self-contained (300 lines + tests) — lift whole. The setup-hook
runner is woven into `SlotContext`; lift the *contract* (timeout, log tail, block on
failure, `env bash`, markers) and re-home it.
