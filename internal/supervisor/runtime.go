package supervisor

import (
	"context"
	"encoding/json"
	"io"

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
// progress while the agent works.
func (s *liveStatusSink) OnAssistantStep() {
	s.sup.mu.Lock()
	defer s.sup.mu.Unlock()
	if rs, ok := s.sup.running[s.jobID]; ok {
		rs.liveStatus.StepCount++
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

	return runtimepkg.Invocation{
		Workspace:    runtimepkg.Workspace{Dir: wsDir},
		AgentName:    agentName,
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
func (sc *SlotContext) runStageThroughRuntime(
	ctx context.Context,
	inv runtimepkg.Invocation,
	logFile io.Writer,
) (int, *runtimepkg.Result, *liveStatusSink, error) {
	rt := sc.sup.Runtime()
	sink := newLiveStatusSink(sc.sup, sc.Job.ID)
	exit, err := rt.Run(ctx, inv, sink, logFile)
	result, _ := rt.ParseResult(sc.LogPath)
	return exit, result, sink, err
}
