package supervisor

import (
	"context"
	"encoding/json"
	"io"
	"os"

	"github.com/aptx-health/agent-minder/internal/agentutil"
	runtimepkg "github.com/aptx-health/agent-minder/internal/runtime"
)

// liveStatusSink is a runtime.EventSink that mirrors the legacy scanStream
// behavior: it updates the supervisor's per-job live status (current tool,
// tool input, step count) and flips the hitUsageLimit flag when the runtime
// reports a usage-limit event. It is safe for the runtime's reader goroutine
// to call concurrently with the supervisor lock.
type liveStatusSink struct {
	sup        *Supervisor
	jobID      int64
	usageLimit bool
}

func newLiveStatusSink(sup *Supervisor, jobID int64) *liveStatusSink {
	return &liveStatusSink{sup: sup, jobID: jobID}
}

// OnAssistantStep advances the step counter so minder status / TUI surface
// progress while the agent works, and persists live progress to the active
// agent_runs row so the read API sees it before the run ends.
func (s *liveStatusSink) OnAssistantStep() {
	s.sup.mu.Lock()
	var runID int64
	var steps int
	if rs, ok := s.sup.running[s.jobID]; ok {
		rs.liveStatus.StepCount++
		rs.runStepCount++
		runID = rs.currentRunID
		steps = rs.runStepCount
	}
	s.sup.mu.Unlock()

	// Persist outside the lock — the DB write serializes on the single-writer
	// connection, and a failed touch must never stall the streaming reader.
	if runID != 0 && s.sup.store != nil {
		_ = s.sup.store.TouchAgentRun(runID, steps)
	}
}

// OnToolStart records the current tool the agent is invoking.
func (s *liveStatusSink) OnToolStart(name, inputSummary string) {
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	if rs, ok := s.sup.running[s.jobID]; ok {
		rs.liveStatus.CurrentTool = name
		rs.liveStatus.ToolInput = inputSummary
	}
}

// OnToolEnd clears the current tool field.
func (s *liveStatusSink) OnToolEnd() {
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	if rs, ok := s.sup.running[s.jobID]; ok {
		rs.liveStatus.CurrentTool = ""
		rs.liveStatus.ToolInput = ""
	}
}

// OnUsageLimit marks both this sink and the supervisor's run state so that
// jobmanager's recovery loop can pick it up via HitUsageLimit().
func (s *liveStatusSink) OnUsageLimit() {
	s.usageLimit = true
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	if rs, ok := s.sup.running[s.jobID]; ok {
		rs.hitUsageLimit = true
	}
}

// runtimeInvocationFor builds a runtime.Invocation for a stage from existing
// SlotContext / job / deploy data. It mirrors buildAgentArgs so the runtime
// receives equivalent limits, tools, prompts, and env.
func runtimeInvocationFor(sc *SlotContext, agentName, prompt, systemPrompt string) runtimepkg.Invocation {
	job := sc.Job
	maxTurns := job.EffectiveMaxTurns(sc.Deploy)
	maxBudget := job.EffectiveMaxBudget(sc.Deploy)
	if agentName == "reviewer" {
		if sc.Deploy.ReviewMaxTurns.Valid {
			maxTurns = int(sc.Deploy.ReviewMaxTurns.Int64)
		}
		if sc.Deploy.ReviewMaxBudget.Valid {
			maxBudget = sc.Deploy.ReviewMaxBudget.Float64
		}
	}

	wsDir := sc.WorktreePath
	if wsDir == "" || !dirExists(wsDir) {
		wsDir = sc.RepoDir
	}

	env := map[string]string{}
	if sc.GHToken != "" {
		env["GITHUB_TOKEN"] = sc.GHToken
	}
	model := ""
	if agentName == job.Agent {
		model = job.EffectiveModel()
	}

	return runtimepkg.Invocation{
		Workspace:    runtimepkg.Workspace{Dir: wsDir},
		AgentName:    agentName,
		Model:        model,
		Prompt:       prompt,
		SystemPrompt: systemPrompt,
		AllowedTools: sc.AllowedTools,
		Limits: runtimepkg.Limits{
			MaxTurns:     maxTurns,
			MaxBudgetUSD: maxBudget,
		},
		Env: env,
	}
}

