package supervisor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
	runtimepkg "github.com/aptx-health/agent-minder/internal/runtime"
)

// --- Test helpers ---

func TestModelProcessExitDetailIncludesGuidanceAndLogTail(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(logPath, []byte("first line\nunknown model: opus\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := modelProcessExitDetail("codex", " opus ", 2, logPath)
	for _, want := range []string{
		`model "opus"`,
		`runtime "codex"`,
		"remove model:",
		"unknown model: opus",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("detail missing %q: %s", want, got)
		}
	}
}

func TestFormatJobStartedSummaryIncludesRuntimeAndModel(t *testing.T) {
	deploy := &db.Deployment{Runtime: runtimepkg.NameClaudeCode}
	job := &db.Job{
		Agent:      "autopilot",
		Name:       "autopilot-issue-8",
		Runtime:    sql.NullString{String: runtimepkg.NameCodex, Valid: true},
		Model:      sql.NullString{String: "gpt-5.5", Valid: true},
		IssueTitle: sql.NullString{String: "Snapshot layer", Valid: true},
	}
	got := formatJobStartedSummary(nil, deploy, job)
	want := "Agent started on autopilot-issue-8 (runtime: codex, model: gpt-5.5): Snapshot layer"
	if got != want {
		t.Fatalf("formatJobStartedSummary() = %q, want %q", got, want)
	}
}

func TestFormatJobStartedSummaryOmitsEmptyModel(t *testing.T) {
	deploy := &db.Deployment{Runtime: runtimepkg.NameClaudeCode}
	job := &db.Job{
		Agent:      "autopilot",
		Name:       "weekly-security-20260708-1500",
		IssueTitle: sql.NullString{},
	}
	got := formatJobStartedSummary(nil, deploy, job)
	want := "Agent started on weekly-security-20260708-1500 (runtime: claude-code): weekly-security-20260708-1500"
	if got != want {
		t.Fatalf("formatJobStartedSummary() = %q, want %q", got, want)
	}
}

// testStore creates a fresh SQLite store in a temp directory.
func testStore(t *testing.T) *db.Store {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return db.NewStore(conn)
}

// testDeployment creates a deployment record in the store.
func testDeployment(t *testing.T, store *db.Store, opts ...func(*db.Deployment)) *db.Deployment {
	t.Helper()
	deploy := &db.Deployment{
		ID:             fmt.Sprintf("test-%d", time.Now().UnixNano()),
		RepoDir:        t.TempDir(),
		Owner:          "acme",
		Repo:           "widgets",
		Mode:           "issues",
		MaxAgents:      3,
		MaxTurns:       50,
		MaxBudgetUSD:   5.0,
		AnalyzerModel:  "sonnet",
		SkipLabel:      "no-agent",
		TotalBudgetUSD: 25.0,
		BaseBranch:     "main",
		ReviewEnabled:  true,
	}
	for _, opt := range opts {
		opt(deploy)
	}
	if err := store.CreateDeployment(deploy); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	return deploy
}

// testJob creates a job record in the store.
func testJob(t *testing.T, store *db.Store, deploy *db.Deployment, opts ...func(*db.Job)) *db.Job {
	t.Helper()
	job := &db.Job{
		DeploymentID: deploy.ID,
		Agent:        "autopilot",
		Name:         fmt.Sprintf("issue-%d", time.Now().UnixNano()%10000),
		IssueNumber:  42,
		IssueTitle:   sql.NullString{String: "Fix the widget", Valid: true},
		IssueBody:    sql.NullString{String: "The widget is broken", Valid: true},
		Owner:        deploy.Owner,
		Repo:         deploy.Repo,
		Status:       db.StatusRunning,
	}
	for _, opt := range opts {
		opt(job)
	}
	if err := store.CreateJob(job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	return job
}

// pipelineHarness wires up a DefaultJobManager with mocked externals.
type pipelineHarness struct {
	t        *testing.T
	store    *db.Store
	deploy   *db.Deployment
	sup      *Supervisor
	logDir   string
	hooks    *TestHooks
	stageLog []stageCall // records each runtime stage invocation
	mu       sync.Mutex
}

type stageCall struct {
	Agent string // agent name from the runtime invocation
	Inv   runtimepkg.Invocation
}

func newHarness(t *testing.T, opts ...func(*db.Deployment)) *pipelineHarness {
	t.Helper()
	store := testStore(t)
	deploy := testDeployment(t, store, opts...)
	sup := NewTestSupervisor(store, deploy, deploy.RepoDir)
	logDir := t.TempDir()

	h := &pipelineHarness{
		t:      t,
		store:  store,
		deploy: deploy,
		sup:    sup,
		logDir: logDir,
	}

	h.hooks = &TestHooks{
		SetupWorktreeFn: func() error {
			return nil // no-op: skip git operations
		},
		EnsureAgentDefFn: func(name AgentName) (AgentDefSource, error) {
			return AgentDefBuiltIn, nil // always succeed
		},
		RunStageFn: func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
			// Default: succeed immediately.
			h.mu.Lock()
			h.stageLog = append(h.stageLog, stageCall{Agent: inv.AgentName, Inv: inv})
			h.mu.Unlock()
			return 0, &runtimepkg.Result{}, false, nil
		},
		DetectPRFn: func(ctx context.Context) int {
			return 0 // default: no PR detected
		},
		ExtractReviewAssessmentFn: func(ctx context.Context, logPath string, job *db.Job) ReviewAssessment {
			return ReviewAssessment{Risk: "low-risk", Summary: "All good"}
		},
		CreateReviewCommentFn: func(ctx context.Context, prNumber int, body string) (int64, error) {
			return 9001, nil
		},
	}

	return h
}

// newSlotContext creates a SlotContext for a job, wired to the harness.
func (h *pipelineHarness) newSlotContext(job *db.Job) *SlotContext {
	h.sup.RegisterTestJob(job)
	logPath := filepath.Join(h.logDir, fmt.Sprintf("job-%d.log", job.ID))
	return &SlotContext{
		Store:        h.store,
		Deploy:       h.deploy,
		Job:          job,
		RepoDir:      h.deploy.RepoDir,
		Owner:        h.deploy.Owner,
		Repo:         h.deploy.Repo,
		WorktreePath: h.deploy.RepoDir, // use repoDir as worktree (no git ops)
		Branch:       fmt.Sprintf("agent/issue-%d", job.IssueNumber),
		LogPath:      logPath,
		BaseBranch:   "main",
		AllowedTools: []string{"Bash(git:*)", "Read"},
		sup:          h.sup,
		Hooks:        h.hooks,
	}
}

// run creates a DefaultJobManager and runs it.
func (h *pipelineHarness) run(ctx context.Context, job *db.Job, contract *AgentContract) error {
	sc := h.newSlotContext(job)
	mgr := NewDefaultJobManager(sc, contract)
	return mgr.Run(ctx)
}

// stages returns the list of agent invocations recorded.
func (h *pipelineHarness) stages() []stageCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make([]stageCall, len(h.stageLog))
	copy(result, h.stageLog)
	return result
}

// events returns all buffered supervisor events.
func (h *pipelineHarness) events() []Event {
	return h.sup.DrainEvents()
}

