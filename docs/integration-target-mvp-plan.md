# Milestone: Issue-declared integration targets

> **Re-baselined 2026-08-08.** This plan was written against schema v9 with the
> migration slated as v10→v11; both PR #578 (schema v10, issue #571 provenance)
> and #583 (`agent_runs`, schema v11) have since landed, and the `#569` Coordinator
> extraction is merged. Every version number and sequencing note below is
> corrected in place per `docs/research/fable-expedition/03-integration-target-domain-review.md`
> §2, whose four re-baselining amendments are: (1) the migration is now **v11→v12**;
> (2) drop the `#569` sequencing caution — serialize instead against whichever v12+
> migration is in flight and against the M1-13/16 `SlotContext` supervisor edits;
> (3) issue 2's target-column persistence must land in **both** `CreateJob` and
> `BulkCreateJobs` (see #602, which unifies the two insert paths); (4) `Prepare`
> writes no provenance today, so provenance completion should land before or with
> issue 2. See that document's §§2–12 for the full domain review, adversarial
> findings, and sequencing plan — it supersedes this plan's design and risk
> framing wherever the two disagree; this file keeps the product narrative and
> flow diagrams.

## Summary

Add a branch-only integration-target MVP for GitHub-issue-triggered jobs.

A reactive issue may declare:

```markdown
## Agent Minder
Integration target: branch:agent/issue-101
```

Minder will snapshot that declaration when it creates the job, fetch the branch from the canonical `origin`, resolve it to a full commit SHA before dispatch, create `agent/issue-102` from that SHA, and instruct the agent to open its draft PR against `agent/issue-101`.

Issues without this metadata retain the current default-branch behavior exactly. Proactive/cron jobs, issue/PR/commit resolvers, automatic restacking, and `gh stack` automation remain out of scope.

Planning baseline (historical): current remote `origin/main` at `bfbc705` (schema v9), 32 commits behind at plan time. **Superseded:** PR #578 (issue #571 provenance) landed as schema v10, and `agent_runs` (#583) landed as schema v11; the integration-target migration now targets **v11→v12**, the next free slot per `internal/db/schema.go:13`. See the re-baselining note above.

## Current reactive flow

```text
minder deploy
        ↓
cmd.runDeploy validates that .agent-minder/jobs.yaml exists
        ↓
create db.Deployment
        ↓
runForeground (default without --serve) or runDaemon
        ↓
load scheduler.Config and SyncSchedules
        ↓
triggerRoutesFromConfig converts trigger jobs to supervisor.TriggerRoute
        ↓
Supervisor.Launch
        ↓
initial WatchTick, then GitHub poll every 2 minutes
        ↓
ListIssuesByLabels / ListIssuesByMilestone
        ↓
resolveRouteForIssue chooses the most-specific matching label route
        ↓
createJobForIssue fetches the issue body and persists db.Job(status=queued)
        ↓
fillCapacity calls git fetch origin, but does not gate launch on fetch success
        ↓
QueuedUnblockedJobs applies dependency-graph blocking
        ↓
job is marked running
        ↓
newSlotContext chooses agent/issue-<number>
        ↓
SetupWorktree creates it from origin/<BaseBranch>
        ↓
agent runs, commits, pushes, and executes gh pr create
        ↓
Minder detects the PR, runs review stages, and records PR/status metadata
```

Important current details:

- `.agent-minder/jobs.yaml` contains both reactive `trigger:` jobs and proactive `schedule:` jobs.
- Trigger routing carries agent, runtime, model, budget, and turn overrides into the activated job on current `origin/main`.
- The issue body is stored as `jobs.issue_body`, but no machine-readable runtime metadata is parsed from it.
- Jobs YAML keys are about to be recorded as `source_name` by issue #571, alongside `source_type` and `source_ref`.
- `knownJobs` is keyed by `(issue number, agent)`, so the selected route is effectively frozen on the created job, although a later label change can activate another agent for the same issue.
- Reactive branch naming is hardcoded as `agent/issue-<number>` in `newSlotContext`; `AgentContract.BranchPrefix` is not currently used for it.
- `gitSetupMu` serializes worktree setup within one supervisor, but `SetupWorktree` currently removes any worktree/local branch using the intended child branch before recreating it.

### Where the default branch becomes fixed

The base is established in two stages:

1. `cmd.runDeploy` resolves `--base-branch`, then `origin/HEAD`, then `main`, and stores it on `db.Deployment.BaseBranch`.
2. `supervisor.newSlotContext` calls `resolveBaseBranch`, whose actual precedence is:
   - `.agent-minder/onboarding.yaml` `context.base_branch`;
   - `Deployment.BaseBranch`;
   - Git default branch;
   - `main`.

`SetupWorktree` then uses `origin/<SlotContext.BaseBranch>` as its start point. The same `BaseBranch` is also used by prompt generation for pre-push rebasing and `gh pr create --base`, so worktree start point and PR base are currently conflated.

## Current targeted flow

```text
minder deploy 102
        ↓
parse issue numbers
        ↓
create Deployment
        ↓
supervisor.Prepare
        ↓
FetchItem + FetchItemContent for each issue
        ↓
BulkCreateJobs
        ↓
if multiple issues, build and apply an LLM dependency graph
        ↓
same Supervisor, SlotContext, worktree, agent, review, and PR pipeline
```

Differences from reactive activation:

- `minder deploy 102` does not select the agent from issue labels or jobs YAML. It uses `--agent`, whose default is `autopilot`.
- The targeted ingestion path is `supervisor.Prepare`; the watcher uses `createJobForIssue`.
- Multiple targeted issues receive an automatically selected dependency-graph option.
- After jobs are created, both modes share the same supervisor, branch, worktree, agent, review, and PR flow.
- The deployment still loads jobs YAML afterward, so a targeted deployment can also remain alive for configured schedules/triggers.

## Recommended declaration interface

Use one strictly parsed field in a dedicated Markdown section:

```markdown
## Agent Minder
Integration target: branch:agent/issue-101
```

Rules:

- No `## Agent Minder` section means no integration target.
- If the section exists, it must contain exactly one `Integration target:` field.
- Multiple sections or multiple fields are invalid.
- The MVP accepts only `branch:<branch-name>`.
- `issue:`, `pr:`, `commit:`, `ref:`, raw SHAs, `origin/foo`, and full `refs/...` values are rejected as unsupported.
- Validate the branch portion with Git’s branch-name rules before interpolating it into any command.
- Backticks, natural-language aliases, and inferred relationships are not accepted.
- The issue body remains stored, so malformed raw input is available for diagnosis.

This fits the repository because its issues already use visible Markdown sections, but it has no issue template, form, front matter, or existing issue-metadata convention. It also leaves a stable typed namespace for future resolvers without placing arbitrary branch names in labels.

## Proposed domain model

Add `internal/integrationtarget` with explicit types rather than passing a `base_branch` string through the code:

```go
type Kind string
const KindBranch Kind = "branch"

type Source string
const (
    SourceIssueBody   Source = "issue_body"
    SourceCLIOverride Source = "cli_override"
)

type Declaration struct {
    Kind      Kind
    Value     string
    Canonical string // branch:agent/issue-101
    Source    Source
}

type Resolution struct {
    RemoteRef          string // refs/remotes/origin/agent/issue-101
    CommitSHA          string // full 40-character SHA
    PullRequestBase    string // agent/issue-101
    ResolvedAt         time.Time
}
```

Keep parsing, source selection, resolution, and persisted results distinct. A future milestone can add resolver implementations for `issue:`, `pr:`, or `commit:` without changing job activation or worktree APIs.

## Proposed reactive flow

```text
minder deploy
        ↓
load jobs.yaml trigger routes
        ↓
discover labeled GitHub issue
        ↓
select TriggerRoute by existing label specificity rules
        ↓
fetch issue body
        ↓
parse optional ## Agent Minder metadata
        ↓
persist selected job + integration-target declaration
        ↓
dependency-ready and slot available?
        ↓
target missing?
   yes ─────→ existing default-base dispatch path, unchanged
   no
        ↓
validate target is not the intended child branch
        ↓
fetch exact refs/heads/<branch> from origin
        ↓
resolve refs/remotes/origin/<branch>^{commit}
        ↓
persist remote ref + full SHA + PR base + timestamp
        ↓
atomically claim agent/issue-<number>
        ↓
mark running
        ↓
git worktree add -b <child> <pinned SHA>
        ↓
verify worktree HEAD equals the pinned SHA
        ↓
run selected agent
        ↓
push ordinary child branch and open draft PR against target branch
```

Blocked path:

```text
declared target is valid but unavailable on origin
        ↓
status=blocked
failure_reason=integration_target_ref_unavailable
retry_at=next target-resolution attempt
        ↓
emit one visible blocked event and expose reason in status/API
        ↓
do not consume an agent slot or add in-progress label
        ↓
retry during a later supervisor cycle
        ↓
branch appears
        ↓
resolve, clear transient failure, and dispatch
```

Resolution must happen after dependency readiness and immediately before dispatch. This lets a dependency-blocked job pick the parent branch’s latest commit at dispatch time, while keeping the child deterministic after that point.

## Recommended CLI behavior

Add:

```text
minder deploy 102 --integration-target branch:agent/issue-101
```

Constraints:

- Allowed only when exactly one explicit issue number is supplied.
- With zero issues, reject it rather than accidentally defining a deployment-wide target.
- With multiple issue numbers, reject it rather than silently applying one parent to all children.
- Multiple targeted issues without the flag still read their own issue metadata independently.
- Parse and validate the CLI value before creating the deployment record.

Precedence:

```text
explicit --integration-target
        ↓
issue-body Integration target field
        ↓
no integration target: existing onboarding/deployment/default-branch behavior
```

There is no jobs YAML or deployment-level integration-target default in the MVP.

If CLI and issue metadata differ, the CLI wins because the operator supplied an explicit recovery override, but Minder must print and emit a notice showing both values. The effective source is persisted as `cli_override`. A valid CLI override may also recover from malformed issue metadata, with a warning rather than silently ignoring the malformed field.

The daemon re-exec does not need the flag propagated: the chosen target is persisted on the job before re-exec.

## Jobs YAML impact

No jobs YAML schema change.

Rationale:

- The target varies per issue, while jobs YAML defines reusable automation policy.
- A user who can create/edit and label an issue can already cause an agent with repository credentials to execute; restricting same-repository parent branches does not add a meaningful new permission boundary requiring `allow_integration_target`.
- Existing jobs YAML files remain valid and unchanged.
- Job selection remains label- or milestone-based.
- Trigger route provenance from issue #571 should identify the selected jobs YAML key and matched trigger separately from integration-target source.

Proactive cron jobs do not parse or accept integration targets in this milestone. Their existing deployment base remains unchanged.

## Data model

Issue #571/PR #578 (schema v10) and #583's `agent_runs` table (schema v11) have both
landed. Add a **v11→v12** additive migration on `jobs` (next free slot per
`internal/db/schema.go:13`):

| Column | Meaning |
|---|---|
| `integration_target_source` | Nullable `issue_body` or `cli_override` |
| `requested_integration_target` | Canonical declaration such as `branch:agent/issue-101`; raw unique value may be retained here on syntax failure |
| `resolved_base_ref` | Fully qualified canonical remote-tracking ref |
| `resolved_base_sha` | Full pinned commit SHA |
| `pull_request_base_branch` | GitHub branch name to use as the child PR base |
| `integration_target_resolved_at` | Successful resolution timestamp |
| `integration_target_retry_at` | Next eligible retry for a target-blocked job |

Continue using existing fields for:

- triggering issue: `issue_number`, `issue_title`, `issue_body`;
- selected automation/job: forthcoming `source_type`, `source_name`, `source_ref`;
- selected agent: `agent`;
- child branch/worktree: `branch`, `worktree_path`;
- blocked/failed diagnostics: `status`, `failure_reason`, `failure_detail`;
- PR result: `pr_number`.

Reset and recovery semantics:

- Crash recovery retains a previously resolved SHA so recreation starts from the same commit.
- An explicit `ResetJob` clears resolution, PR-base, and retry fields but retains the declaration/source, causing a fresh resolution.
- No-target jobs leave all new fields null.
- On successful retry, clear transient target failure fields and `integration_target_retry_at`.

## Branch, commit, and PR semantics

For `branch:agent/issue-101`:

```text
requested target:    branch:agent/issue-101
canonical source:    origin
remote ref:          refs/remotes/origin/agent/issue-101
resolved SHA:        abc123... (full SHA)
child branch:        agent/issue-102
PR base branch:      agent/issue-101
```

Decisions:

- Fetch the exact `refs/heads/<branch>` from `origin`; do not accept a local-only branch.
- Use a fully qualified refspec and validated branch value so user input cannot become a Git option or arbitrary ref destination.
- Resolve with commit peeling and persist the full SHA before marking the job running.
- Create the child branch from the SHA, not from the moving remote ref.
- Do not fail if the parent advances after resolution; the stored SHA is intentionally authoritative.
- Do not automatically rebase or restack an explicit-target child before push. Current generated “rebase onto base” instructions must be omitted for these jobs.
- Open the child PR against the declared parent branch. This keeps the PR diff limited to the child’s work.
- After detecting the PR, verify its actual GitHub base matches `pull_request_base_branch`. On mismatch, mark the job `manual` with `pr_base_mismatch`; report expected and actual bases, but do not edit the PR automatically.
- Permit an open parent PR.
- Require the parent branch to be pushed to `origin`.
- Do not require a parent PR, passing CI, review approval, or merge.
- If the parent branch is deleted after child creation, keep the pinned child branch untouched; PR creation may require manual recovery.
- An explicit `branch:main` is valid and records/pins the otherwise-default behavior.
- A target equal to the intended child branch is a terminal validation error before any worktree or branch cleanup occurs.

Split the current `SlotContext.BaseBranch` responsibility into:

- repository/default base;
- worktree start point or pinned start SHA;
- pull-request base branch;
- optional integration-target declaration/resolution.

## Job-state behavior

| Situation | Behavior |
|---|---|
| No integration target | Existing path unchanged: start from `origin/<resolved default base>` and retain current rebase/PR instructions |
| Valid remote branch | Resolve and persist SHA, then dispatch from that SHA |
| Branch not yet on `origin` | `blocked`, reason `integration_target_ref_unavailable`, retry every 2 minutes |
| Permanently missing ref | Indistinguishable from “not yet created” in this MVP; remain visibly blocked until it appears or the operator starts a corrected targeted deployment |
| Malformed/unsupported syntax | Terminal `failed`, reason `integration_target_invalid`; never fall back to default |
| Multiple declarations | Terminal `failed`, reason `integration_target_invalid` |
| Target equals child branch | Terminal `failed`, reason `integration_target_conflict` |
| Network fetch failure | `blocked`, reason `integration_target_fetch_failed`; use the supervisor’s existing offline backoff and persist the next attempt |
| Authentication/permission failure | `blocked`, reason `integration_target_access_denied`; retry at a slower 15-minute interval so credential repair can recover without a restart |
| Other Git fetch failure | `blocked`, reason `integration_target_fetch_failed`, retry after 5 minutes |
| Local branch exists but remote does not | Treat as unavailable; never use the local branch |
| CLI and issue differ | CLI wins; log both values and persist `cli_override` |
| Issue body changes while queued/blocked | Existing job keeps its activation-time snapshot; no silent retargeting. A corrected targeted deployment/override is the MVP recovery path |
| Labels change after selection | Existing job keeps its selected agent, automation provenance, and target. Any newly activated job must pass branch ownership checks |
| Target advances after resolution | Ignore for this run; child remains based on the pinned SHA |
| Actual PR uses wrong base | `manual`, reason `pr_base_mismatch`; do not rewrite the PR |
| Another active job owns the child branch | Keep the new job `blocked`, reason `branch_in_use`; never remove the active worktree |

Target blocking occurs before `UpdateJobRunning`, so it consumes no runtime slot, budget, worktree, or GitHub `in-progress` label.

Extend dispatch eligibility so target-blocked jobs are reconsidered only when `integration_target_retry_at` is due. Dependency-only `blocked` jobs retain current behavior.

Use an atomic SQLite transaction when marking a job running and assigning its planned branch: check for another active job in the same owner/repo with that branch and statuses `running`, `reviewing`, or `waiting`, then claim it. Also make `SetupWorktree` refuse to remove a worktree owned by another active job. This protects multiple deployments sharing the host database as well as concurrent jobs in one supervisor.

## Observability and interface additions

Additive fields should appear in:

- `GET /jobs` and `GET /jobs/{id}`;
- daemon client response types;
- `minder status <deploy-id> --json`;
- human `minder status` detail for targeted or blocked jobs.

Events/logs must expose:

- issue number;
- selected jobs YAML key and trigger expression when provenance is available;
- selected agent;
- integration-target source;
- requested target;
- resolved remote ref;
- pinned SHA;
- child branch;
- PR base branch;
- blocked reason and next retry.

Do not post repetitive GitHub issue comments or add the GitHub `blocked` label during target preflight. The persisted job status and daemon/foreground event log are the operator surface.

## Milestone issues

### 1. Define and parse issue-declared integration targets

User-visible outcome: Issues can contain one deterministic, validated `Integration target` declaration without affecting label routing.

Implementation scope:

- Add the `internal/integrationtarget` domain package.
- Implement typed parsing and canonical formatting for `branch:<name>`.
- Implement strict extraction from the `## Agent Minder` issue-body section.
- Add Git branch-name validation.
- Define typed invalid, unsupported-kind, duplicate, and missing-field errors.
- Do not implement Git resolution yet.

Likely files/components:

- `internal/integrationtarget/target.go`
- `internal/integrationtarget/issuebody.go`
- corresponding tests
- `internal/git/git.go` only if branch validation is exposed there

Acceptance criteria:

- Section absent returns no declaration and no error.
- One valid field returns a canonical branch target.
- Multiple sections/fields, empty values, invalid branch names, and unsupported kinds return precise errors.
- Labels and prose elsewhere in the issue are ignored.
- No natural-language inference occurs.

Tests required:

- Table-driven parser and issue-body tests.
- Branch names containing slashes, dots, and hyphens.
- Rejection of option-like values, `origin/` names, SHAs, full refs, duplicates, and malformed headings.

Dependencies: none.

Risks/decisions: Markdown parsing must stay deliberately narrow. The exact section and field syntax above is locked for the MVP.

### 2. Persist integration-target input during reactive and targeted activation

User-visible outcome: Both unattended issue activation and `minder deploy <issue>` create jobs containing the same issue-specific integration metadata; targeted CLI override is supported.

Implementation scope:

- Add the v11→v12 job migration (issue #571/PR #578 and #583's `agent_runs` table have already landed as v10/v11).
- Extend `db.Job`, inserts, reset behavior, and store update helpers. Target columns must be added to **both** `CreateJob` and `BulkCreateJobs` — see #602, which unifies the two insert paths; landing persistence through only one would repeat the same gap `agent_runs`/provenance hit before (Expedition III §2.3, Expedition I C9).
- Parse issue metadata in both `Supervisor.createJobForIssue` and `supervisor.Prepare`.
- Add `--integration-target` with the one-explicit-issue restriction.
- Apply and report the precedence rules.
- Create failed jobs for malformed issue metadata so reactive operators can see the failure.
- Reject malformed CLI input before deployment creation.
- Preserve trigger/job provenance independently from target source.

Likely files/components:

- `internal/db/schema.go`, `models.go`, `queries.go`, `db_test.go`
- `internal/supervisor/watch.go`, `depgraph.go`
- `cmd/deploy.go`, `cmd/deploy_test.go`
- schema-version references in `CLAUDE.md` if required by existing drift tests

Acceptance criteria:

- Reactive and targeted jobs persist identical canonical metadata for the same issue body.
- CLI overrides issue metadata and records `cli_override`.
- CLI with zero or multiple issue arguments fails clearly.
- Existing jobs and jobs YAML migrate without modification.
- No-target jobs have null integration fields and unchanged behavior.
- Invalid reactive declarations never become queued/running default-base jobs.

Tests required:

- v11→v12 migration and fresh-schema tests.
- Watch activation with trigger provenance plus issue target.
- Targeted `Prepare` with issue metadata.
- CLI precedence, warning, cardinality, and malformed-input tests.
- Backward-compatibility tests for existing job creation paths.

Dependencies: milestone issue 1 and upstream issue #571/PR #578.

Risks/decisions: This migration must not run concurrently with another schema task — it owns the v12 queue slot. The `#569` Coordinator extraction has landed (`internal/coordinator` exists); target parsing, persistence, resolution, and worktree setup remain below the Coordinator boundary, so no serialization against it is needed. Instead, serialize against whichever v12+ migration is in flight and against the M1-13/16 `SlotContext` split (Expedition III §2.2, §11).

### 3. Resolve canonical branch targets and gate dispatch safely

User-visible outcome: A job waits visibly without consuming a slot until its declared remote branch can be pinned.

Implementation scope:

- Add exact-branch fetch and full-SHA resolution helpers in `internal/git`.
- Add a supervisor pre-dispatch phase before `UpdateJobRunning`.
- Persist successful resolution or classified blocked state.
- Extend dispatch eligibility with `integration_target_retry_at`.
- Reuse existing offline state/backoff for network failures.
- Add atomic child-branch ownership claiming and active-worktree protection.
- Reject target/child self-reference before cleanup.
- Preserve the old path when no target exists.

Likely files/components:

- `internal/git/git.go`, `git_test.go`, `retry_test.go`
- `internal/supervisor/supervisor.go`, `network_test.go`, `multi_agent_test.go`
- `internal/db/queries.go`, `db_test.go`

Acceptance criteria:

- Only the canonical `origin` branch can satisfy a declaration.
- Successful resolution records a full SHA before the job is running.
- Missing branches become retryable `blocked` jobs without a rapid launch/failure loop.
- A later-created branch allows automatic dispatch.
- Network, access, missing-ref, and syntax/conflict failures remain distinguishable.
- Two active jobs cannot claim the same child branch.
- Two different issue branches can resolve/create concurrently without affecting each other.

Tests required:

- Temporary bare-origin repositories for exact fetch/ref resolution.
- Local-only branch rejection.
- Missing branch followed by later branch creation and retry.
- Target advancement after resolution.
- Retry-time eligibility tests.
- Concurrent branch-claim and active-worktree collision tests.
- Regression test proving no-target dispatch still follows the existing path.

Dependencies: milestone issue 2.

Risks/decisions: Git error text varies by transport; classification should use stable exit/error categories where possible and retain the original sanitized error in `failure_detail`.

### 4. Create child worktrees from pinned SHAs and preserve PR-base semantics

User-visible outcome: The child agent starts on the recorded parent commit, owns a new ordinary branch, and opens a normal draft PR against the parent branch without automatic restacking.

Implementation scope:

- Split default base, worktree start, and PR base inside `SlotContext`.
- Pass `resolved_base_sha` to `WorktreeAdd`.
- Verify worktree `HEAD` equals the stored SHA before invoking the agent.
- Update repo and review context to show target, ref, SHA, child branch, and PR base.
- For explicit targets, remove automatic rebase instructions and generate `gh pr create --draft --base <parent>`.
- Keep existing commands byte-for-byte where practical for no-target jobs.
- Extend GitHub PR lookup to expose actual base and flag mismatches as manual.
- Never modify, check out, rebase, or force-push the parent branch.

Likely files/components:

- `internal/supervisor/jobmanager.go`, `context.go`, `pipeline_test.go`, `context_test.go`
- `internal/git/git.go`
- `internal/github/github.go` and tests

Acceptance criteria:

- `agent/issue-102` is created from the recorded SHA even if the remote parent advances.
- The target branch remains untouched and is never attached to the child worktree.
- Explicit-target prompts contain no automatic rebase/restack command.
- The draft PR command uses the parent branch as its base.
- Wrong actual PR base is surfaced as `manual`; Minder does not edit it.
- Default-base jobs retain their current rebase and PR behavior.

Tests required:

- Worktree HEAD assertion against a pinned SHA.
- Parent advancement race between resolution and worktree creation.
- Prompt golden tests for default and explicit-target jobs.
- PR-base validation tests.
- Same-target/child and parent-worktree safety regressions.

Dependencies: milestone issue 3.

Risks/decisions: Agents may still choose commands outside the generated guidance; post-creation PR-base validation makes any divergence visible without autonomous correction.

### 5. Expose target status, add vertical behavioral coverage, and document rollout

User-visible outcome: Operators can verify every target decision through status/API/logs and follow a documented two-issue experiment.

Implementation scope:

- Add integration fields to daemon API and CLI JSON response types using backward-compatible optional fields.
- Add concise human status detail for targeted and target-blocked jobs.
- Expand discovery, blocked, resolution, start, and PR-base event summaries.
- Add end-to-end supervisor behavioral tests using temporary Git repositories and GitHub fixtures.
- Document issue syntax, CLI override, retry behavior, PR semantics, and limitations.
- Keep `.agent-minder/jobs.yaml` examples unchanged except for a note that target data is issue-specific.

Likely files/components:

- `internal/daemon/server.go`, `client.go`, tests
- `cmd/status.go`, status/deploy tests
- `internal/supervisor/scenario_test.go` and automation correctness specs
- `README.md`
- optional focused `docs/integration-targets.md`

Acceptance criteria:

- Status surfaces show issue, selected automation/agent, target source/value, resolved ref/SHA, child branch, PR base, and blocked reason/retry.
- Existing API clients tolerate the additive fields.
- A behavioral test covers issue discovery → routing → blocking → resolution → pinned worktree creation.
- Documentation includes the manual reactive and targeted experiments below.
- `go test ./...` and `make integration` pass.

Dependencies: milestone issues 1–4.

Risks/decisions: Current supervisor events are not durable; persisted job fields are the authoritative record until the planned control-plane event model lands.

## Rollout experiment

Use two disposable issues carrying the existing `agent-ready` label so `.agent-minder/jobs.yaml` selects `autopilot`.

### Issue A

Ask for a small shared model, interface, or utility. Do not add an Agent Minder section.

Expected behavior:

```text
main
└── agent/issue-<A>
```

Minder uses the existing default-base behavior and eventually pushes the branch/open PR.

### Issue B

Ask it to consume Issue A’s change:

```markdown
## Agent Minder
Integration target: branch:agent/issue-<A>
```

Start the normal long-running deployment:

```text
minder deploy
```

Because jobs YAML contains trigger routes, the foreground process remains alive, performs an initial poll, and continues polling. `--watch` is not required.

Expected progression:

1. Both issues are discovered through label routing and record the `agent-ready` jobs YAML trigger plus `autopilot`.
2. Issue A launches from `main`.
3. Issue B enters `blocked` while `origin/agent/issue-<A>` is absent.
4. Issue A pushes its ordinary branch.
5. A later resolution attempt pins its full SHA.
6. Minder creates `agent/issue-<B>` from that SHA.
7. Issue B pushes its ordinary branch and opens a draft PR based on `agent/issue-<A>`.

Expected structure:

```text
main
└── agent/issue-<A>
    └── agent/issue-<B>
```

Verify with `minder status <deploy-id> --json`, `git log`, `git merge-base`, and `gh pr view` that:

- A and B selected the correct jobs YAML route and agent.
- B recorded `issue_body` as target source.
- B did not run before the target resolved.
- B’s first child commit descends from the persisted `resolved_base_sha`.
- A’s and B’s worktrees use distinct child branches.
- Neither agent checked out or modified the other’s branch.
- Both branches are ordinary remote Git branches.
- A’s PR base is `main`; B’s PR base is A’s branch.
- No force-push, automatic rebase, restack, or `gh stack` action occurred.
- A supervisor can subsequently use GitHub’s `gh stack` tooling manually to inspect or align the relationship.

### Targeted override test

Repeat the B half from a clean disposable child branch, or before labeling B for the reactive run:

```text
minder deploy <B> \
  --integration-target branch:agent/issue-<A> \
  --foreground
```

Verify that:

- `--agent`/default `autopilot`, not label routing, selects the targeted agent.
- The persisted target source is `cli_override`.
- If B’s body names another target, the console/event log reports the overridden issue value.
- Worktree start SHA and PR base match the CLI target.
- Running the flag with no issue or multiple issues fails before deployment creation.

## Out of scope

- Automatic dependency inference from issue prose
- Reading GitHub issue dependency relationships
- `issue:<number>` resolution
- `pr:<number>` resolution
- `commit:<sha>` or arbitrary-ref resolution
- Automatic issue-to-branch or PR-to-branch mapping
- Integration targets for proactive or cron jobs
- Jobs YAML integration-target defaults or policy fields
- Automatic stacked PR creation
- Automatic `gh stack` invocation
- Automatic restacking
- Automatic rebasing or force-pushing
- Updating descendants when a parent branch changes
- Re-resolving a target after child creation
- GitButler integration
- Changes to GitHub merge behavior
- Allowing multiple active agents to modify the same branch
- Fully automated dependency-DAG orchestration
- Broad jobs YAML redesign
- Durable event-stream/control-plane redesign
- Automatic refresh of integration metadata after a job activation snapshot

## Recommendation

1. This is a small, reasonably low-risk extension to the existing reactive pipeline. The two nontrivial seams are pre-dispatch retry gating and splitting the currently conflated `SlotContext.BaseBranch`; neither requires a broad scheduler or runtime rewrite.
2. Human-declared target metadata should live primarily in the GitHub issue. The effective declaration and resolution must be snapshotted in the persisted `jobs` row. Jobs YAML should continue to define routing, not per-issue ancestry.
3. Proactive jobs should be deferred because they have no issue metadata source and their integration semantics are not needed for the vertical slice.
4. The targeted CLI override belongs in the MVP as a narrowly scoped experiment/recovery tool, restricted to exactly one issue. It is secondary to the unattended issue declaration, not a replacement for it.
5. `db.Job` is the correct architectural seam for carrying issue-specific runtime metadata from `createJobForIssue`/`Prepare`, through dispatch, into `SlotContext` and worktree creation. Forthcoming job provenance fields identify the selected automation independently.
6. No current design forces a larger refactor. The Coordinator extraction (`#569`, landed) moved assembly code, but target parsing, persistence, resolution, and worktree setup remain below that boundary. Serialize the migration against other v12+ schema work instead, per the re-baselining note at the top of this document.

See `docs/research/fable-expedition/03-integration-target-domain-review.md` for the full independent domain review, including an unresolved disagreement over whether the CLI override belongs in the MVP (§10, §12) and adversarial findings (auto-merge/parent-branch contamination, swallowed activation fetch errors, and others in §7) that this plan predates.
