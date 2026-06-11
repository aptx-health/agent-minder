package supervisor

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	runtimepkg "github.com/aptx-health/agent-minder/internal/runtime"
)

// fakeRuntime is a runtime.AgentRuntime stub used to verify that
// DefaultJobManager drives stages through the runtime contract and that
// EventSink events surface as live status updates.
type fakeRuntime struct {
	runCalled int
	lastInv   runtimepkg.Invocation
}

func (f *fakeRuntime) Name() string { return "fake" }

func (f *fakeRuntime) PrepareAgentDef(_ context.Context, _ runtimepkg.Workspace, _ runtimepkg.AgentDefinition) error {
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

	// Drain emitted events in the background to keep the channel from blocking.
	go func() {
		for range sup.Events() {
		}
	}()

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