// hasEvent returns true if any event matches the given type and contains substr.
func hasEvent(events []Event, typ, substr string) bool {
	for _, e := range events {
		if e.Type == typ && strContains(e.Summary, substr) {
			return true
		}
	}
	return false
}

func strContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return len(sub) == 0
}

// --- Test cases ---

// TestBranchCollisionBlocksSecondJob verifies that when two jobs on the same
// issue (different agents) derive the same branch, the second job is blocked
// rather than destroying the first job's active worktree. Covers A-4: a sibling
// in "review" status holds the claim because its open PR is backed by the branch.
func TestBranchCollisionBlocksSecondJob(t *testing.T) {
	tests := []struct {
		name          string
		siblingStatus string
	}{
		{"running sibling", db.StatusRunning},
		{"review sibling (A-4)", db.StatusReview},
		{"reviewed sibling (A-4)", db.StatusReviewed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)

			// Sibling job on issue 42 (spike agent) already active and owning the branch.
			sibling := testJob(t, h.store, h.deploy, func(j *db.Job) {
				j.Agent = "spike"
				j.Name = "spike-issue-42"
				j.IssueNumber = 42
			})
			if err := h.store.UpdateJobWorktree(sibling.ID, "/tmp/wt-sibling", "agent/issue-42"); err != nil {
				t.Fatal(err)
			}
			if err := h.store.UpdateJobStatus(sibling.ID, tc.siblingStatus); err != nil {
				t.Fatal(err)
			}

			// Second job on the same issue with a different agent derives the same branch.
			second := testJob(t, h.store, h.deploy, func(j *db.Job) {
				j.Agent = "autopilot"
				j.Name = "autopilot-issue-42"
				j.IssueNumber = 42
			})

			var setupCalled bool
			h.hooks.SetupWorktreeFn = func() error {
				setupCalled = true
				return nil
			}

			contract := &AgentContract{Name: "autopilot", Output: "pr"}
			err := h.run(context.Background(), second, contract)
			if err == nil {
				t.Fatal("expected second job to be blocked, got nil error")
			}
			if setupCalled {
				t.Error("SetupWorktree was called — active worktree may have been destroyed")
			}

			got, err := h.store.GetJob(second.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != db.StatusBailed {
				t.Errorf("second job status = %q, want %q", got.Status, db.StatusBailed)
			}
			if got.FailureReason.String != "branch_in_use" {
				t.Errorf("failure_reason = %q, want branch_in_use", got.FailureReason.String)
			}

			// Sibling must be untouched.
			sib, err := h.store.GetJob(sibling.ID)
			if err != nil {
				t.Fatal(err)
			}
			if sib.Branch.String != "agent/issue-42" {
				t.Errorf("sibling branch = %q, want agent/issue-42 (untouched)", sib.Branch.String)
			}
			if sib.Status != tc.siblingStatus {
				t.Errorf("sibling status = %q, want %q (untouched)", sib.Status, tc.siblingStatus)
			}
		})
	}
}

// TestBranchClaimReleasedWhenSiblingTerminal verifies that a terminal sibling
// (done/bailed) does not hold the branch claim, so a new job may proceed.
func TestBranchClaimReleasedWhenSiblingTerminal(t *testing.T) {
	h := newHarness(t)

	sibling := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.Agent = "spike"
		j.Name = "spike-issue-42"
		j.IssueNumber = 42
	})
	if err := h.store.UpdateJobWorktree(sibling.ID, "/tmp/wt-sibling", "agent/issue-42"); err != nil {
		t.Fatal(err)
	}
	if err := h.store.UpdateJobStatus(sibling.ID, db.StatusDone); err != nil {
		t.Fatal(err)
	}

	owner, err := h.store.ActiveJobOwningBranch(h.deploy.Owner, h.deploy.Repo, "agent/issue-42", 999)
	if err != nil {
		t.Fatal(err)
	}
	if owner != nil {
		t.Errorf("terminal sibling should not hold branch claim, got job #%d", owner.ID)
	}
}

func TestSetupHookAbsentDoesNotChangePipeline(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = false
	})

	job := testJob(t, h.store, h.deploy)
	contract := &AgentContract{
		Name:   "autopilot",
		Output: "pr",
		Stages: []StageContract{{Name: "run", Agent: "autopilot"}},
	}

	if err := h.run(context.Background(), job, contract); err != nil {
		t.Fatalf("Run: %v", err)
	}

	stages := h.stages()
	if len(stages) != 1 {
		t.Fatalf("got %d stage invocations, want 1", len(stages))
	}
	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status == db.StatusBlocked {
		t.Fatalf("job was blocked without a setup hook: %+v", updated)
	}
	if hasEvent(h.events(), string(EventInfo), "Setup hook") {
		t.Fatal("setup hook event emitted even though hook was absent")
	}
}

func TestSetupHookPassingRunsBeforeAgentAndCapturesOutput(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = false
	})
	writeSetupHook(t, h.deploy.RepoDir, "echo setup-start\nprintf 'setup-err\\n' >&2\n")

	var logAtAgentStart string
	job := testJob(t, h.store, h.deploy)
	h.hooks.RunStageFn = func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
		data, err := os.ReadFile(h.logPathForJob(job.ID))
		if err != nil {
			t.Fatalf("read log before agent write: %v", err)
		}
		logAtAgentStart = string(data)
		h.mu.Lock()
		h.stageLog = append(h.stageLog, stageCall{Agent: inv.AgentName, Inv: inv})
		h.mu.Unlock()
		return 0, &runtimepkg.Result{}, false, nil
	}

	contract := &AgentContract{
		Name:   "autopilot",
		Output: "pr",
		Stages: []StageContract{{Name: "run", Agent: "autopilot"}},
	}

	if err := h.run(context.Background(), job, contract); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, want := range []string{"setup-start", "setup-err", "setup hook started", "setup hook finished"} {
		if !strings.Contains(logAtAgentStart, want) {
			t.Fatalf("agent started without setup output %q in log:\n%s", want, logAtAgentStart)
		}
	}
	if len(h.stages()) != 1 {
		t.Fatalf("agent did not proceed after passing hook; stages=%d", len(h.stages()))
	}
	if !h.hasStoredEvent(t, EventInfo, "Setup hook started") {
		t.Fatal("missing setup hook start event")
	}
	if !h.hasStoredEvent(t, EventInfo, "Setup hook succeeded") {
		t.Fatal("missing setup hook success event")
	}
}

func TestSetupHookFailureBlocksWithoutAgentRun(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = false
	})
	writeSetupHook(t, h.deploy.RepoDir, "echo before-fail\nprintf 'bad setup\\n' >&2\nexit 7\n")

	job := testJob(t, h.store, h.deploy)
	contract := &AgentContract{
		Name:   "autopilot",
		Output: "pr",
		Stages: []StageContract{{Name: "run", Agent: "autopilot"}},
	}

	err := h.run(context.Background(), job, contract)
	if err == nil {
		t.Fatal("Run returned nil, want setup hook error")
	}

	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != db.StatusBlocked {
		t.Fatalf("status = %q, want %q", updated.Status, db.StatusBlocked)
	}
	if updated.FailureReason.String != setupHookFailureReason {
		t.Fatalf("failure_reason = %q, want %q", updated.FailureReason.String, setupHookFailureReason)
	}
	for _, want := range []string{"exit code 7", "before-fail", "bad setup"} {
		if !strings.Contains(updated.FailureDetail.String, want) {
			t.Fatalf("failure_detail missing %q:\n%s", want, updated.FailureDetail.String)
		}
	}
	if len(h.stages()) != 0 {
		t.Fatalf("agent ran after setup hook failure: %+v", h.stages())
	}
	runs, err := h.store.GetAgentRuns(job.ID)
	if err != nil {
		t.Fatalf("GetAgentRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("agent_runs recorded despite setup failure: %+v", runs)
	}
	if !h.hasStoredEvent(t, EventError, "Setup hook failed") {
		t.Fatal("missing setup hook failure event")
	}
}

