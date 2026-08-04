# Expedition III — Integration-Target Domain Review

Status: complete
Reviewed baseline: `origin/main` @ `b96d79da0b89fe98813a1dd34e9ef02051207e19` (merge of PR #596, 2026-08-03)
Subject under review: `docs/integration-target-mvp-plan.md` (planning baseline `bfbc705`, schema v9 — 30+ commits and two schema versions stale)
Consumes: `docs/research/fable-expedition/01-architecture-truth-map.md` (Expedition I — especially invariant §6.6, C3, C9, risk 7, DM-1/DM-2), `02-worker-process-topology-adr.md` (Expedition II — I-7 data plane).
Evidence reviewed: `internal/db` (schema, models, queries), `internal/supervisor` (supervisor, watch, jobmanager, depgraph, context, prompt), `internal/git`, `internal/github`, `cmd/deploy.go`; issues #571/#578, #583, #569; milestone 23 contract.

Claims are cited to file:line at the reviewed SHA. Statements derived by reading code paths rather than executing them are marked *(inference)*.

**Verdict in one paragraph.** The plan's product behavior is right and its temporal model — declaration snapshotted at activation, branch resolved to a pinned SHA at dispatch, child created from the SHA, PR opened against the parent branch — survives adversarial review intact. Its data model (flatten onto `db.Job`) is the correct MVP seam, with one ownership amendment now that `agent_runs` exists. The plan is honestly small, but it is not as low-risk as it claims in two places it never mentions: **auto-merge would squash a stacked child PR into its parent branch** (§7, A-1), and **a swallowed issue-body fetch error silently drops a declaration forever** (§7, A-2). Both are cheap to fix and must be in scope. Every version number and sequencing note in the plan needs re-baselining (§2).

---

## 1. Assumption audit — every schema/version and code-location claim, verified

Per the issue's instruction, all assumptions were verified, not just the obvious stale number.

| Plan assumption | Status at `b96d79d` | Evidence |
|---|---|---|
| Baseline `bfbc705`, schema v9 | **Stale.** Schema is v11; next free slot is **v12** | `internal/db/schema.go:13`; Expedition I invariant §6.4 |
| "PR #578 for #571 is adding provenance as v10" | Landed. `source_type/source_name/source_ref` exist on `jobs`; written only by trigger activation | `internal/db/queries.go:83`, `internal/supervisor/watch.go:267-269`; Expedition I C5 |
| "Integration-target migration should land after it as v11" | **Stale.** v11 was consumed by `agent_runs` (#583). Target migration is **v11→v12** | `schema.go:393-417`; Expedition I C3 |
| "Serialize `cmd/deploy.go` edits if #569 lands first" | **Superseded.** #569 (Coordinator extraction) landed; `internal/coordinator` exists. The plan's claim that target parsing/persistence/resolution/worktree setup sit *below* the Coordinator boundary holds — the affected files are supervisor/git/db-level | `cmd/deploy.go:332,445` construct the Coordinator; target seams are in `internal/supervisor` |
| Trigger routes carry agent/runtime/model/budget/turn overrides | Correct | `watch.go:25-34`, `watch.go:257-260` |
| Issue body stored, nothing parses runtime metadata from it | Correct | `watch.go:243-247`, `internal/db/models.go:52` |
| `knownJobs` keyed by (issue number, agent) | Correct | `watch.go:99-108` |
| Reactive branch hardcoded `agent/issue-<N>` in `newSlotContext` | Correct — and proactive jobs use `agent/<job-name>` from the same function | `internal/supervisor/jobmanager.go:316-323` |
| `SetupWorktree` removes any worktree/local branch using the child branch before recreating | Correct — `WorktreeRemoveByBranch` + `DeleteBranch` + `WorktreeAdd` under `gitSetupMu` | `jobmanager.go:130-137` |
| `fillCapacity` fetches but does not gate launch on fetch success | Correct — `tryFetch` with offline backoff, result ignored for dispatch | `internal/supervisor/supervisor.go:554-566`, `supervisor.go:585-624` |
| Base branch resolved onboarding → deployment → git default → `main`; same string used for worktree start, rebase instructions, and PR base | Correct — risk 7 of Expedition I confirmed live | `internal/supervisor/prompt.go:166-181`, `jobmanager.go:136`, `context.go:285-288` |
| `Prepare` is the targeted path, watcher uses `createJobForIssue` | Correct | `internal/supervisor/depgraph.go:33-72`, `watch.go:242` |
| Daemon re-exec doesn't need the flag: jobs persisted before re-exec | Correct — `Prepare` at `cmd/deploy.go:213-218` precedes daemonize at `cmd/deploy.go:258-286` |
| "Reuse the supervisor's existing offline backoff" for network failures | Exists and reusable | `supervisor.go:572-624`, `internal/git/git.go:284` (`IsNetworkError`) |

Two code facts the plan relies on implicitly but never states, both confirmed:

- **`blocked` is a soft status.** `QueuedUnblockedJobs` considers both `queued` and `blocked` rows and recomputes dependency eligibility from the `dependencies` column every cycle (`queries.go:261-296`); `blocked` is only ever *written* by dep-graph application (`depgraph.go:125`). Status is a display value; eligibility is derived from fields. This is exactly what makes the plan's reuse of `blocked` workable (§5).
- **A dependency counts as satisfied once the dependee reaches `review`** — PR open, unmerged (`queries.go:285-286`). This is the moment `origin/agent/issue-<A>` exists. Dependency timing and target availability converge naturally; the plan gets this benefit without ever claiming it.

---

## 2. Re-baselining amendments (mechanical, must happen before issue-writing)

1. Migration is **v11→v12**. Update the plan's Planning-baseline and Data-model sections; `CLAUDE.md` schema line moves to v12 in the same PR (`TestClaudeMDSchemaVersion` enforces it).
2. Drop the #569 sequencing caution; replace with: serialize against whichever v12+ migration is in flight (Expedition I's single-file migration queue) and against the M1-13/16 supervisor edits for the `SlotContext` split (Expedition I risk 7 sequencing rule).
3. The plan's issue 2 must add the target columns to **both** `CreateJob` and `BulkCreateJobs`. `BulkCreateJobs` already silently omits `max_turns`, `max_budget_usd`, and all three provenance columns (`queries.go:96-108`, Expedition I C9). Landing target persistence through only one path would spring the same trap a third time. Preferably unify the two inserts while in there.
4. `Prepare` writes no provenance today (`depgraph.go:55-72`). Expedition I DM-1 (provenance completion) should land before or with the plan's issue 2 so "trigger/job provenance independently from target source" is actually true, not aspirational.

---

## 3. Three viable domain designs

### Design A — Flatten onto `db.Job` (the plan's proposal)

Seven nullable columns on `jobs`: declaration (`integration_target_source`, `requested_integration_target`), resolution (`resolved_base_ref`, `resolved_base_sha`, `pull_request_base_branch`, `integration_target_resolved_at`), retry (`integration_target_retry_at`).

- For: `db.Job` is already the carrier from activation through `SlotContext` to worktree creation; every consumer (dispatch eligibility, status, daemon API, `minder status --json`) reads job rows today. No joins, no new table, additive migration. Matches Expedition I invariant §6.6: `jobs` is the backward-compatible aggregate.
- Against: mutable aggregate — a re-resolution or reset overwrites the previous resolution; per-attempt history is lost. Seven columns is wide for a feature most jobs won't use.

### Design B — Declaration on `jobs`, resolution on `agent_runs`

Declaration + retry state on `jobs`; the resolved ref/SHA recorded per attempt on `agent_runs` (additive `base_ref`/`base_sha` columns), since the SHA a worktree was created from is an attribute of *that execution*.

- For: honors the v11 split — `jobs` mutable aggregate, `agent_runs` immutable per-attempt truth (Expedition I §6.6). Retries/resets keep full provenance ("attempt 1 ran from SHA X, attempt 2 from SHA Y") for free.
- Against: **resolution temporally precedes the run row.** `beginAgentRun` fires at stage start, after worktree setup (`jobmanager.go:365-390`); resolution must complete before `UpdateJobRunning`, and a target-blocked job has no run at all. So `jobs` still needs current-resolution columns for dispatch gating and blocked-state display — B adds the run columns *in addition to*, not instead of, most of A. Also `agent_runs` writes are deliberately best-effort ("never fails the stage"), the wrong durability class for data that gates dispatch.

### Design C — Separate `integration_targets` table

One row per job (PK `job_id`) holding declaration + resolution + retry; `jobs` untouched.

- For: cleanest separation; `jobs` stays narrow; a future 1:N shape (multiple resolutions, resolver kinds `issue:`/`pr:`/`commit:`) has a home; no-target jobs cost nothing.
- Against: every consumer joins — `QueuedUnblockedJobs`, dispatch, status, daemon API, reset, crash recovery. `ResetJob` and `TransitionStaleRunningJobs` become two-table transactions. For a branch-only MVP with at most one declaration and one live resolution per job, the table earns nothing yet; it is infrastructure for resolvers that are explicitly out of scope.

### Recommendation

**Design A for the MVP, with B's run-provenance as a fast-follow, and C as the named escape when a second resolver kind lands.** Confidence: high.

Amendments to A as specced:

- Define the resolution columns as **latest-resolution scratch owned by the dispatch phase**: written immediately before `UpdateJobRunning`, cleared by `ResetJob`, retained by crash recovery (§6). Do not present them as history — they aren't.
- When `agent_runs` columns are next touched for any reason, add `base_ref`/`base_sha` written at `beginAgentRun` from the job's current resolution. Cheap, additive, restores per-attempt truth. Not MVP-blocking.
- Revisit trigger for C: the first accepted `issue:`/`pr:`/`commit:` resolver, or any need for more than one live resolution per job.

**Ownership answers to the issue's questions:** `db.Job` is the right owner for requested *and* currently-resolved target data (it is the only object that exists at both activation and dispatch, and the only one dispatch gating reads). Declaration and resolution should be *separate column groups with separate lifecycles* on that row — declaration immutable after activation, resolution dispatch-owned and clearable — not separate tables. Attempt provenance belongs on `agent_runs` and can trail the MVP.

---

## 4. Scheduling dependency vs. source-control ancestry

Cleanly separated, and correctly so. `dependencies` (issue numbers, evaluated by `QueuedUnblockedJobs`) answers *when may this job start*; the integration target answers *what commit does it start from and where does its PR point*. The plan never infers one from the other — no target derived from dep edges, no dep derived from a declared target — and that restraint should be kept: the two compose through timing alone (§1, review-counts-as-satisfied).

One semantic shift to document, not fix: for a targeted child, `done` means *merged into the parent branch*, not merged into main. `checkMergedPRs` marks a job done when GitHub reports its PR merged (`supervisor.go:734-738`), and downstream dependencies then treat it satisfied (`queries.go:285-286`). A chain A←B←C can complete with nothing in main until A's PR merges. That is coherent stacked-PR semantics, but `minder status` operators should be told that `done` is PR-relative.

---

## 5. Minimal state machine

Is reusing `blocked` for dependency, ref, network, access, and branch-ownership states coherent? **Yes, under one rule the plan implies but must state as an invariant: `status` is never the eligibility source of truth — eligibility is recomputed from persisted fields every cycle.** Dependency-blocked derives from `dependencies`; target-blocked derives from `failure_reason` + `integration_target_retry_at`. `blocked` remains what it is today: a display aggregate over "not eligible right now, will be reconsidered without human action" (`queries.go:261`, `supervisor.go:514`). Splitting statuses per cause would multiply every status switch in supervisor, daemon, and CLI for no eligibility gain. The causes stay distinguishable through `failure_reason`, which the plan already requires.

### Transition table (new/changed transitions only; existing lifecycle unchanged)

| From | Event / guard | To | Fields written |
|---|---|---|---|
| (activation) | body parsed, valid declaration | `queued` | declaration source + canonical value |
| (activation) | body parsed, no `## Agent Minder` section | `queued` | none (all target fields NULL) |
| (activation) | malformed/duplicate/unsupported declaration | `failed` | `failure_reason=integration_target_invalid`, raw value retained |
| (activation) | issue-body fetch failed | do **not** create the job; retry next poll (§7, A-2) | — |
| `queued`/`blocked` | deps satisfied ∧ slot free ∧ no target | `running` | (existing path, byte-identical) |
| `queued`/`blocked` | deps satisfied ∧ slot free ∧ target ∧ (`retry_at` NULL or due) → exact fetch + peel succeeds | `running` | `resolved_base_ref`, `resolved_base_sha`, `pull_request_base_branch`, `resolved_at`; clear `retry_at`; atomic child-branch claim in the same transaction as `UpdateJobRunning` |
| same | ref absent on origin | `blocked` | `failure_reason=integration_target_ref_unavailable`, `retry_at=now+2m` |
| same | network failure (`IsNetworkError`) | `blocked` | `failure_reason=integration_target_fetch_failed`, `retry_at` from offline backoff |
| same | other fetch failure | `blocked` | `failure_reason=integration_target_fetch_failed`, `retry_at=now+5m` |
| same | target == child branch | `failed` | `failure_reason=integration_target_conflict` (terminal, before any cleanup) |
| same | child branch owned by an active job (`running`/`reviewing`/`waiting`/**`review`/`reviewed`**) in same owner/repo | `blocked` | `failure_reason=branch_in_use`, `retry_at=now+2m` |
| `running` | worktree HEAD ≠ pinned SHA after creation | `failed` | `failure_reason=integration_target_pin_mismatch` *(new — the plan requires the verification but names no outcome)* |
| `review`→ | detected PR base ≠ `pull_request_base_branch` | `manual` | `failure_reason=pr_base_mismatch` (via the existing `finalizeManual` pattern, `jobmanager.go:1172-1194`) |

Adversarial corrections embedded above, argued in §7: the branch-ownership set must include `review`/`reviewed` (A-4); `integration_target_access_denied` is dropped for the MVP (A-6); activation-time fetch failure must not create a declaration-less job (A-2).

Two mechanical notes for the implementer:

- **`failed` needs a reason-setting helper.** Today `UpdateJobFailure` hardcodes `status='bailed'` (`queries.go:183-188`); pre-run terminal failures currently go through `finalizeFailure`'s two-step write (`jobmanager.go:1162-1163`), which assumes a `SlotContext` that a never-dispatched job doesn't have. Small new store method, not a design problem.
- **`hasWork` already keeps blocked jobs alive** (`supervisor.go:514`), so a foreground deployment waits on a blocked target indefinitely with no timeout. Correct per the plan's "remain visibly blocked", but it must be in the operator docs: a permanently missing ref pins a foreground process open until Ctrl-C.

---

## 6. Crash, reset, retry, multi-deployment, multi-host semantics

**Crash.** `TransitionStaleRunningJobs` moves `running`→`queued`, clearing only `started_at` (`queries.go:235-242`); the plan's target columns would survive, which is what its "crash recovery retains a previously resolved SHA" rule needs. But note the tension the plan doesn't acknowledge: its own flow resolves *at dispatch*, so retention only matters if the pre-dispatch phase explicitly skips re-resolution when `resolved_base_sha` is set. And the value of retention is thin: recovery re-runs `SetupWorktree`, which deletes the branch and recreates the worktree from scratch (`jobmanager.go:130-137`) — the attempt's work is discarded either way, so re-resolving to a fresher parent commit would be equally defensible and one conditional simpler. **Recommendation: re-resolve on crash recovery (drop the retention rule); pin-per-attempt, not pin-per-job-lifetime.** The determinism the plan wants is per-attempt determinism, and that is fully preserved. Low confidence this matters much either way; pick one and write the test. If retention is kept, the skip-if-resolved guard must be explicit in the issue text.

**Reset.** `ResetJob` today clears runtime state but knows nothing of the new columns (`queries.go:219-226`). The plan's rule — clear resolution/PR-base/retry, keep declaration/source — is right and matches the two-lifecycle ownership model (§3). The plan's issue 2 correctly lists reset behavior in scope; the test must assert both halves (declaration survives, resolution doesn't).

**Retry.** The retry loop is the supervisor's existing 2-second `fillCapacity` cadence gated by `integration_target_retry_at`. Two implementation constraints the plan leaves unstated, both load-bearing:

1. **`QueuedUnblockedJobs` must exclude target-blocked jobs whose `retry_at` is in the future.** `fillCapacity` loops `jobs[0]` until the eligible list empties (`supervisor.go:561-567`); if a job fails resolution, stays in the list, and is retried in the same loop iteration, that is a busy-loop hammering `git fetch` — or an infinite loop if the failure is deterministic. The eligibility query, not the dispatch code, is where the gate belongs.
2. **Resolution must not run under `s.mu`.** `fillCapacity` holds the supervisor mutex; `launchJob` marks running and registers the slot synchronously (`supervisor.go:662-675`). A per-job exact fetch is a network call; doing it under the lock stalls the whole supervisor (event emission, status, watch ticks). The established pattern is `tryFetch`: unlock, fetch, relock, re-check (`supervisor.go:554-559`). The plan's two constraints — "blocked before `UpdateJobRunning`" and "consumes no slot" — are satisfiable only with this shape: resolve outside the lock, then atomically claim-and-run.

**Multi-deployment, one host.** All deployments share `~/.agent-minder/v2.db`; cross-process safety is WAL + `busy_timeout`, in-process is single-connection (Expedition II I-7, `schema.go:438-445`). The plan's atomic claim transaction (check active jobs in same owner/repo holding the branch, then claim + mark running in one transaction) is sound within this database, including across concurrent worker processes. It also fixes a live pre-existing hazard: two agents on the same issue with different agents (`knownJobs` permits spike + autopilot, `watch.go:99-108`) both derive `agent/issue-<N>` (`jobmanager.go:316-319`), and today the second `SetupWorktree` silently deletes the first's worktree and branch. Under the plan the second blocks with `branch_in_use` — strictly better, and worth an explicit regression test.

**Multi-host.** Two hosts running minder against the same GitHub repo share nothing: no common SQLite, no claim transaction, no `SetupWorktree` mutex. Both can create `agent/issue-102`, resolve different parent SHAs, and race the push. The only global arbiter is origin's fast-forward rule at push time, and that protects nothing if an agent force-pushes (agent behavior is guidance, not enforcement). **No MVP mechanism can close this; it must be a documented limitation** — the honest statement is "collision guarantees hold per host database; cross-host coordination is out of scope." Same for the dedup engine's `branch_exists` strategy: it helps, but it is advisory, not atomic.

---

## 7. Adversarial findings — gaps the plan must absorb

**A-1. Auto-merge merges the child into the parent branch. Severity: high; not mentioned in the plan.** `finalizePipeline` enables GitHub auto-merge for any low-risk PR (`jobmanager.go:1088-1094`) with no awareness of the PR's base. A low-risk child PR based on `agent/issue-101` would auto-merge into the *parent's in-flight branch* when CI passes, contaminating the parent's PR with the child's diff — silently, with an "Auto-merge enabled" info event. The MVP must gate auto-merge off whenever `pull_request_base_branch` is set and differs from the deployment's default base. One conditional; belongs in the plan's issue 4.

**A-2. Activation swallows issue-body fetch failures, silently dropping declarations. Severity: high; not mentioned.** `createJobForIssue` ignores the `FetchItemContent` error and proceeds with an empty body (`watch.go:243-247`); `Prepare` does the same (`depgraph.go:49-53`). Under the plan's snapshot rule ("issue body changes while queued: job keeps its activation-time snapshot; no silent retargeting"), a transient GitHub error at activation permanently converts a declared-target issue into a default-base job — the exact "fall back to default" outcome the plan's parser rules forbid, arriving through a side door. Fix: on content-fetch failure, don't create the job; let the next watch poll retry (`knownJobs` only records created jobs, so retry is automatic). For `Prepare`, fail the deploy command. Belongs in the plan's issue 2 acceptance criteria.

**A-3. `pr_base_mismatch` is a one-shot check, but GitHub retargets PRs unilaterally. Severity: medium; partially covered.** When a parent PR merges and its branch is deleted, GitHub automatically retargets open child PRs to the parent's base — and after a squash merge the child's diff then re-includes the parent's changes. The plan's post-detection base verification catches an agent opening the PR wrong; it cannot catch a later retarget. Re-checking continuously is scope creep; instead the limitation must be documented ("base verification is at detection time; parent merge + branch deletion retargets the child on GitHub's side, and the child PR diff should be re-reviewed") and the rollout experiment should demonstrate it. The existing `review`/`reviewed` PR-status polling loop is the natural future home for a re-check — defer, don't build.

**A-4. The branch-ownership claim set omits `review` and `reviewed`. Severity: medium.** The plan claims against `running`, `reviewing`, `waiting`. A job in `review` or `reviewed` has an open PR whose head *is* its branch (`supervisor.go:713-738` polls these for merge). A new job claiming that branch — today's `SetupWorktree` would delete and recreate it — yanks the branch out from under an open PR. The claim predicate should be "any non-terminal status other than `queued`/`blocked`": `running`, `review`, `reviewing`, `reviewed`, `waiting`, `manual` (a `manual` job's pushed branch is likewise live). Terminal states (`done`, `bailed`, `failed`, `stopped`, `skipped`) release ownership.

**A-5. Prompt changes must cover the reviewer stage too.** Rebase-onto-base instructions are generated in *both* `renderCommands` (`context.go:285-288`) and `renderReviewContext` (`context.go:345-347`), and the review context prints `**Base branch:**` from the same conflated field (`context.go:305`). The plan's issue 4 says "update repo and review context" — make the reviewer's rebase-instruction removal and correct PR-base display explicit acceptance criteria, or the reviewer will instruct a rebase onto `main` for a child based on `agent/issue-101`.

**A-6. `integration_target_access_denied` should be cut from the MVP.** The plan itself concedes Git error text varies by transport and classification is brittle (its issue 3 risk note). Distinguishing auth failure from other fetch failure buys one thing — a 15-minute retry interval instead of 5 — at the cost of a fragile classifier over stderr text. Collapse it into `integration_target_fetch_failed` with the standard backoff; `failure_detail` retains the sanitized error for the operator either way. Reintroduce the category only if real-world logs show credential outages thrashing the retry loop.

**A-7. Observability additions must not pre-freeze event shapes.** The plan's event list (discovery, blocked, resolution, start, PR-base summaries) lands while `supervisor.Event` is still `{Type string, Summary string}` prose (Expedition I C10, DM-2). Emit the summaries — they die with the process — but do not add target-specific structured event types or persist any of this before the typed envelope (Expedition IV / DM-2) exists. Persisted job columns are the authoritative record; the plan's own issue 5 risk note already says this and it should be promoted to a constraint.

---

## 8. Enforceable guarantees vs. documented limitations

**Enforceable (test these):**

- A declared target is never silently ignored: valid → pinned dispatch; invalid → terminal `failed`; unavailable → visible `blocked`. (Requires A-2's fix to be true.)
- The child worktree's initial HEAD equals `resolved_base_sha`, even if origin's parent branch has advanced (worktree created from SHA, verified before agent start).
- Target-blocked jobs consume no slot, budget, worktree, or `in-progress` label (blocking precedes `UpdateJobRunning` and slot registration).
- Within one host database: at most one non-terminal job owns a child branch (claim transaction, per A-4's status set); no active job's worktree is removed by another's setup.
- No-target jobs follow the existing path with NULL target fields — regression-tested to byte-identical prompts and commands.
- The parent branch is never checked out, committed to, rebased, or force-pushed by minder or its generated instructions.

**Documented limitations (do not pretend to enforce):**

- Agents are guided, not sandboxed: a PR opened with the wrong base is *detected* (`manual`, one-shot at detection time) — never prevented or auto-corrected. Later GitHub-side retargeting is invisible (A-3).
- Cross-host: all collision guarantees are per host database; origin's push rules are the only global arbiter and are advisory against force-push (§6).
- "Branch not yet created" and "branch permanently wrong/never coming" are indistinguishable; a blocked job waits until the operator intervenes.
- Parent branch deletion after resolution: the pinned child is untouched (the SHA is in the local object store), but PR creation against a deleted base fails into the manual path.
- `done` for a targeted child means merged-into-parent, not merged-into-main (§4).

---

## 9. Fit with the Coordinator/event/API direction

Fits. The feature's write sites (`createJobForIssue`, `Prepare`, the pre-dispatch phase, `SetupWorktree`) and its read sites (dispatch eligibility, status) are all below the Coordinator boundary; nothing touches Coordinator assembly, jobs.yaml ownership (no YAML change — correct, agreed), or the process topology (Expedition II's invariants are untouched; the claim transaction is exactly the I-7-sanctioned cross-process channel). API/status additions are additive nullable fields on existing job-shaped responses, which is compatible with the legacy daemon routes now and with `/api/v1`'s job resource later; the DM-5 provider interface would carry them without change. The one interaction requiring discipline is events (A-7). Expedition I's revisit note on DM-1 ("if Expedition III changes activation flow, re-check the write sites") resolves to: activation gains a parse step but no new write site; the pre-dispatch phase adds resolution writes to `jobs` only. Provenance taxonomy is unaffected.

---

## 10. Honestly small? The smallest safe MVP

The plan is honest about scope — its out-of-scope list is long and correct, and its five issues are real vertical slices. It is *not* minimal. The smallest safe MVP that still delivers the rollout experiment end-to-end:

**Keep (MVP):** issues 1–4 with the amendments above — parser package; persistence in both activation paths + v12 migration + reset semantics + A-2; resolution/gating/claim + retry eligibility + A-6's simplification; pinned worktree + PR-base split + A-1 auto-merge gate + A-5 reviewer prompts + one-shot base verification.

**Defer from the MVP (cut from the plan's scope):**

- **`--integration-target` CLI override (from issue 2).** It is the plan's own "secondary … recovery tool"; the unattended issue declaration is the product. The CLI flag brings precedence rules, override warnings, cardinality validation, `cli_override` provenance, and `cmd/deploy.go` churn in a file the control-plane work also wants. The recovery story without it — edit the issue body, reset or re-deploy — is adequate for a two-issue experiment. Ship it as its own follow-up issue once the vertical slice works. (This is the review's largest disagreement with the plan; see §12.)
- **Daemon API/client response-type additions (from issue 5).** `minder status --json` already serializes job rows, so the new columns surface there nearly for free; the daemon client structs and human-status formatting can trail by one issue without blocking the experiment.
- `integration_target_access_denied` (A-6).
- Per-attempt `agent_runs` base provenance (§3) — fast-follow.

**Keep in issue 5 (not deferrable):** the end-to-end behavioral test (discovery → blocked → resolution → pinned worktree → PR base) and the documentation of §8's limitations. A guarantees section that exists only in this review is not a shipped deliverable.

Risk assessment: with A-1 and A-2 fixed, "reasonably low-risk" is fair. The two nontrivial seams the plan names (pre-dispatch retry gating, `SlotContext` split) are confirmed as the real ones; §6's two retry-loop constraints are where an implementer would most plausibly ship a subtle bug.

---

## 11. Cheaper-agent handoff

### Fixed decisions (do not re-litigate)

- Declaration syntax: one `Integration target: branch:<name>` field in one `## Agent Minder` section; strict parse; no fallback-to-default on malformed input; no natural-language inference.
- Temporal model: snapshot at activation, resolve-and-pin at dispatch (after dependency readiness), child from SHA, PR against parent branch, no auto-rebase/restack of targeted children.
- Data model: Design A on `jobs` (§3), migration **v11→v12**, declaration immutable / resolution dispatch-owned lifecycles; `ResetJob` clears resolution, keeps declaration.
- `blocked` reuse with field-derived eligibility (§5), including the `retry_at` gate inside `QueuedUnblockedJobs`.
- Claim set per A-4; auto-merge gated per A-1; activation fetch failure per A-2; no `access_denied` category (A-6).
- No jobs YAML change; no proactive/cron targets; everything in the plan's out-of-scope list stays out.

### Open decisions and owners

- Crash recovery: retain-SHA vs re-resolve (§6 recommends re-resolve; either is safe — the implementing issue must pick one and test it). Owner: implementer of issue 3, one-line decision in the PR body.
- CLI override scope and timing → follow-up issue after the vertical slice (this review) vs in-MVP (the plan). Owner: Dustin. §12.
- Continuous PR-base re-verification → deferred to the PR-status polling loop, post-MVP (A-3).
- Per-attempt base provenance on `agent_runs` → fast-follow, sequenced with any other `agent_runs` change.

### Sequencing and prerequisites

1. Parser package (plan issue 1) — dependency-free, autopilot-safe, no migration.
2. DM-1 provenance completion + `CreateJob`/`BulkCreateJobs` unification (Expedition I, issue boundary 1) — before or fused with step 3.
3. Persistence + v12 migration (plan issue 2, amended: A-2, reset tests, both insert paths, CLI cut). **Owns the v12 queue slot; nothing else migrates concurrently; `CLAUDE.md` updated in the same PR.**
4. Resolution + gating + claim (plan issue 3, amended: §6's two constraints, A-4, A-6). Not autopilot-safe — supervisor-lock choreography; solo/interactive per the established dispatch rule.
5. Pinned worktree + PR semantics (plan issue 4, amended: A-1, A-5, pin-mismatch outcome). Must not run concurrently with M1-13/16 supervisor edits (Expedition I risk 7).
6. Behavioral vertical + docs (plan issue 5, trimmed per §10).

### Likely traps and prohibited shortcuts

1. Adding target columns to `CreateJob` but not `BulkCreateJobs` (C9's trap, third spring).
2. Gating retries in dispatch code instead of `QueuedUnblockedJobs` — produces the `fillCapacity` busy/infinite loop (§6).
3. Resolving (network fetch) while holding `s.mu` — stalls the supervisor (§6).
4. Falling back to the default base on any parse or resolution failure "to be helpful." Prohibited by the plan and this review; the failure modes are `failed` and `blocked`, never silent default.
5. Using `BranchExists` (local, `git/git.go:344`) or any local ref to satisfy a declaration — only the freshly fetched `refs/remotes/origin/<branch>` counts.
6. Enabling auto-merge, or leaving it reachable, for any PR whose base isn't the deployment default (A-1).
7. Interpolating unvalidated branch input into git/gh commands — validate with git's branch-name rules first, use fully qualified refspecs (plan rule, reaffirmed).
8. Editing an existing migration constant, batching two schema bumps, or "fixing" `TestClaudeMDSchemaVersion`.
9. Touching no-target job prompts/commands — golden tests must prove byte-identical output.
10. Persisting or structuring new event types ahead of DM-2 (A-7).

### Verification checklist

- [ ] Parser: table-driven cases per plan issue 1 (absent section, valid, duplicates, option-like values, `origin/` names, SHAs, full refs, slashes/dots/hyphens).
- [ ] Migration: fresh-create and v11→v12 both tested; existing rows get NULLs; `CLAUDE.md` v12.
- [ ] Same issue body yields identical canonical metadata via watch and via `Prepare`.
- [ ] Activation with failing `FetchItemContent` creates no job; next poll retries (A-2).
- [ ] `ResetJob` clears resolution/retry, keeps declaration; crash recovery behaves per the recorded §6 decision.
- [ ] Bare-origin fixture: missing ref → blocked with due `retry_at`, no slot consumed, no worktree, no label; branch appears → next cycle resolves, pins full SHA before `running`.
- [ ] Deterministic resolution failure does not busy-loop `fillCapacity` (assert fetch-call count over N ticks).
- [ ] Concurrent claim: same child branch, two jobs → one runs, one `branch_in_use`; claim respected for a job in `review` (A-4); active worktree never removed.
- [ ] Worktree HEAD == pinned SHA even after parent advances post-resolution; mismatch → `failed`/`integration_target_pin_mismatch`.
- [ ] Targeted job prompts: no rebase instructions in doer *and* reviewer contexts; `gh pr create --draft --base <parent>`; no-target prompts byte-identical to today.
- [ ] Auto-merge not enabled for a low-risk PR with non-default base (A-1).
- [ ] Wrong actual PR base → `manual`/`pr_base_mismatch`, PR untouched.
- [ ] Rollout experiment (plan's two-issue script) passes with the documented limitations observed, including `done`-is-PR-relative (§4).
- [ ] `go test ./...` green; green anchors re-run for any supervisor/deploy edit.

---

## 12. Unresolved disagreements

- **CLI override in or out of the MVP.** The plan argues it in as a recovery tool restricted to one issue; this review argues it out (§10) because issue-body editing covers recovery, and the flag's precedence/provenance/validation surface is a quarter of issue 2's complexity in a contended file. Both are defensible; deferring is reversible, shipping is not. Dustin decides at issue-filing time.
- **Crash recovery: retain vs re-resolve** (§6). This review recommends re-resolve as simpler and equally deterministic per attempt; the plan says retain. Genuinely low-stakes; flagged so it gets decided explicitly rather than emerging from whichever branch the implementer writes first.
- **Design B's timing objection** (§3) rests on reading `beginAgentRun`'s call ordering, not on executing a targeted job *(inference)*. If a future refactor moves run-row creation ahead of worktree setup, B's case strengthens and the fast-follow should be revisited then.
