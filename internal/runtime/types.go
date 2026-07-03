// Package runtime defines the doer execution boundary for agent-minder.
//
// An AgentRuntime adapts a concrete agent process (Claude Code, Codex, etc.)
// to the contract the supervisor expects: prepare an agent definition,
// run a single invocation, normalize the event stream, parse the result,
// and classify the outcome.
//
// This package contains the interface and supporting types only. Concrete
// runtimes live in sibling packages (e.g., internal/runtime/claudecode).
package runtime

import (
	"encoding/json"
	"time"
)

// Workspace is the directory the runtime executes in. For worktree-using
// agents this is the worktree path; for non-worktree agents (output: issue
// or comment) it is the repo root.
type Workspace struct {
	Dir string
}

// AgentDefinition is a runtime-agnostic agent contract that PrepareAgentDef
// translates into the runtime's native on-disk form.
type AgentDefinition struct {
	Name   string // contract identifier, e.g. "autopilot"
	Source string // origin path or label, for diagnostics
	Body   []byte // raw contract body (frontmatter + markdown)
}

// Invocation is a fully-specified single run request.
type Invocation struct {
	Workspace    Workspace
	AgentName    string   // must match a prior PrepareAgentDef
	Model        string   // optional runtime-native model name or alias
	Prompt       string   // assembled user prompt / context
	SystemPrompt string   // optional system-prompt append (e.g., lessons)
	AllowedTools []string // runtime translates to its own gating; may ignore
	Limits       Limits
	Env          map[string]string // additional env vars (e.g., GITHUB_TOKEN)
}

// Limits caps a single run. A zero value means no cap (runtime default).
type Limits struct {
	MaxTurns     int
	MaxBudgetUSD float64
	Timeout      time.Duration
}

// Result is the normalized post-run summary parsed from the runtime log.
type Result struct {
	SessionID         string // for Resume; empty if runtime has no session concept
	NumTurns          int
	TotalCostUSD      float64
	FinalText         string // last assistant message text (where bail-report lives)
	IsError           bool
	StopReason        string
	PermissionDenials []string
	Native            json.RawMessage // raw runtime-native result event, for debugging
}

// Outcome categorizes a Result against the run's Limits.
//
// Status values:
//   - ""        no failure detected
//   - "warning" non-fatal issue (e.g., permission denials); caller should still check for a PR
//   - "failed"  fatal failure
type Outcome struct {
	Status        string
	FailureReason string // "max_turns" | "max_budget" | "error" | "permissions" | ...
	FailureDetail string
}

// BailReport is the Minder-defined <bail-report> JSON the agent emits when it
// decides it cannot complete the work. The shape is runtime-agnostic; runtimes
// know where to look for it (final message vs. raw log scan).
type BailReport struct {
	Reason string          // agent-supplied category
	Detail string          // agent-supplied free text
	Native json.RawMessage // raw JSON block, for forensics
}

// EventSink receives normalized events as the agent runs. Implementations
// must be safe to call from the runtime's reader goroutine.
type EventSink interface {
	// OnAssistantStep is called once per assistant message.
	OnAssistantStep()

	// OnToolStart is called when the agent begins a tool call.
	// inputSummary is a truncated, displayable summary of the tool input.
	OnToolStart(name, inputSummary string)

	// OnToolEnd is called when an assistant message has no further tool_use
	// (the agent is idle or has finished).
	OnToolEnd()

	// OnUsageLimit is called when the runtime detects a rate limit, billing
	// error, or other usage-limit signal mid-run.
	OnUsageLimit()
}