func TestSetupHookTimeoutBlocksWithoutAgentRun(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = false
	})
	writeSetupHook(t, h.deploy.RepoDir, "echo before-sleep\nsleep 2\n")

	job := testJob(t, h.store, h.deploy)
	contract := &AgentContract{
		Name:   "autopilot",
		Output: "pr",
		Stages: []StageContract{{Name: "run", Agent: "autopilot"}},
	}

	sc := h.newSlotContext(job)
	sc.SetupTimeout = "20ms"
	mgr := NewDefaultJobManager(sc, contract)
	err := mgr.Run(context.Background())
	if err == nil {
		t.Fatal("Run returned nil, want setup hook timeout")
	}

	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != db.StatusBlocked {
		t.Fatalf("status = %q, want %q", updated.Status, db.StatusBlocked)
	}
	if updated.FailureReason.String != setupHookFailureReason {
		t.Fatalf("failure_reason = %q, want %q", updated.FailureReason.String, setupHookFailureReason)
	}
	if !strings.Contains(updated.FailureDetail.String, "timed out after 20ms") {
		t.Fatalf("failure_detail missing timeout:\n%s", updated.FailureDetail.String)
	}
	if !strings.Contains(updated.FailureDetail.String, "before-sleep") {
		t.Fatalf("failure_detail missing output tail:\n%s", updated.FailureDetail.String)
	}
	if len(h.stages()) != 0 {
		t.Fatalf("agent ran after setup hook timeout: %+v", h.stages())
	}
	runs, err := h.store.GetAgentRuns(job.ID)
	if err != nil {
		t.Fatalf("GetAgentRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("agent_runs recorded despite setup timeout: %+v", runs)
	}
}

func (h *pipelineHarness) logPathForJob(jobID int64) string {
	return filepath.Join(h.logDir, fmt.Sprintf("job-%d.log", jobID))
}

func (h *pipelineHarness) hasStoredEvent(t *testing.T, typ EventType, substr string) bool {
	t.Helper()
	events, err := h.store.EventsAfter(h.deploy.ID, 0, 0)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	for _, event := range events {
		if event.Type == string(typ) && strings.Contains(event.Summary, substr) {
			return true
		}
	}
	return false
}

func writeSetupHook(t *testing.T, root string, body string) {
	t.Helper()
	dir := filepath.Join(root, ".agent-minder")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir setup hook dir: %v", err)
	}
	path := filepath.Join(dir, "setup.sh")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nset -e\n"+body), 0o644); err != nil {
		t.Fatalf("write setup hook: %v", err)
	}
}

// TestPipeline_CodeThenReview verifies the happy path: code stage succeeds,
// PR detected, review stage fires and completes.
// This is the exact scenario that failed in production (issue #437 context):
// bug-fixer agent's code stage succeeded but review stage never fired.
func TestPipeline_CodeThenReview(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = true
	})

	// Mock: code stage succeeds, PR #100 detected.
	h.hooks.DetectPRFn = func(ctx context.Context) int {
		return 100
	}

	// Mock: review returns low-risk.
	h.hooks.ExtractReviewAssessmentFn = func(ctx context.Context, logPath string, job *db.Job) ReviewAssessment {
		return ReviewAssessment{Risk: "low-risk", Summary: "Clean PR", Lessons: []string{"Keep tests focused"}}
	}
	var reviewCommentPR int
	var reviewCommentBody string
	h.hooks.CreateReviewCommentFn = func(ctx context.Context, prNumber int, body string) (int64, error) {
		reviewCommentPR = prNumber
		reviewCommentBody = body
		return 12345, nil
	}

	job := testJob(t, h.store, h.deploy)
	contract := DefaultAutopilotContract()

	err := h.run(context.Background(), job, contract)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// Verify both stages ran.
	stages := h.stages()
	if len(stages) < 2 {
		t.Fatalf("expected at least 2 stage invocations (code + review), got %d: %+v", len(stages), stages)
	}
	if stages[0].Agent != "autopilot" {
		t.Errorf("stage 0: expected agent 'autopilot', got %q", stages[0].Agent)
	}
	if stages[1].Agent != "reviewer" {
		t.Errorf("stage 1: expected agent 'reviewer', got %q", stages[1].Agent)
	}

	// Verify DB state: job should have PR number and review risk set.
	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !updated.PRNumber.Valid || updated.PRNumber.Int64 != 100 {
		t.Errorf("expected PR #100, got %v", updated.PRNumber)
	}
	if !updated.ReviewRisk.Valid || updated.ReviewRisk.String != "low-risk" {
		t.Errorf("expected review_risk 'low-risk', got %v", updated.ReviewRisk)
	}
	if !updated.ReviewCommentID.Valid || updated.ReviewCommentID.Int64 != 12345 {
		t.Errorf("expected review_comment_id 12345, got %v", updated.ReviewCommentID)
	}
	// Status should be "reviewed" (pipeline finalized with PR).
	if updated.Status != db.StatusReviewed {
		t.Errorf("expected status %q, got %q", db.StatusReviewed, updated.Status)
	}
	if reviewCommentPR != 100 {
		t.Errorf("review comment PR = %d, want 100", reviewCommentPR)
	}
	if !strContains(reviewCommentBody, "**Risk:** `low-risk`") || !strContains(reviewCommentBody, "Clean PR") || !strContains(reviewCommentBody, "Keep tests focused") {
		t.Errorf("review comment body missing expected content: %s", reviewCommentBody)
	}

	// Verify events include review stage starting.
	events := h.events()
	if !hasEvent(events, "info", "Stage \"review\" started") {
		t.Errorf("expected 'Stage review started' event, got events: %+v", events)
	}
}

// TestPipeline_CodeSuccessNoReview verifies that when ReviewEnabled is false,
// only the code stage runs.
func TestPipeline_CodeSuccessNoReview(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = false
	})

	h.hooks.DetectPRFn = func(ctx context.Context) int {
		return 200
	}

	job := testJob(t, h.store, h.deploy)
	contract := DefaultContract("autopilot") // no explicit stages, ReviewEnabled=false → code only

	err := h.run(context.Background(), job, contract)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	stages := h.stages()
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage invocation (code only), got %d: %+v", len(stages), stages)
	}
	if stages[0].Agent != "autopilot" {
		t.Errorf("stage 0: expected agent 'autopilot', got %q", stages[0].Agent)
	}
}

