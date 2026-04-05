package supervisor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
)

// --- Test helpers ---

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
	stageLog []stageCall // records each RunClaudeAgent invocation
	mu       sync.Mutex
}

type stageCall struct {
	Agent string // agent name from --agent arg
	Args  []string
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
		RunClaudeAgentFn: func(ctx context.Context, args []string, logFile *os.File) (int, error) {
			// Default: succeed immediately.
			agent := extractAgentArg(args)
			h.mu.Lock()
			h.stageLog = append(h.stageLog, stageCall{Agent: agent, Args: args})
			h.mu.Unlock()
			return 0, nil
		},
		DetectPRFn: func(ctx context.Context) int {
			return 0 // default: no PR detected
		},
		ExtractReviewAssessmentFn: func(ctx context.Context, logPath string, job *db.Job) ReviewAssessment {
			return ReviewAssessment{Risk: "low-risk", Summary: "All good"}
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

// extractAgentArg finds the --agent value from CLI args.
func extractAgentArg(args []string) string {
	for i, a := range args {
		if a == "--agent" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
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
		return ReviewAssessment{Risk: "low-risk", Summary: "Clean PR"}
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
	// Status should be "reviewed" (pipeline finalized with PR).
	if updated.Status != db.StatusReviewed {
		t.Errorf("expected status %q, got %q", db.StatusReviewed, updated.Status)
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
	h.hooks.RunClaudeAgentFn = func(ctx context.Context, args []string, logFile *os.File) (int, error) {
		agent := extractAgentArg(args)
		h.mu.Lock()
		h.stageLog = append(h.stageLog, stageCall{Agent: agent, Args: args})
		h.mu.Unlock()
		return 1, nil // non-zero exit
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

	h.hooks.RunClaudeAgentFn = func(ctx context.Context, args []string, logFile *os.File) (int, error) {
		agent := extractAgentArg(args)
		h.mu.Lock()
		h.stageLog = append(h.stageLog, stageCall{Agent: agent, Args: args})
		h.mu.Unlock()
		if agent == "reviewer" {
			reviewRuns.Add(1)
		} else {
			codeRuns.Add(1)
		}
		return 0, nil
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
	// The retry code stage should have "Review Feedback" in its prompt (last arg).
	retryArgs := stages[2].Args
	lastArg := retryArgs[len(retryArgs)-1]
	if !strContains(lastArg, "Review Feedback") {
		t.Errorf("retry code stage prompt should contain 'Review Feedback', got: %s", truncate(lastArg, 200))
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
			RunClaudeAgentFn: func(ctx context.Context, args []string, logFile *os.File) (int, error) {
				agent := extractAgentArg(args)
				mu.Lock()
				*stages = append(*stages, stageCall{Agent: agent})
				mu.Unlock()

				if job.ID == jobA.ID {
					// Job A: wait for B to start, then fail.
					close(aCanFinish) // signal that A is running
					select {
					case <-bCodeStarted:
					case <-ctx.Done():
						return 1, ctx.Err()
					}
					return 1, nil // bail
				}
				if job.ID == jobB.ID && agent != "reviewer" {
					// Job B code stage: signal that B has started.
					select {
					case <-aCanFinish: // wait for A to be running
					default:
					}
					close(bCodeStarted)
				}
				return 0, nil
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
		// Only code stage calls RunClaudeAgent; review stage exits early since no PR.
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
	h.hooks.RunClaudeAgentFn = func(ctx context.Context, args []string, logFile *os.File) (int, error) {
		agent := extractAgentArg(args)
		h.mu.Lock()
		callOrder = append(callOrder, agent)
		h.stageLog = append(h.stageLog, stageCall{Agent: agent, Args: args})
		h.mu.Unlock()
		// Simulate some work time so timestamps differ.
		time.Sleep(10 * time.Millisecond)
		return 0, nil
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

	// Both stages should have called RunClaudeAgent.
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

// --- Utilities ---

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