// adaptRuntimeResult converts a runtime.Result into the legacy AgentResult
// shape that the existing supervisor helpers (classifyOutcome, bail extraction
// fallbacks) understand.
func adaptRuntimeResult(r *runtimepkg.Result) *agentutil.AgentResult {
	if r == nil {
		return nil
	}
	denials := make([]json.RawMessage, 0, len(r.PermissionDenials))
	for _, d := range r.PermissionDenials {
		if d == "" {
			continue
		}
		denials = append(denials, json.RawMessage(d))
	}
	return &agentutil.AgentResult{
		IsError:           r.IsError,
		NumTurns:          r.NumTurns,
		TotalCost:         r.TotalCostUSD,
		StopReason:        json.RawMessage(`"` + r.StopReason + `"`),
		Result:            r.FinalText,
		PermissionDenials: denials,
		SessionID:         r.SessionID,
	}
}

// runStageThroughRuntime runs the given invocation through the configured
// AgentRuntime, streaming events to the supervisor's live status via sink.
//
// Returns the parsed result, sink (so callers can read usage-limit), and exit
// code. ParseResult / ClassifyOutcome / ExtractBailReport are intentionally
// left to the caller, which already has the limits + result context.
//
// When SlotContext.Hooks.RunStageFn is set (tests only), the runtime call is
// short-circuited: the hook supplies the exit code + parsed result + usage-
// limit flag directly. The sink is still constructed so callers can read
// usageLimit uniformly.
func (sc *SlotContext) runStageThroughRuntime(
	ctx context.Context,
	inv runtimepkg.Invocation,
	logFile io.Writer,
) (int, *runtimepkg.Result, *liveStatusSink, error) {
	sink := newLiveStatusSink(sc.sup, sc.Job.ID)
	if sc.Hooks != nil && sc.Hooks.RunStageFn != nil {
		f, ok := logFile.(*os.File)
		if !ok {
			f = nil
		}
		exit, result, usageLimit, err := sc.Hooks.RunStageFn(ctx, inv, f)
		if usageLimit {
			sink.OnUsageLimit()
		}
		return exit, result, sink, err
	}
	rt, err := sc.sup.RuntimeForJob(sc.Job)
	if err != nil {
		return 0, nil, sink, err
	}
	if rt == nil {
		return 0, nil, sink, runtimepkg.ErrNotSupported
	}
	exit, err := rt.Run(ctx, inv, sink, logFile)
	result, _ := rt.ParseResult(sc.LogPath)
	return exit, result, sink, err
}

// resumeThroughRuntime drives the runtime's session-resume path during usage-
// limit recovery. Returns the parsed result, whether the resumed session also
// hit a limit, and an error (ErrNotSupported when the runtime cannot resume).
//
// Tests may inject SlotContext.Hooks.ResumeFn to skip the actual runtime call
// and supply a synthetic result.
func (sc *SlotContext) resumeThroughRuntime(
	ctx context.Context,
	sessionID string,
	logFile io.Writer,
) (*runtimepkg.Result, bool, error) {
	if sc.Hooks != nil && sc.Hooks.ResumeFn != nil {
		f, ok := logFile.(*os.File)
		if !ok {
			f = nil
		}
		result, usageLimit, err := sc.Hooks.ResumeFn(ctx, sessionID, f)
		return result, usageLimit, err
	}
	rt, err := sc.sup.RuntimeForJob(sc.Job)
	if err != nil {
		return nil, false, err
	}
	if rt == nil {
		return nil, false, runtimepkg.ErrNotSupported
	}
	sink := newLiveStatusSink(sc.sup, sc.Job.ID)
	if _, err := rt.Resume(ctx, sessionID, sink, logFile); err != nil {
		return nil, sink.usageLimit, err
	}
	result, _ := rt.ParseResult(sc.LogPath)
	return result, sink.usageLimit, nil
}