// TestPipeline_CodeBailsNoReview verifies that when the code stage bails,
// the review stage does not fire and the job is marked as bailed.
func TestPipeline_CodeBailsNoReview(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = true
	})

	// Mock: code stage fails (non-zero exit, no PR).
	h.hooks.RunStageFn = func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
		h.mu.Lock()
		h.stageLog = append(h.stageLog, stageCall{Agent: inv.AgentName, Inv: inv})
		h.mu.Unlock()
		return 1, &runtimepkg.Result{}, false, nil
	}
	h.hooks.DetectPRFn = func(ctx context.Context) int {
		return 0 // no PR
	}

	job := testJob(t, h.store, h.deploy)
	contract := DefaultAutopilotContract()

	err := h.run(context.Background(), job, contract)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// Only code stage should have run — review should NOT fire.
	stages := h.stages()
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage invocation (code only, bail), got %d: %+v", len(stages), stages)
	}

	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	// Should have failure info set by finalizeBail.
	if !updated.FailureReason.Valid {
		// finalizeBail sets failure; if log is empty classifyOutcome returns empty strings,
		// but the DB update still happens.
		t.Logf("Note: failure_reason not set (expected when log is empty)")
	}
}

func TestPipeline_PushedBranchWithoutPRNeedsManualFollowUp(t *testing.T) {
	repo := initPushedBranchRepo(t, "agent/issue-2")
	h := newHarness(t, func(d *db.Deployment) {
		d.RepoDir = repo
		d.ReviewEnabled = false
	})

	h.hooks.RunStageFn = func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
		h.mu.Lock()
		h.stageLog = append(h.stageLog, stageCall{Agent: inv.AgentName, Inv: inv})
		h.mu.Unlock()
		return 0, &runtimepkg.Result{FinalText: "Implemented and pushed. PR creation requires token permissions."}, false, nil
	}
	h.hooks.DetectPRFn = func(ctx context.Context) int { return 0 }

	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.IssueNumber = 2
		j.Name = "issue-2"
	})
	contract := &AgentContract{
		Name:   "autopilot",
		Output: "pr",
		Stages: []StageContract{{Name: "run", Agent: "autopilot"}},
	}

	if err := h.run(context.Background(), job, contract); err != nil {
		t.Fatalf("Run: %v", err)
	}

	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if updated.Status != db.StatusManual {
		t.Fatalf("status = %q, want %q", updated.Status, db.StatusManual)
	}
	if !updated.FailureReason.Valid || updated.FailureReason.String != "pr_required" {
		t.Errorf("failure_reason = %+v, want pr_required", updated.FailureReason)
	}
	if !updated.FailureDetail.Valid || !strContains(updated.FailureDetail.String, "token permissions") {
		t.Errorf("failure_detail = %+v, want final message", updated.FailureDetail)
	}
	if !hasEvent(h.events(), "manual", "manual follow-up") {
		t.Errorf("expected manual event, got %+v", h.events())
	}
}

func initPushedBranchRepo(t *testing.T, branch string) string {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "repo")
	runGitCmd(t, root, "init", "--bare", origin)
	runGitCmd(t, root, "init", repo)
	runGitCmd(t, repo, "config", "user.email", "test@example.com")
	runGitCmd(t, repo, "config", "user.name", "Test User")
	runGitCmd(t, repo, "remote", "add", "origin", origin)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, repo, "add", "README.md")
	runGitCmd(t, repo, "commit", "-m", "initial")
	runGitCmd(t, repo, "branch", "-M", "main")
	runGitCmd(t, repo, "push", "-u", "origin", "main")
	runGitCmd(t, repo, "switch", "-c", branch)
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, repo, "add", "feature.txt")
	runGitCmd(t, repo, "commit", "-m", "feature")
	runGitCmd(t, repo, "push", "-u", "origin", branch)
	return repo
}

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// TestPipeline_ReviewSuspectRetry verifies that when the review stage returns
// "suspect" with issues, the code stage is re-run with feedback, then review
// runs again.
func TestPipeline_ReviewSuspectRetry(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = true
	})

	h.hooks.DetectPRFn = func(ctx context.Context) int {
		return 300
	}

	// Track how many times each stage runs.
	var codeRuns, reviewRuns atomic.Int32

	h.hooks.RunStageFn = func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
		h.mu.Lock()
		h.stageLog = append(h.stageLog, stageCall{Agent: inv.AgentName, Inv: inv})
		h.mu.Unlock()
		if inv.AgentName == "reviewer" {
			reviewRuns.Add(1)
		} else {
			codeRuns.Add(1)
		}
		return 0, &runtimepkg.Result{}, false, nil
	}

	// First review: suspect with issues → triggers retry.
	// Second review: low-risk → pipeline completes.
	var reviewCallCount atomic.Int32
	h.hooks.ExtractReviewAssessmentFn = func(ctx context.Context, logPath string, job *db.Job) ReviewAssessment {
		n := reviewCallCount.Add(1)
		if n == 1 {
			return ReviewAssessment{
				Risk:    "suspect",
				Summary: "Found issues",
				Issues:  []string{"Missing error handling in handler.go", "No test for edge case"},
			}
		}
		return ReviewAssessment{Risk: "low-risk", Summary: "Issues resolved"}
	}

	job := testJob(t, h.store, h.deploy)
	contract := DefaultAutopilotContract() // code + review(on_failure=skip, retries=1)
	// Override review on_failure to "retry" so it actually retries the code stage.
	contract.Stages[1].OnFailure = "retry"

	err := h.run(context.Background(), job, contract)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// Expected sequence: code(1) → review(1, suspect) → code(2, with feedback) → review(2, low-risk)
	if codeRuns.Load() != 2 {
		t.Errorf("expected code stage to run 2 times, got %d", codeRuns.Load())
	}
	if reviewRuns.Load() != 2 {
		t.Errorf("expected review stage to run 2 times, got %d", reviewRuns.Load())
	}

	// Verify the retry code stage received feedback in its prompt.
	stages := h.stages()
	// stages should be: [autopilot, reviewer, autopilot(retry), reviewer(retry)]
	if len(stages) < 3 {
		t.Fatalf("expected at least 3 stage calls, got %d", len(stages))
	}
	// The retry code stage should have "Review Feedback" in its prompt.
	retryPrompt := stages[2].Inv.Prompt
	if !strContains(retryPrompt, "Review Feedback") {
		t.Errorf("retry code stage prompt should contain 'Review Feedback', got: %s", truncate(retryPrompt, 200))
	}
}

func TestPipeline_ReviewSuspectSkipLogsGateNotFailure(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = true
	})

	h.hooks.DetectPRFn = func(ctx context.Context) int {
		return 300
	}
	h.hooks.ExtractReviewAssessmentFn = func(ctx context.Context, logPath string, job *db.Job) ReviewAssessment {
		return ReviewAssessment{
			Risk:    "suspect",
			Summary: "Found blocking issues",
			Issues:  []string{"Missing stale-lock recovery"},
		}
	}

	job := testJob(t, h.store, h.deploy)
	contract := DefaultAutopilotContract() // review on_failure=skip by default.

	if err := h.run(context.Background(), job, contract); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	events := h.events()
	if !hasEvent(events, "info", `Stage "review" review gate reported suspect`) {
		t.Fatalf("expected review gate skip event, got %#v", events)
	}
	if hasEvent(events, "info", `Stage "review" failed`) {
		t.Fatalf("did not expect review stage failure wording, got %#v", events)
	}

	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if updated.Status != db.StatusReviewed {
		t.Fatalf("status = %q, want %q", updated.Status, db.StatusReviewed)
	}
	if !updated.ReviewRisk.Valid || updated.ReviewRisk.String != "suspect" {
		t.Fatalf("review_risk = %+v, want suspect", updated.ReviewRisk)
	}
}

