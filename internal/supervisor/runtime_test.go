package supervisor

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimepkg "github.com/aptx-health/agent-minder/internal/runtime"
	_ "github.com/aptx-health/agent-minder/internal/runtime/codex"
	opencodert "github.com/aptx-health/agent-minder/internal/runtime/opencode"
)

// fakeRuntime is a runtime.AgentRuntime stub used to verify that
// DefaultJobManager drives stages through the runtime contract and that
// EventSink events surface as live status updates.
type fakeRuntime struct {
	runCalled     int
	prepareCalled int
	lastInv       runtimepkg.Invocation
	lastDef       runtimepkg.AgentDefinition
	lastWorkspace runtimepkg.Workspace
}

func (f *fakeRuntime) Name() string { return "fake" }

func (f *fakeRuntime) PrepareAgentDef(_ context.Context, ws runtimepkg.Workspace, def runtimepkg.AgentDefinition) error {
	f.prepareCalled++
	f.lastWorkspace = ws
	f.lastDef = def
	return nil
}

func (f *fakeRuntime) Run(_ context.Context, inv runtimepkg.Invocation, sink runtimepkg.EventSink, logFile io.Writer) (int, error) {
	f.runCalled++
	f.lastInv = inv
	// Emit a realistic event sequence: two assistant steps, one tool call.
	sink.OnAssistantStep()
	sink.OnToolStart("Read", "internal/foo.go")
	sink.OnAssistantStep()
	sink.OnToolEnd()
	// Write a tiny "log" so ParseResult / DetectPR fallbacks have something
	// to look at when called.
	_, _ = logFile.Write([]byte(`{"type":"result","is_error":false,"num_turns":2,"total_cost_usd":0.01,"session_id":"sess-1","result":"done"}` + "\n"))
	return 0, nil
}

func (f *fakeRuntime) Resume(_ context.Context, _ string, _ runtimepkg.EventSink, _ io.Writer) (int, error) {
	return 0, runtimepkg.ErrNotSupported
}

func (f *fakeRuntime) ParseResult(_ string) (*runtimepkg.Result, error) {
	return &runtimepkg.Result{
		SessionID:    "sess-1",
		NumTurns:     2,
		TotalCostUSD: 0.01,
		FinalText:    "done",
	}, nil
}

func (f *fakeRuntime) ClassifyOutcome(_ *runtimepkg.Result, _ runtimepkg.Limits) runtimepkg.Outcome {
	return runtimepkg.Outcome{}
}

func (f *fakeRuntime) ExtractBailReport(_ *runtimepkg.Result, _ string) *runtimepkg.BailReport {
	return nil
}

