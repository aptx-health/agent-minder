---
title: "Config schema (design)"
status: draft
date: 2026-08-29
tags: [design, config, schema, workflow]
related: [[config-resolve-once]], [[0008-workflows-deterministic-steps]], [[0010-go-template-variables]]
---

# Config schema (design)

The declarative config for Trigger. Deliberately shaped like a GitHub Actions workflow:
named jobs, each with a trigger and an ordered list of steps; a step runs an inline script,
a script file, or an agent. This is the R3 deliverable in draft — it lands ADRs 0005–0008
and 0010.

## Location

- Primary: user-level `~/.trigger/config.yaml` — Trigger is a daemon that may drive many
  repos, so global jobs are the norm.
- A per-repo overlay (`.trigger/config.yaml`) is a later addition, not v1.

## Load discipline

Layer the file over built-in defaults (harvest pr-triage's `config.Load`): unmarshal only
overwrites present keys, so a partial config inherits sane defaults instead of producing an
empty jobs list. Effective per-step config is computed by resolve-once
([[config-resolve-once]]): `defaults → job → step`, most-specific wins, resolved at run
start, stored on the run record.

## Annotated example

```yaml
version: 1

defaults:                       # resolve-once base for every job/step
  runtime: opencode
  model: openrouter/z-ai/glm-5.3-flash
  timeout: 10m

jobs:
  - name: nightly-deps
    trigger: { cron: "0 3 * * *" }        # exactly ONE trigger kind per job
    steps:
      - name: update
        kind: agent
        agent: dependency-updater          # runtime/model inherited from defaults
        on_failure: stop
      - name: verify
        kind: script
        run: go test ./...                 # inline single command
        timeout: 5m
        on_failure: stop

  - name: implement-issue
    trigger:
      github: { event: issue, filter: { label: agent-ready } }
    steps:
      - name: implement
        kind: agent
        agent: autopilot
        runtime: claude-code               # overrides default (step wins)
        model: claude-sonnet-5
        with:                              # variables available to the prompt/context
          issue: "{{ .trigger.github.issue.number }}"
        secrets: [github_token]            # resolved from macOS Keychain (ADR 0006)
        permissions: { tools: [Bash, Edit, Write], network: none }
        on_failure: stop
      - name: test
        kind: script
        run: |                             # inline multi-line, GitHub-Actions-like
          make build
          make test
        shell: bash
        on_failure: escalate               # on_success defaults to next step

  - name: nightly-report
    trigger: { cron: "0 6 * * *" }
    steps:
      - name: gen
        kind: script
        run: ./scripts/report.py --day today   # invoke a script file
        shell: python
        work_dir: ~/repos/acme
        env: { REPORT_ENV: prod }

  - name: spike
    trigger: { manual: true }              # fired via CLI or MCP (ADR 0007)
    steps:
      - { name: run, kind: agent, agent: spike }
```

## Field reference

### Top level
- `version` (int, required) — schema version; unrecognized versions hard-fail.
- `defaults` (map) — base config inherited by every job/step (resolve-once level 0).
- `jobs` (list) — the jobs.

### Job
- `name` (string, required, unique) — job identity.
- `trigger` (map, required) — **exactly one** of:
  - `cron: "<expr>"`
  - `manual: true` — fired via CLI or MCP.
  - `webhook: { path, secret_ref }` — inbound HTTP fires the job.
  - `github: { event, filter }` — a GitHub event (one adapter, per ADR 0005).
- `steps` (list, required, ≥1) — ordered steps.
- Job-level overrides (`runtime`, `model`, `timeout`, `secrets`, `permissions`, `work_dir`,
  `env`) — resolve-once level 1.

### Step (common)
- `name` (string, required, unique within the job).
- `kind` (enum, required) — `agent` | `script`.
- `on_success` (enum/string) — default `next`; or a named step; or `stop`.
- `on_failure` (enum/string) — `stop` | `escalate` | a named step. Applies to **step-logic**
  failure only; infrastructure crashes resume mechanically ([[0008-workflows-deterministic-steps]]).
- `secrets` (list of names) — resolved from the Keychain at run time, never inlined (ADR 0006).
- `permissions` (map) — least-privilege policy passed to the runtime (ADR 0006).
- `with` (map) — variables exposed to templating ([[0010-go-template-variables]]).

### Step — `kind: script`
- `run` (string, required) — an inline command (single or multi-line) **or** an invocation
  of a script file. Multi-line runs execute as one script, GitHub-Actions style.
- `shell` (string, optional) — `bash` | `sh` | `python` | … ; default `bash`.
- `timeout`, `work_dir`, `env` — execution config (harvest agent-minder script fields).

### Step — `kind: agent`
- `agent` (string, required) — the agent definition name (neutral `*.agent.yaml`, ADR 0009).
- `runtime`, `model` — resolve-once overrides; empty defers to the agent/runtime default.
- `prompt` (string, optional) — inline task text, templated.

## Validation rules (fail loud at load)

1. `version` present and supported.
2. Every job `name` unique; every step `name` unique within its job.
3. Each job has **exactly one** trigger kind.
4. Each step is exactly one `kind`; script fields and agent fields do not mix.
5. Every `on_success`/`on_failure` named-step target exists in the same job.
6. `secrets` names must resolve at run time (missing secret = step fails, fail-closed).
7. No cyclic step routing (v1 is linear; a named-step jump must not create a loop).

## Go struct sketch

```go
type Config struct {
    Version  int               `yaml:"version"`
    Defaults StepDefaults      `yaml:"defaults"`
    Jobs     []Job             `yaml:"jobs"`
}
type Job struct {
    Name    string  `yaml:"name"`
    Trigger Trigger `yaml:"trigger"`   // exactly one field non-nil
    Steps   []Step  `yaml:"steps"`
    StepDefaults `yaml:",inline"`      // job-level overrides
}
type Trigger struct {
    Cron    *string        `yaml:"cron,omitempty"`
    Manual  *bool          `yaml:"manual,omitempty"`
    Webhook *WebhookTrig   `yaml:"webhook,omitempty"`
    GitHub  *GitHubTrig    `yaml:"github,omitempty"`
}
type Step struct {
    Name      string   `yaml:"name"`
    Kind      string   `yaml:"kind"`          // agent | script
    // script
    Run       string   `yaml:"run,omitempty"`
    Shell     string   `yaml:"shell,omitempty"`
    // agent
    Agent     string   `yaml:"agent,omitempty"`
    Prompt    string   `yaml:"prompt,omitempty"`
    // routing + shared
    OnSuccess string            `yaml:"on_success,omitempty"`
    OnFailure string            `yaml:"on_failure,omitempty"`
    With      map[string]string `yaml:"with,omitempty"`
    StepDefaults `yaml:",inline"`
}
type StepDefaults struct {
    Runtime     string            `yaml:"runtime,omitempty"`
    Model       string            `yaml:"model,omitempty"`
    Timeout     string            `yaml:"timeout,omitempty"`
    WorkDir     string            `yaml:"work_dir,omitempty"`
    Env         map[string]string `yaml:"env,omitempty"`
    Secrets     []string          `yaml:"secrets,omitempty"`
    Permissions Permissions       `yaml:"permissions,omitempty"`
}
```

## Record which config revision loaded

The loader must record **which config revision a run used** — path, SHA-256, load time, and
any validation error (Expedition I DM-3, [[fable-expedition-crosswalk]]). agent-minder had no
such record, so "why did this run behave that way" was unanswerable after an edit. Surface it
via `GET /version`/status and stamp it on runs, so a run is traceable to the exact config that
produced it.

## Open items

- Webhook trigger transport and auth — see R2 / R6.
- Exact permissions schema — see R5.
- Whether `with`/templating context includes prior-step outputs by default or opt-in —
  decide in [[0010-go-template-variables]].