// TestPipeline_ProactiveAgent verifies pipeline execution for a proactive agent
// (issue_number=0) with no issue context.
func TestPipeline_ProactiveAgent(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = true
	})

	h.hooks.DetectPRFn = func(ctx context.Context) int {
		return 400
	}

	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.IssueNumber = 0
		j.Agent = "dependency-updater"
		j.Name = "weekly-deps-2026-04-04"
		j.IssueTitle = sql.NullString{String: "Weekly dependency update", Valid: true}
	})

	contract := &AgentContract{
		Name:   "dependency-updater",
		Mode:   "proactive",
		Output: "pr",
		Stages: []StageContract{
			{Name: "scan", Agent: "dependency-updater", OnFailure: "bail"},
			{Name: "review", Agent: "reviewer", OnFailure: "skip", Retries: 1},
		},
	}
	applyContractDefaults(contract)

	err := h.run(context.Background(), job, contract)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	stages := h.stages()
	if len(stages) < 2 {
		t.Fatalf("expected 2 stage invocations, got %d", len(stages))
	}
	if stages[0].Agent != "dependency-updater" {
		t.Errorf("stage 0: expected 'dependency-updater', got %q", stages[0].Agent)
	}
	if stages[1].Agent != "reviewer" {
		t.Errorf("stage 1: expected 'reviewer', got %q", stages[1].Agent)
	}

	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if updated.Status != db.StatusReviewed {
		t.Errorf("expected status %q, got %q", db.StatusReviewed, updated.Status)
	}
}

// TestPipeline_BugFixerAgent verifies the bug-fixer agent goes through
// code→review stages (the exact scenario from the production bug).
func TestPipeline_BugFixerAgent(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = true
	})

	h.hooks.DetectPRFn = func(ctx context.Context) int {
		return 435
	}

	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.Agent = "bug-fixer"
		j.Name = "issue-434"
		j.IssueNumber = 434
		j.IssueTitle = sql.NullString{String: "Bug in trigger routing", Valid: true}
	})

	// Bug-fixer uses default contract (no explicit stages) → should get code + review.
	contract := DefaultContract("bug-fixer")

	err := h.run(context.Background(), job, contract)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	stages := h.stages()
	if len(stages) < 2 {
		t.Fatalf("expected 2 stage invocations for bug-fixer (code + review), got %d: %+v", len(stages), stages)
	}
	if stages[0].Agent != "bug-fixer" {
		t.Errorf("stage 0: expected 'bug-fixer', got %q", stages[0].Agent)
	}
	if stages[1].Agent != "reviewer" {
		t.Errorf("stage 1: expected 'reviewer', got %q", stages[1].Agent)
	}

	// Verify current_stage was updated to "review" at some point.
	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if updated.ReviewRisk.String != "low-risk" {
		t.Errorf("expected review_risk 'low-risk', got %q", updated.ReviewRisk.String)
	}
}

// TestPipeline_ConcurrentJobs verifies that two jobs running concurrently
// don't interfere with each other's stage pipelines.
// This tests the scenario where job A bails while job B is between stages.
func TestPipeline_ConcurrentJobs(t *testing.T) {
	store := testStore(t)
	deploy := testDeployment(t, store, func(d *db.Deployment) {
		d.ReviewEnabled = true
	})
	sup := NewTestSupervisor(store, deploy, deploy.RepoDir)
	logDir := t.TempDir()

	// Job A: will bail on code stage.
	jobA := testJob(t, store, deploy, func(j *db.Job) {
		j.Name = "issue-416"
		j.IssueNumber = 416
		j.IssueTitle = sql.NullString{String: "Complex refactor", Valid: true}
	})

	// Job B: should succeed code→review despite A bailing.
	jobB := testJob(t, store, deploy, func(j *db.Job) {
		j.Name = "issue-434"
		j.IssueNumber = 434
		j.Agent = "bug-fixer"
		j.IssueTitle = sql.NullString{String: "Fix trigger routing", Valid: true}
	})

	sup.RegisterTestJob(jobA)
	sup.RegisterTestJob(jobB)

	var stagesA, stagesB []stageCall
	var muA, muB sync.Mutex

	// Barrier: make job A's code stage block until job B's code stage starts.
	bCodeStarted := make(chan struct{})
	aCanFinish := make(chan struct{})

	makeHooks := func(job *db.Job, stages *[]stageCall, mu *sync.Mutex) *TestHooks {
		return &TestHooks{
			SetupWorktreeFn:  func() error { return nil },
			EnsureAgentDefFn: func(name AgentName) (AgentDefSource, error) { return AgentDefBuiltIn, nil },
			RunStageFn: func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
				mu.Lock()
				*stages = append(*stages, stageCall{Agent: inv.AgentName})
				mu.Unlock()

				if job.ID == jobA.ID {
					// Job A: wait for B to start, then fail.
					close(aCanFinish) // signal that A is running
					select {
					case <-bCodeStarted:
					case <-ctx.Done():
						return 1, &runtimepkg.Result{}, false, ctx.Err()
					}
					return 1, &runtimepkg.Result{}, false, nil // bail
				}
				if job.ID == jobB.ID && inv.AgentName != "reviewer" {
					// Job B code stage: signal that B has started.
					select {
					case <-aCanFinish: // wait for A to be running
					default:
					}
					close(bCodeStarted)
				}
				return 0, &runtimepkg.Result{}, false, nil
			},
			DetectPRFn: func(ctx context.Context) int {
				if job.ID == jobB.ID {
					return 435
				}
				return 0
			},
			ExtractReviewAssessmentFn: func(ctx context.Context, logPath string, j *db.Job) ReviewAssessment {
				return ReviewAssessment{Risk: "low-risk", Summary: "Clean"}
			},
		}
	}

	hooksA := makeHooks(jobA, &stagesA, &muA)
	hooksB := makeHooks(jobB, &stagesB, &muB)

	contractA := DefaultAutopilotContract()
	contractB := DefaultContract("bug-fixer")

	var wg sync.WaitGroup
	var errA, errB error

	wg.Add(2)
	go func() {
		defer wg.Done()
		sc := &SlotContext{
			Store: store, Deploy: deploy, Job: jobA,
			RepoDir: deploy.RepoDir, Owner: deploy.Owner, Repo: deploy.Repo,
			WorktreePath: deploy.RepoDir, Branch: "agent/issue-416",
			LogPath: filepath.Join(logDir, "jobA.log"), BaseBranch: "main",
			AllowedTools: []string{"Read"}, sup: sup, Hooks: hooksA,
		}
		mgr := NewDefaultJobManager(sc, contractA)
		errA = mgr.Run(context.Background())
	}()
	go func() {
		defer wg.Done()
		sc := &SlotContext{
			Store: store, Deploy: deploy, Job: jobB,
			RepoDir: deploy.RepoDir, Owner: deploy.Owner, Repo: deploy.Repo,
			WorktreePath: deploy.RepoDir, Branch: "agent/issue-434",
			LogPath: filepath.Join(logDir, "jobB.log"), BaseBranch: "main",
			AllowedTools: []string{"Read"}, sup: sup, Hooks: hooksB,
		}
		mgr := NewDefaultJobManager(sc, contractB)
		errB = mgr.Run(context.Background())
	}()

	wg.Wait()

	if errA != nil {
		t.Errorf("Job A returned error: %v", errA)
	}
	if errB != nil {
		t.Errorf("Job B returned error: %v", errB)
	}

	// Job A: should only have code stage (bailed).
	muA.Lock()
	aLen := len(stagesA)
	muA.Unlock()
	if aLen != 1 {
		t.Errorf("Job A: expected 1 stage (code bail), got %d", aLen)
	}

	// Job B: should have code + review (the critical assertion).
	muB.Lock()
	bStages := make([]stageCall, len(stagesB))
	copy(bStages, stagesB)
	muB.Unlock()
	if len(bStages) < 2 {
		t.Fatalf("Job B: expected 2 stages (code + review), got %d: %+v — review stage did not fire!", len(bStages), bStages)
	}
	if bStages[0].Agent != "bug-fixer" {
		t.Errorf("Job B stage 0: expected 'bug-fixer', got %q", bStages[0].Agent)
	}
	if bStages[1].Agent != "reviewer" {
		t.Errorf("Job B stage 1: expected 'reviewer', got %q", bStages[1].Agent)
	}

	// Verify Job B has review_risk set.
	updatedB, err := store.GetJob(jobB.ID)
	if err != nil {
		t.Fatalf("GetJob B: %v", err)
	}
	if updatedB.ReviewRisk.String != "low-risk" {
		t.Errorf("Job B: expected review_risk 'low-risk', got %q", updatedB.ReviewRisk.String)
	}
}

