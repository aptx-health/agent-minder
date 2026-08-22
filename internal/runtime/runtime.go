package runtime

import (
	"context"
	"errors"
	"io"
)

// ErrNotSupported is returned by runtime methods for optional capabilities
// (e.g., Resume) that a particular runtime does not implement. Callers must
// handle this by falling back to a runtime-agnostic recovery path.
var ErrNotSupported = errors.New("runtime: capability not supported")

// AgentRuntime is the doer execution boundary. It abstracts the agent process,
// its event stream, its result, and its outcome classification so the
// supervisor can drive multiple runtimes through one contract.
//
// Topology (see docs/research/fable-expedition/02-worker-process-topology-adr.md
// §6 for the full ADR):
//
//   - I-4: process-global equals deployment-global. A runtime adapter may own
//     package-global shared resources (e.g., opencode's shared `opencode
//     serve` process) and register process-level teardown via
//     RegisterShutdown, because one process hosts exactly one Coordinator
//     driving exactly one deployment. Implementations may rely on this — do
//     not "fix" a runtime-owned singleton toward per-call scoping without
//     first checking whether the process-per-deployment invariant still
//     holds.
//   - I-5: anything that must be shared *across* deployments on a host (a
//     local inference server, a shared model cache) is an external service
//     configured per deployment — never a runtime-owned child process. If a
//     resource needs cross-deployment sharing, it does not belong behind
//     this interface.
//
// Lifecycle for a single stage:
//
//  1. PrepareAgentDef once per agent referenced by the stage pipeline.
//  2. Run the invocation; events flow to sink, raw stream to logFile.
//  3. ParseResult from logFile.
//  4. ClassifyOutcome on the parsed result.
//  5. If the outcome indicates the agent bailed, ExtractBailReport.
//  6. On usage-limit recovery, Resume with the SessionID from step 3
//     (or fall back to a fresh Run if the runtime returns ErrNotSupported).
type AgentRuntime interface {
	// Name identifies the runtime in logs, CLI flags, and stored metadata
	// (e.g., "claude-code", "codex").
	Name() string

	// Capabilities reports local runtime availability, model support, and
	// feature flags. Callers should normally use CachedCapabilities so slow CLI
	// probes run at most once per process.
	Capabilities(ctx context.Context) (RuntimeCapabilities, error)

	// PrepareAgentDef materializes an agent definition into the workspace in
	// whatever form this runtime expects (e.g., .claude/agents/<name>.md for
	// Claude Code). Idempotent. Called once per agent before Run.
	PrepareAgentDef(ctx context.Context, ws Workspace, def AgentDefinition) error

	// Run executes the agent until exit. Normalized events are pushed to sink
	// as they happen. The raw runtime-native stream is written to logFile for
	// forensics and post-hoc parsing by ParseResult.
	//
	// exitCode is the underlying process exit code; err is non-nil only on
	// failures to start, pipe, or wait on the process (not on agent-level
	// errors, which are surfaced via ParseResult).
	Run(ctx context.Context, inv Invocation, sink EventSink, logFile io.Writer) (exitCode int, err error)

	// Resume restarts a prior session by ID, typically after a usage-limit
	// wait. The runtime continues writing to logFile and pushing to sink as
	// in Run.
	//
	// Runtimes that do not support session resume must return ErrNotSupported.
	// The caller will fall back to a fresh Run.
	Resume(ctx context.Context, sessionID string, sink EventSink, logFile io.Writer) (exitCode int, err error)

	// ParseResult reads the raw log written by Run/Resume and extracts the
	// normalized final result. Returns (nil, nil) if no result event is found
	// (e.g., the agent crashed before emitting one).
	ParseResult(logPath string) (*Result, error)

	// ClassifyOutcome maps a Result against the run's Limits to a normalized
	// Outcome. An empty Outcome.Status means no failure was detected and the
	// caller should proceed (e.g., check for a PR).
	ClassifyOutcome(r *Result, limits Limits) Outcome

	// ExtractBailReport finds the <bail-report> JSON the agent embeds in its
	// final message. The runtime knows where the final message lives (Claude's
	// result.Result vs. the equivalent for other runtimes) and falls back to
	// scanning the raw log. Returns nil if no report is present.
	ExtractBailReport(r *Result, logPath string) *BailReport
}
