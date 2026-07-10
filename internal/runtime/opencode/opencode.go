// Package opencode is the opencode implementation of runtime.AgentRuntime.
//
// This is a partial adapter: it registers the "opencode" runtime so
// `--runtime opencode` passes early validation, and PrepareAgentDef writes
// native on-disk agent definitions. The execution methods (Run, Resume,
// ParseResult, etc.) are still stubs that return runtime.ErrNotSupported (or
// zero values) until the real adapter — planned around `opencode serve` plus
// its Go SDK — lands in later issues. See design/opencode-mapping.md.
package opencode

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

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

// PrepareAgentDef writes the agent definition body to
// `<workspace>/.opencode/agent/<name>.md`. Idempotent: overwrites if the file
// already exists so updates to the embedded contract are picked up.
//
// If the workspace directory is empty (non-worktree agents that run from the
// repo root with no PrepareAgentDef step), this is a no-op.
//
// Directory note: opencode reads project agents from the SINGULAR
// `.opencode/agent/` directory — this is also what `opencode agent create`
// writes, and matches the global `~/.config/opencode/agent/` layout. Some docs
// say plural `.opencode/agents/`; that is a documentation error, so we write to
// the singular form the runtime actually reads.
func (o *OpencodeRuntime) PrepareAgentDef(_ context.Context, ws runtime.Workspace, def runtime.AgentDefinition) error {
	if ws.Dir == "" {
		return nil
	}
	if def.Name == "" {
		return fmt.Errorf("opencode: PrepareAgentDef: empty agent name")
	}
	if len(def.Body) == 0 {
		return fmt.Errorf("opencode: PrepareAgentDef: empty body for %q", def.Name)
	}
	dir := filepath.Join(ws.Dir, ".opencode", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("opencode: create agent dir: %w", err)
	}
	path := filepath.Join(dir, def.Name+".md")
	if err := os.WriteFile(path, def.Body, 0o644); err != nil {
		return fmt.Errorf("opencode: write agent def: %w", err)
	}
	return nil
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