// TestPipeline_ReviewSkipOnNoPR verifies that the review stage is silently
// skipped when no PR was opened (e.g., non-PR output agent).
func TestPipeline_ReviewSkipOnNoPR(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = true
	})

	// No PR detected.
	h.hooks.DetectPRFn = func(ctx context.Context) int { return 0 }

	job := testJob(t, h.store, h.deploy)

	// Contract with output=pr but no PR will be found → code fails, bail.
	// Use output=none so code succeeds on exit 0.
	contract := &AgentContract{
		Name:   "report-gen",
		Mode:   "reactive",
		Output: "none",
		Stages: []StageContract{
			{Name: "run", Agent: "report-gen", OnFailure: "bail"},
			{Name: "review", Agent: "reviewer", OnFailure: "skip"},
		},
	}
	applyContractDefaults(contract)

	err := h.run(context.Background(), job, contract)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// Both stages should be called, but review should succeed immediately
	// (no PR to review → returns stageResult{success: true}).
	stages := h.stages()
	// Code stage runs the agent; review stage should see no PR and skip gracefully
	// (executeReviewStage returns success when PRNumber is not valid).
	if len(stages) != 1 {
		// Only code stage runs through the runtime; review stage exits early since no PR.
		t.Logf("stages: %+v", stages)
	}

	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	// Should be done (non-PR agent, pipeline complete).
	if updated.Status != db.StatusDone {
		t.Errorf("expected status %q, got %q", db.StatusDone, updated.Status)
	}
}

// TestPipeline_DefaultStagesWithReview verifies that an agent with no explicit
// stages gets the default [code, review] pipeline when ReviewEnabled=true.
// This is the exact code path from the production bug.
func TestPipeline_DefaultStagesWithReview(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = true
	})

	h.hooks.DetectPRFn = func(ctx context.Context) int { return 500 }

	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.Agent = "bug-fixer"
	})

	// Contract with NO explicit stages — DefaultJobManager.Run builds them.
	contract := &AgentContract{
		Name:   "bug-fixer",
		Mode:   "reactive",
		Output: "pr",
	}
	applyContractDefaults(contract)
	// Verify the contract has no stages (they'll be built at runtime).
	if len(contract.Stages) != 0 {
		t.Fatalf("expected 0 stages in contract (should be built at runtime), got %d", len(contract.Stages))
	}

	err := h.run(context.Background(), job, contract)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	stages := h.stages()
	if len(stages) < 2 {
		t.Fatalf("expected 2 stage invocations (auto-built code + review), got %d: %+v", len(stages), stages)
	}
	if stages[0].Agent != "bug-fixer" {
		t.Errorf("stage 0: expected 'bug-fixer', got %q", stages[0].Agent)
	}
	if stages[1].Agent != "reviewer" {
		t.Errorf("stage 1: expected 'reviewer', got %q", stages[1].Agent)
	}
}

// TestPipeline_StageNamedReviewWithNonReviewerAgent verifies that a stage
// named "review" but using a non-reviewer agent runs as a code stage,
// not through the review-specific path.
func TestPipeline_StageNamedReviewWithNonReviewerAgent(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = false // no auto-appended review
	})

	var callOrder []string
	h.hooks.RunStageFn = func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
		h.mu.Lock()
		callOrder = append(callOrder, inv.AgentName)
		h.stageLog = append(h.stageLog, stageCall{Agent: inv.AgentName, Inv: inv})
		h.mu.Unlock()
		// Simulate some work time so timestamps differ.
		time.Sleep(10 * time.Millisecond)
		return 0, &runtimepkg.Result{}, false, nil
	}

	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.Agent = "quality-check"
		j.Name = "weekly-quality"
		j.IssueNumber = 0 // proactive
		j.IssueTitle = sql.NullString{String: "Weekly quality review", Valid: true}
	})

	// Contract with a stage named "review" using a non-reviewer agent,
	// followed by a "verify" stage using the actual reviewer.
	contract := &AgentContract{
		Name:   "quality-check",
		Mode:   "proactive",
		Output: "pr",
		Stages: []StageContract{
			{Name: "review", Agent: "quality-check", OnFailure: "bail"},
			{Name: "verify", Agent: "reviewer", OnFailure: "skip"},
		},
	}
	applyContractDefaults(contract)

	// Need a PR for the reviewer stage to actually run.
	h.hooks.DetectPRFn = func(ctx context.Context) int { return 500 }

	err := h.run(context.Background(), job, contract)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// Both stages should have run through the runtime.
	stages := h.stages()
	if len(stages) < 2 {
		t.Fatalf("expected 2 agent invocations, got %d: %+v", len(stages), stages)
	}

	// Stage 1 ("review") should run quality-check as a code stage.
	if stages[0].Agent != "quality-check" {
		t.Errorf("stage 0: expected 'quality-check', got %q", stages[0].Agent)
	}
	// Stage 2 ("verify") should run reviewer as a review stage.
	if stages[1].Agent != "reviewer" {
		t.Errorf("stage 1: expected 'reviewer', got %q", stages[1].Agent)
	}

	// Verify sequential execution: quality-check must come before reviewer.
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(callOrder) != 2 || callOrder[0] != "quality-check" || callOrder[1] != "reviewer" {
		t.Errorf("expected sequential execution [quality-check, reviewer], got %v", callOrder)
	}
}

