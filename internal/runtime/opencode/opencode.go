// Package opencode is the opencode implementation of runtime.AgentRuntime.
//
// This is a skeleton: it registers the "opencode" runtime so
// `--runtime opencode` passes early validation, but the execution methods are
// stubs. They return runtime.ErrNotSupported (or zero values) until the real
// adapter — planned around `opencode serve` plus its Go SDK — lands in later
// issues. See design/opencode-mapping.md.
package opencode

import (
	"context"
	"io"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

// Name is the canonical identifier for this runtime.
const Name = runtime.NameOpenCode

// OpencodeRuntime implements runtime.AgentRuntime for opencode.
//
// The zero value is usable; fields will be added as the concrete adapter is
// built out.
type OpencodeRuntime struct{}

// New constructs an OpencodeRuntime with default settings.
func New() *OpencodeRuntime { return &OpencodeRuntime{} }

func init() {
	runtime.Register(Name, func() runtime.AgentRuntime { return New() })
}

// Name returns the runtime identifier.
func (o *OpencodeRuntime) Name() string { return Name }

// PrepareAgentDef is not yet implemented.
func (o *OpencodeRuntime) PrepareAgentDef(_ context.Context, _ runtime.Workspace, _ runtime.AgentDefinition) error {
	return runtime.ErrNotSupported
}

// Run is not yet implemented.
func (o *OpencodeRuntime) Run(_ context.Context, _ runtime.Invocation, _ runtime.EventSink, _ io.Writer) (int, error) {
	return 0, runtime.ErrNotSupported
}

// Resume is not yet implemented.
func (o *OpencodeRuntime) Resume(_ context.Context, _ string, _ runtime.EventSink, _ io.Writer) (int, error) {
	return 0, runtime.ErrNotSupported
}

// ParseResult is not yet implemented.
func (o *OpencodeRuntime) ParseResult(_ string) (*runtime.Result, error) {
	return nil, runtime.ErrNotSupported
}

// ClassifyOutcome is not yet implemented.
func (o *OpencodeRuntime) ClassifyOutcome(_ *runtime.Result, _ runtime.Limits) runtime.Outcome {
	return runtime.Outcome{}
}

// ExtractBailReport is not yet implemented.
func (o *OpencodeRuntime) ExtractBailReport(_ *runtime.Result, _ string) *runtime.BailReport {
	return nil
}
