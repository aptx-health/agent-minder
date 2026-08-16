# Wide Exploration Before First Write Exhausts Turns

## What happened

An automated agent implementing issue #652 stopped after 100 turns and $5.03. The implementation — a new cross-package E2E test plus CI wiring — was complete and correct, but the run hit `max_turns` while editing the changelog, before it could commit, push, or open a PR.

Of 99 tool calls, 65 were pure exploration (Read/grep/sed) across five unfamiliar packages (`coordinator`, `controlapi`, `daemon`, `runtime`, `supervisor`) before the agent wrote a single line of the new test file. Only ~13 turns went to writing the test, iterating on it, and wiring CI/Makefile at the end.

Two repetitive patterns show up in that exploration:

- **Permission-denial thrash.** The agent tried to spin up a scratch git repo under `/tmp` to check `WorktreeAdd` behavior, and hit 3 near-identical permission denials in a row, each still shaped as `cd /tmp && ...` or an equivalent that didn't address why the prior attempt was blocked:
  ```sh
  cd /tmp && rm -rf gittest && mkdir gittest && git init -q ...
  set -e; rm -rf ...; git -C /tmp/gittest init -q ...
  rm -rf /tmp/gittest /tmp/gittest-wt && mkdir /tmp/gittest && git -C ...
  ```
  It never got past the allowlist and abandoned the scratch-repo approach entirely.
- **Creeping/overlapping file reads.** On `supervisor.go` it read lines 596–650, then 650–695, then 685–740 — three overlapping windows to find one thing, instead of one wider read. The same pattern showed up as repeated narrow greps for the same symbol (`Hooks\b`, `TestHooks`, `SetHooks`, `newSlotContext`; `InstallAgentDef` grepped twice against different globs) instead of one combined query.

This is the same failure shape as the #648 incident (see `automated-agent-turn-efficiency.md`), but in the discovery phase rather than a golden-file retry loop — the run never got expensive per-call, it just paid a per-lookup turn tax across ~15 symbols/files before producing code.

## How to prevent it

### Agent design

- Give phases explicit tool-call budgets, including a discovery phase cap (e.g. 25–30 turns) before code must start, not just a total budget.
- Add a denial circuit breaker: after one permission denial, stop reformulating the same command shape and either drop the approach or ask for approval once.
- Prefer one wide `Read` (or a single `rg`/`grep` with a combined pattern) over multiple narrow, overlapping reads of the same file or repeated single-symbol greps.
- Reserve turns for final validation, commit, push, and PR creation — treat a green implementation as a checkpoint to commit before further polish.

### General guidance

- When a task spans several unfamiliar packages, consider a single upfront wide-read pass (or a dedicated search/explore step) over the touched files rather than incremental narrow lookups driven by the current line of code.
- `cd <path> && ...` Bash command shapes can defeat permission allowlisting; use `-C`/absolute paths instead, which also avoids repeated denials burning turns on the same underlying problem.