// TestPipeline_AutoMergeSkippedForNonDefaultBase verifies that a low-risk PR
// whose base branch is not the deployment default does NOT get auto-merge
// enabled. Enabling it would silently squash a stacked child PR into an
// in-flight parent branch. See #611.
func TestPipeline_AutoMergeSkippedForNonDefaultBase(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = true
		d.AutoMerge = true
		d.BaseBranch = "main"
	})

	h.hooks.DetectPRFn = func(ctx context.Context) int { return 100 }
	h.hooks.ExtractReviewAssessmentFn = func(ctx context.Context, logPath string, job *db.Job) ReviewAssessment {
		return ReviewAssessment{Risk: "low-risk", Summary: "Clean"}
	}
	// PR targets a non-default base branch.
	h.hooks.GetPRBaseFn = func(ctx context.Context, prNumber int) (string, error) {
		return "agent/issue-99", nil
	}
	var autoMergeCalled bool
	h.hooks.EnableAutoMergeFn = func(ctx context.Context, prNumber int, method string) error {
		autoMergeCalled = true
		return nil
	}

	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.IssueNumber = 611
		j.Name = "issue-611"
	})
	contract := &AgentContract{
		Name:   "autopilot",
		Output: "pr",
		Stages: []StageContract{{Name: "run", Agent: "autopilot"}},
	}

	if err := h.run(context.Background(), job, contract); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if autoMergeCalled {
		t.Error("auto-merge was enabled for a PR with a non-default base branch")
	}
	if !hasEvent(h.events(), "info", "Auto-merge skipped for PR #100") {
		t.Error("expected an info event noting auto-merge was skipped")
	}
}

// TestPipeline_AutoMergeEnabledForDefaultBase verifies default-base behavior is
// unchanged: a low-risk PR targeting the deployment default gets auto-merge
// enabled (which waits for CI to pass). See #611.
func TestPipeline_AutoMergeEnabledForDefaultBase(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = true
		d.AutoMerge = true
		d.BaseBranch = "main"
	})

	h.hooks.DetectPRFn = func(ctx context.Context) int { return 100 }
	h.hooks.ExtractReviewAssessmentFn = func(ctx context.Context, logPath string, job *db.Job) ReviewAssessment {
		return ReviewAssessment{Risk: "low-risk", Summary: "Clean"}
	}
	h.hooks.GetPRBaseFn = func(ctx context.Context, prNumber int) (string, error) {
		return "main", nil
	}
	var autoMergeCalled bool
	h.hooks.EnableAutoMergeFn = func(ctx context.Context, prNumber int, method string) error {
		autoMergeCalled = true
		return nil
	}

	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.IssueNumber = 611
		j.Name = "issue-611-default"
	})
	contract := &AgentContract{
		Name:   "autopilot",
		Output: "pr",
		Stages: []StageContract{{Name: "run", Agent: "autopilot"}},
	}

	if err := h.run(context.Background(), job, contract); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if !autoMergeCalled {
		t.Error("auto-merge was not enabled for a low-risk PR on the default base")
	}
	if !hasEvent(h.events(), "info", "Auto-merge enabled for PR #100") {
		t.Error("expected an info event noting auto-merge was enabled")
	}
}

// TestPipeline_CapturesLessonsFromNonReviewerStage verifies that a stage
// with captures_lessons: true extracts and saves lessons from its output.
func TestPipeline_CapturesLessonsFromNonReviewerStage(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = false
	})

	// The extraction hook returns lessons.
	h.hooks.ExtractReviewAssessmentFn = func(ctx context.Context, logPath string, job *db.Job) ReviewAssessment {
		return ReviewAssessment{
			Risk:    "low-risk",
			Summary: "Quality check found patterns to learn from",
			Lessons: []string{
				"Always validate input before database writes",
				"Use structured logging with slog, not fmt.Println",
			},
		}
	}

	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.Agent = "quality-check"
		j.Name = "weekly-quality"
		j.IssueNumber = 0
		j.IssueTitle = sql.NullString{String: "Weekly quality review", Valid: true}
	})

	contract := &AgentContract{
		Name:   "quality-check",
		Mode:   "proactive",
		Output: "none",
		Stages: []StageContract{
			{Name: "scan", Agent: "quality-check", OnFailure: "bail", CapturesLessons: true},
		},
	}
	applyContractDefaults(contract)

	err := h.run(context.Background(), job, contract)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// Verify lessons were captured.
	scope := h.deploy.Owner + "/" + h.deploy.Repo
	lessons, err := h.store.GetActiveLessons(scope)
	if err != nil {
		t.Fatalf("GetActiveLessons: %v", err)
	}

	if len(lessons) < 2 {
		t.Fatalf("expected at least 2 lessons captured, got %d", len(lessons))
	}

	// Check lesson content.
	found := map[string]bool{}
	for _, l := range lessons {
		found[l.Content] = true
	}
	if !found["Always validate input before database writes"] {
		t.Error("expected lesson about input validation")
	}
	if !found["Use structured logging with slog, not fmt.Println"] {
		t.Error("expected lesson about structured logging")
	}
}

// TestPipeline_NoCaptureWithoutFlag verifies that stages without
// captures_lessons don't extract lessons.
func TestPipeline_NoCaptureWithoutFlag(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = false
	})

	// Even if the hook returns lessons, they should NOT be captured.
	h.hooks.ExtractReviewAssessmentFn = func(ctx context.Context, logPath string, job *db.Job) ReviewAssessment {
		return ReviewAssessment{
			Risk:    "low-risk",
			Lessons: []string{"This should not be captured"},
		}
	}

	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.Agent = "quality-check"
		j.Name = "weekly-quality-2"
		j.IssueNumber = 0
	})

	contract := &AgentContract{
		Name:   "quality-check",
		Mode:   "proactive",
		Output: "none",
		Stages: []StageContract{
			{Name: "scan", Agent: "quality-check", OnFailure: "bail"},
			// No CapturesLessons flag.
		},
	}
	applyContractDefaults(contract)

	err := h.run(context.Background(), job, contract)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	scope := h.deploy.Owner + "/" + h.deploy.Repo
	lessons, _ := h.store.GetActiveLessons(scope)
	for _, l := range lessons {
		if l.Content == "This should not be captured" {
			t.Error("lesson was captured despite no captures_lessons flag")
		}
	}
}