func TestEnsureAgentDefPreparesRuntimeDefinition(t *testing.T) {
	store := testStore(t)
	deploy := testDeployment(t, store)
	job := testJob(t, store, deploy)

	sup := NewTestSupervisor(store, deploy, deploy.RepoDir)
	rt := &fakeRuntime{}
	sup.SetRuntime(rt)

	worktree := t.TempDir()
	agentsDir := filepath.Join(worktree, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("---\nname: autopilot\n---\nrepo contract")
	if err := os.WriteFile(filepath.Join(agentsDir, "autopilot.md"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	sc := sup.newSlotContext(job.ID, job)
	sc.WorktreePath = worktree

	source, err := sc.EnsureAgentDef(AgentAutopilot)
	if err != nil {
		t.Fatalf("EnsureAgentDef: %v", err)
	}
	if source != AgentDefRepo {
		t.Errorf("source = %q, want %q", source, AgentDefRepo)
	}
	if rt.prepareCalled != 1 {
		t.Fatalf("PrepareAgentDef calls = %d, want 1", rt.prepareCalled)
	}
	if rt.lastWorkspace.Dir != worktree {
		t.Errorf("workspace = %q, want %q", rt.lastWorkspace.Dir, worktree)
	}
	if rt.lastDef.Name != "autopilot" {
		t.Errorf("def name = %q, want autopilot", rt.lastDef.Name)
	}
	if string(rt.lastDef.Body) != string(body) {
		t.Errorf("def body = %q, want %q", rt.lastDef.Body, body)
	}
}

func TestRuntimeForJobUsesJobOverride(t *testing.T) {
	store := testStore(t)
	deploy := testDeployment(t, store)
	deploy.Runtime = runtimepkg.NameClaudeCode
	job := testJob(t, store, deploy)
	job.Runtime = sql.NullString{String: runtimepkg.NameCodex, Valid: true}

	sup := NewTestSupervisor(store, deploy, deploy.RepoDir)
	sup.SetRuntime(&fakeRuntime{})

	rt, err := sup.RuntimeForJob(job)
	if err != nil {
		t.Fatalf("RuntimeForJob: %v", err)
	}
	if rt == nil {
		t.Fatal("RuntimeForJob returned nil")
	}
	if got := rt.Name(); got != runtimepkg.NameCodex {
		t.Fatalf("RuntimeForJob runtime = %q, want %q", got, runtimepkg.NameCodex)
	}
}

// TestRuntimeForJobSelectsOpenCode mirrors the codex override for opencode: a
// job-level runtime override resolves through the registry to the real opencode
// runtime (registered via the blank import above), independent of the
// deployment's default runtime.
func TestRuntimeForJobSelectsOpenCode(t *testing.T) {
	store := testStore(t)
	deploy := testDeployment(t, store)
	deploy.Runtime = runtimepkg.NameClaudeCode
	job := testJob(t, store, deploy)
	job.Runtime = sql.NullString{String: runtimepkg.NameOpenCode, Valid: true}

	sup := NewTestSupervisor(store, deploy, deploy.RepoDir)

	rt, err := sup.RuntimeForJob(job)
	if err != nil {
		t.Fatalf("RuntimeForJob: %v", err)
	}
	if rt == nil {
		t.Fatal("RuntimeForJob returned nil")
	}
	if got := rt.Name(); got != runtimepkg.NameOpenCode {
		t.Fatalf("RuntimeForJob runtime = %q, want %q", got, runtimepkg.NameOpenCode)
	}
}

// TestExecuteCodeStage_RuntimeLiveStatus exercises executeCodeStage through a
// fake AgentRuntime and asserts that EventSink updates land on the running
// state's LiveStatus (the same field consumed by minder status / TUI / xbar).
func TestExecuteCodeStage_RuntimeLiveStatus(t *testing.T) {
	store := testStore(t)
	deploy := testDeployment(t, store)
	job := testJob(t, store, deploy)

	sup := NewTestSupervisor(store, deploy, deploy.RepoDir)
	rt := &fakeRuntime{}
	sup.SetRuntime(rt)
	sup.RegisterTestJob(job)

	sc := sup.newSlotContext(job.ID, job)
	sc.LogPath = filepath.Join(t.TempDir(), "agent.log")
	// Skip worktree + def + PR detection (legacy hooks still apply).
	sc.Hooks = &TestHooks{
		SetupWorktreeFn:  func() error { return nil },
		EnsureAgentDefFn: func(_ AgentName) (AgentDefSource, error) { return AgentDefBuiltIn, nil },
		DetectPRFn:       func(_ context.Context) int { return 0 },
	}

	logFile, err := os.Create(sc.LogPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	defer func() { _ = logFile.Close() }()

	contract := &AgentContract{
		Name:   "autopilot",
		Output: "pr",
	}
	mgr := NewDefaultJobManager(sc, contract)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = mgr.executeCodeStage(ctx, StageContract{Name: "implement"}, "autopilot", logFile, "")

	if rt.runCalled != 1 {
		t.Fatalf("expected runtime.Run to be called once, got %d", rt.runCalled)
	}
	if rt.lastInv.AgentName != "autopilot" {
		t.Errorf("expected agent name autopilot, got %q", rt.lastInv.AgentName)
	}
	if rt.lastInv.Limits.MaxTurns == 0 {
		t.Errorf("expected limits to be populated, got %+v", rt.lastInv.Limits)
	}

	// Verify the EventSink updates flowed into RunningJobs.
	infos := sup.RunningJobs()
	var found *RunInfo
	for i := range infos {
		if infos[i].JobID == job.ID {
			found = &infos[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected job %d in RunningJobs, got %+v", job.ID, infos)
	}
	if found.StepCount != 2 {
		t.Errorf("expected StepCount=2 from EventSink, got %d", found.StepCount)
	}
	// CurrentTool/ToolInput were cleared by the final OnToolEnd event.
	if found.CurrentTool != "" {
		t.Errorf("expected CurrentTool cleared after OnToolEnd, got %q", found.CurrentTool)
	}
}

// fakeOpencodeHTTP stands in for `opencode serve`: POST /session, POST
// /session/{id}/message, GET /event. Used by the supervisor→opencode integration
// smoke below so a real stage drives the real opencode runtime over HTTP.
func fakeOpencodeHTTP(t *testing.T) *httptest.Server {
	t.Helper()
	const promptBody = `{"info":{"id":"m1","role":"assistant","sessionID":"ses_fake","cost":0.0123,"tokens":{},"error":{}},"parts":[{"type":"step-start"},{"type":"text","text":"hello from fake"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"ses_fake","title":"t","version":"1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			<-r.Context().Done()
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(promptBody))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestExecuteCodeStage_OpenCodeIntegration is the end-to-end integration smoke
// (per the "integration smoke after refactor" practice): a supervisor code stage
// drives the REAL opencode runtime, which talks to a fake `opencode serve` over
// HTTP, writes its canonical result record to the stage log, and that record
// parses back to the expected normalized Result. Unit-green on each layer is
// necessary but not sufficient for the seam between them.
func TestExecuteCodeStage_OpenCodeIntegration(t *testing.T) {
	srv := fakeOpencodeHTTP(t)

	store := testStore(t)
	deploy := testDeployment(t, store)
	job := testJob(t, store, deploy)

	sup := NewTestSupervisor(store, deploy, deploy.RepoDir)
	rt := &opencodert.OpencodeRuntime{BaseURL: srv.URL}
	sup.SetRuntime(rt)
	sup.RegisterTestJob(job)

	sc := sup.newSlotContext(job.ID, job)
	sc.LogPath = filepath.Join(t.TempDir(), "agent.log")
	sc.Hooks = &TestHooks{
		SetupWorktreeFn:  func() error { return nil },
		EnsureAgentDefFn: func(_ AgentName) (AgentDefSource, error) { return AgentDefBuiltIn, nil },
		DetectPRFn:       func(_ context.Context) int { return 0 },
	}

	logFile, err := os.Create(sc.LogPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	defer func() { _ = logFile.Close() }()

	mgr := NewDefaultJobManager(sc, &AgentContract{Name: "autopilot", Output: "pr"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The stage's own outcome is "no_pr" here (the test's DetectPRFn returns 0),
	// which is downstream of the runtime call and expected. What this smoke
	// asserts is the runtime SEAM: opencode drove to completion (no runtime_error
	// / runtime_config), and its result record parses back below.
	if sr := mgr.executeCodeStage(ctx, StageContract{Name: "implement"}, "autopilot", logFile, ""); sr.reason == "runtime_error" || sr.reason == "runtime_config" {
		t.Fatalf("runtime seam failed: reason=%q detail=%q", sr.reason, sr.detail)
	}
	_ = logFile.Sync()

	// The opencode runtime wrote its result record through the supervisor's stage
	// log; parsing it back closes the seam.
	res, err := rt.ParseResult(sc.LogPath)
	if err != nil {
		t.Fatalf("ParseResult: %v", err)
	}
	if res == nil {
		data, _ := os.ReadFile(sc.LogPath)
		t.Fatalf("ParseResult returned nil; stage log:\n%s", data)
	}
	if res.SessionID != "ses_fake" {
		t.Errorf("SessionID = %q, want ses_fake", res.SessionID)
	}
	if res.FinalText != "hello from fake" {
		t.Errorf("FinalText = %q, want 'hello from fake'", res.FinalText)
	}
	if res.TotalCostUSD != 0.0123 {
		t.Errorf("TotalCostUSD = %v, want 0.0123", res.TotalCostUSD)
	}
}
