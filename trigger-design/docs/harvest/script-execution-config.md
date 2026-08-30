---
title: "Harvest: script execution config"
status: accepted
date: 2026-08-29
tags: [harvest, scripts, config, deterministic-steps, transplant]
source: agent-minder internal/supervisor/script.go, internal/scheduler/config.go, db schema
related: "[[0008-workflows-deterministic-steps]]"
---

# Harvest: script execution config

The `kind: script` job — a deterministic, non-LLM step. Four nullable columns on both
`jobs` and `job_schedules`:

| Column | Form | Notes |
| --- | --- | --- |
| `script_command` | shell string | required for script jobs |
| `script_timeout` | Go duration string (`"30s"`) | optional; no value = no cap |
| `script_env` | JSON object | additive overrides on top of inherited env |
| `script_work_dir` | path | absolute, or relative to repo root |

YAML shape (`jobs.yaml`): `kind: script`, `command:`, `timeout:`, `env:` (map),
`workdir:` (alias `working_dir:`).

## Validation rules that caught real misuse

- `kind: script` **requires** `command`.
- `kind: script` **rejects agent fields** (`runtime`/`model`/`budget`/`max_turns`…)
  with an explicit "cannot be combined" error — a script job is exactly one kind.
- Config sync **replaces** the execution config wholesale: converting agent↔script
  clears the other side's fields (stale fields otherwise linger and mislead).

## Execution hardening (supervisor/script.go)

1. `sh -c command` via `exec.CommandContext`; env is `os.Environ()` **then** JSON
   overrides appended (Go keeps the last duplicate → overrides win).
2. Timeout = `context.WithTimeout`; `DeadlineExceeded` ⇒ reason `timeout`, process
   killed, not merely reported.
3. Exit-code extraction (`exec.ExitError`); negative code ⇒ generic detail.
4. stdout+stderr both stream to the job log file; log path stored on the run row.
5. Every run is tracked in `agent_runs` (stage=`script`, agent=`script`) — scripts get
   the same run-record audit trail as agents.
6. Failure-reason vocabulary: `script_config`, `log`, `script_env`, `timeout_config`
   (unparseable duration), `timeout`, `process_exit`.
7. Job completion + durable event in one tx (`EmitDurableWith`) — store-first publish.

## Transplant note

This is the model for [[0008-workflows-deterministic-steps]] rule 2 (`script` steps are
deterministic, run exactly once). For Trigger: same four knobs on the step def, same
"script steps reject agent fields" validation, and keep **timeout kill + explicit
`timeout` reason** — a script that hangs must be killed and labeled, not left draining
a slot. The `workdir`/`working_dir` alias exists because both spellings occurred in
real configs; pick one, keep the validator strict.