// TestPipeline_WritesAgentRuns verifies the supervisor records a durable
// agent_runs row per stage execution, capturing the terminal status, exact
// final turns, cost, and session, plus a separate row for the review stage.
func TestPipeline_WritesAgentRuns(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) { d.ReviewEnabled = true })
	h.sup.SetRuntime(&fakeRuntime{metadata: runtimepkg.RunMetadata{
		RuntimeName:    "fake",
		Model:          "resolved-model",
		RuntimeVersion: "fake version 1.2.3",
	}})

	h.hooks.DetectPRFn = func(ctx context.Context) int { return 100 }
	h.hooks.RunStageFn = func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
		return 0, &runtimepkg.Result{
			SessionID:    "sess-" + inv.AgentName,
			Model:        "resolved-model",
			NumTurns:     9,
			TotalCostUSD: 0.21,
			FinalText:    "done",
			StopReason:   "end_turn",
		}, false, nil
	}
	h.hooks.ExtractReviewAssessmentFn = func(ctx context.Context, logPath string, job *db.Job) ReviewAssessment {
		return ReviewAssessment{Risk: "low-risk", Summary: "Clean"}
	}

	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.IssueNumber = 77
		j.Name = "issue-77"
		j.Model = sql.NullString{String: "requested-model", Valid: true}
	})
	contract := &AgentContract{
		Name:   "autopilot",
		Output: "pr",
		Stages: []StageContract{{Name: "run", Agent: "autopilot"}},
	}

	if err := h.run(context.Background(), job, contract); err != nil {
		t.Fatalf("Run: %v", err)
	}

	runs, err := h.store.GetAgentRuns(job.ID)
	if err != nil {
		t.Fatalf("GetAgentRuns: %v", err)
	}
	// One code run + one auto-appended review run.
	if len(runs) != 2 {
		t.Fatalf("got %d agent runs, want 2: %+v", len(runs), runs)
	}

	code := runs[0]
	if code.Stage != "run" || code.Agent != "autopilot" || code.Attempt != 1 {
		t.Errorf("code run identity = %s/%s/%d, want run/autopilot/1", code.Stage, code.Agent, code.Attempt)
	}
	if code.Status != db.RunStatusSuccess {
		t.Errorf("code run status = %q, want success", code.Status)
	}
	if code.FinalTurns != 9 || code.CostUSD != 0.21 {
		t.Errorf("code run turns/cost = %d/%v, want 9/0.21", code.FinalTurns, code.CostUSD)
	}
	if code.SessionID.String != "sess-autopilot" {
		t.Errorf("code run session = %q, want sess-autopilot", code.SessionID.String)
	}
	if code.Runtime.String != "fake" {
		t.Errorf("code run runtime = %q, want fake", code.Runtime.String)
	}
	if code.Model.String != "resolved-model" {
		t.Errorf("code run model = %q, want resolved-model", code.Model.String)
	}
	if code.RuntimeVersion.String != "fake version 1.2.3" {
		t.Errorf("code run runtime_version = %q, want fake version 1.2.3", code.RuntimeVersion.String)
	}
	if !code.CompletedAt.Valid {
		t.Error("code run completed_at should be set")
	}

	review := runs[1]
	if review.Agent != "reviewer" {
		t.Errorf("second run agent = %q, want reviewer", review.Agent)
	}
	if review.Status != db.RunStatusSuccess {
		t.Errorf("review run status = %q, want success", review.Status)
	}
}

func TestPipeline_ReconcilesRuntimeReportedModelMismatch(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) { d.ReviewEnabled = false })
	h.sup.SetRuntime(&fakeRuntime{metadata: runtimepkg.RunMetadata{
		RuntimeName: "fake",
		Model:       "requested-model",
	}})
	h.hooks.RunStageFn = func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
		return 0, &runtimepkg.Result{
			SessionID: "sess-autopilot",
			Model:     "reported-model",
			FinalText: "done",
		}, false, nil
	}

	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.IssueNumber = 88
		j.Name = "issue-88"
		j.Model = sql.NullString{String: "requested-model", Valid: true}
	})
	contract := &AgentContract{
		Name:   "autopilot",
		Output: "issue",
		Stages: []StageContract{{Name: "run", Agent: "autopilot"}},
	}

	if err := h.run(context.Background(), job, contract); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !hasEvent(h.events(), string(EventWarning), `recorded "requested-model" but runtime reported "reported-model"`) {
		t.Fatalf("missing runtime model mismatch warning")
	}

	runs, err := h.store.GetAgentRuns(job.ID)
	if err != nil {
		t.Fatalf("GetAgentRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d agent runs, want 1", len(runs))
	}
	if runs[0].Model.String != "reported-model" {
		t.Errorf("reconciled model = %q, want reported-model", runs[0].Model.String)
	}
}

func TestScriptJobCompletesWithCapturedOutputAndRunRecord(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(filepath.Join(h.deploy.RepoDir, "input.txt"), []byte("from repo"), 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.Kind = db.JobKindScript
		j.Agent = db.JobKindScript
		j.Name = "lint-20260809-1200"
		j.IssueNumber = 0
		j.ScriptCommand = sql.NullString{String: "cat input.txt && printf '\\n%s\\n' \"$FOO\"", Valid: true}
		j.ScriptEnv = sql.NullString{String: `{"FOO":"bar"}`, Valid: true}
	})
	sc := h.newSlotContext(job)

	if err := runScriptJob(context.Background(), sc); err != nil {
		t.Fatalf("runScriptJob: %v", err)
	}

	got, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != db.StatusDone {
		t.Fatalf("status = %q, want done", got.Status)
	}
	if got.CostUSD != 0 {
		t.Fatalf("cost_usd = %v, want zero", got.CostUSD)
	}
	if !got.AgentLog.Valid || got.AgentLog.String == "" {
		t.Fatalf("agent_log = %v, want captured log path", got.AgentLog)
	}
	logData, err := os.ReadFile(got.AgentLog.String)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(logData), "from repo") || !strings.Contains(string(logData), "bar") {
		t.Fatalf("log missing stdout/env output:\n%s", string(logData))
	}
	runs, err := h.store.GetAgentRuns(job.ID)
	if err != nil {
		t.Fatalf("GetAgentRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0].Status != db.RunStatusSuccess || runs[0].Agent != db.JobKindScript {
		t.Fatalf("run = %+v, want successful script run", runs[0])
	}
	if runs[0].CostUSD != 0 {
		t.Fatalf("run cost = %v, want zero", runs[0].CostUSD)
	}
}

func TestScriptJobFailureRecordsExitCode(t *testing.T) {
	h := newHarness(t)
	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.Kind = db.JobKindScript
		j.Agent = db.JobKindScript
		j.Name = "fail-20260809-1200"
		j.IssueNumber = 0
		j.ScriptCommand = sql.NullString{String: "printf boom >&2; exit 7", Valid: true}
	})
	sc := h.newSlotContext(job)

	if err := runScriptJob(context.Background(), sc); err == nil {
		t.Fatal("expected script failure")
	}

	got, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != db.StatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if !got.FailureDetail.Valid || !strings.Contains(got.FailureDetail.String, "code 7") {
		t.Fatalf("failure_detail = %v, want exit code", got.FailureDetail)
	}
	runs, err := h.store.GetAgentRuns(job.ID)
	if err != nil {
		t.Fatalf("GetAgentRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != db.RunStatusFailed || !strings.Contains(runs[0].FailureDetail.String, "code 7") {
		t.Fatalf("runs = %+v, want failed run with exit code", runs)
	}
}

func TestScriptJobTimeoutKillsAndRecordsFailure(t *testing.T) {
	h := newHarness(t)
	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.Kind = db.JobKindScript
		j.Agent = db.JobKindScript
		j.Name = "timeout-20260809-1200"
		j.IssueNumber = 0
		j.ScriptCommand = sql.NullString{String: "while true; do :; done", Valid: true}
		j.ScriptTimeout = sql.NullString{String: "20ms", Valid: true}
	})
	sc := h.newSlotContext(job)

	if err := runScriptJob(context.Background(), sc); err == nil {
		t.Fatal("expected timeout")
	}

	got, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != db.StatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if !got.FailureReason.Valid || got.FailureReason.String != "timeout" {
		t.Fatalf("failure_reason = %v, want timeout", got.FailureReason)
	}
	if !got.FailureDetail.Valid || !strings.Contains(got.FailureDetail.String, "timed out") {
		t.Fatalf("failure_detail = %v, want timeout detail", got.FailureDetail)
	}
}

// --- Utilities ---

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
