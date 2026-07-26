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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	opencodesdk "github.com/sst/opencode-sdk-go"
	"github.com/sst/opencode-sdk-go/option"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

// Name is the canonical identifier for this runtime.
const Name = runtime.NameOpenCode

// resultMarker tags the canonical final record Run appends to the log so
// ParseResult can locate it among the mirrored event stream.
const resultMarker = "agent-minder/opencode-result"

// OpencodeRuntime implements runtime.AgentRuntime for opencode.
//
// The zero value is usable: it drives the process-wide shared `opencode serve`
// via the opencode Go SDK.
type OpencodeRuntime struct {
	// Bin overrides the opencode binary used to start the shared server
	// (default: "opencode").
	Bin string

	// BaseURL, when set, points at an already-running server instead of
	// starting one. Primarily for tests.
	BaseURL string
}

// New constructs an OpencodeRuntime with default settings.
func New() *OpencodeRuntime { return &OpencodeRuntime{} }

func (o *OpencodeRuntime) binPath() string {
	if o.Bin != "" {
		return o.Bin
	}
	return defaultBin
}

// logRecord is the canonical final record appended to the run log. It carries
// the session id (for Resume) and the final assistant message (for ParseResult).
type logRecord struct {
	Marker    string          `json:"marker"`
	SessionID string          `json:"sessionID"`
	ExitCode  int             `json:"exitCode"`
	Info      json.RawMessage `json:"info"`
}

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

// Run drives a single opencode prompt to completion against the shared server.
//
// It ensures a server, opens an SDK client, creates a session scoped to the
// job worktree (via the `directory` param), streams live progress from
// `GET /event` to sink while the prompt runs, and — after Session.Prompt
// returns the final assistant message — appends a canonical result record to
// logFile for ParseResult. The returned exitCode is synthetic: 0 on success,
// 1 when the prompt errors or the assistant message carries an error.
func (o *OpencodeRuntime) Run(ctx context.Context, inv runtime.Invocation, sink runtime.EventSink, logFile io.Writer) (int, error) {
	baseURL := o.BaseURL
	if baseURL == "" {
		var err error
		baseURL, err = sharedManager.ensure(ctx, o.binPath(), inv.Env)
		if err != nil {
			return 0, err
		}
	}

	client := opencodesdk.NewClient(option.WithBaseURL(baseURL))
	dir := inv.Workspace.Dir

	sess, err := client.Session.New(ctx, opencodesdk.SessionNewParams{
		Directory: opencodesdk.F(dir),
		Title:     opencodesdk.F(fmt.Sprintf("agent-minder:%s", inv.AgentName)),
	})
	if err != nil {
		return 0, fmt.Errorf("opencode: create session: %w", err)
	}

	// Stream live events for the sink while the (blocking) prompt runs.
	streamCtx, stopStream := context.WithCancel(ctx)
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		streamEvents(streamCtx, baseURL, sess.ID, sink, logFile)
	}()

	resp, promptErr := client.Session.Prompt(ctx, sess.ID, promptParams(inv, dir))

	stopStream()
	<-streamDone

	exitCode, info := 0, json.RawMessage(nil)
	if resp != nil {
		if raw, mErr := json.Marshal(resp.Info); mErr == nil {
			info = raw
		}
		if resp.Info.Error.Name != "" {
			exitCode = 1
		}
	}
	if promptErr != nil {
		exitCode = 1
	}

	writeLogRecord(logFile, logRecord{
		Marker:    resultMarker,
		SessionID: sess.ID,
		ExitCode:  exitCode,
		Info:      info,
	})

	if promptErr != nil {
		return exitCode, fmt.Errorf("opencode: prompt: %w", promptErr)
	}
	return exitCode, nil
}

// promptParams maps a runtime.Invocation onto opencode's SessionPromptParams.
// Every Invocation field has a direct home: Directory, System, Agent, Model,
// Tools, and the prompt text as a single text part.
func promptParams(inv runtime.Invocation, dir string) opencodesdk.SessionPromptParams {
	p := opencodesdk.SessionPromptParams{
		Directory: opencodesdk.F(dir),
		Parts: opencodesdk.F([]opencodesdk.SessionPromptParamsPartUnion{
			opencodesdk.TextPartInputParam{
				Type: opencodesdk.F(opencodesdk.TextPartInputTypeText),
				Text: opencodesdk.F(inv.Prompt),
			},
		}),
	}
	if inv.AgentName != "" {
		p.Agent = opencodesdk.F(inv.AgentName)
	}
	if inv.SystemPrompt != "" {
		p.System = opencodesdk.F(inv.SystemPrompt)
	}
	if provider, model, ok := splitModel(inv.Model); ok {
		p.Model = opencodesdk.F(opencodesdk.SessionPromptParamsModel{
			ProviderID: opencodesdk.F(provider),
			ModelID:    opencodesdk.F(model),
		})
	}
	if len(inv.AllowedTools) > 0 {
		tools := make(map[string]bool, len(inv.AllowedTools))
		for _, t := range inv.AllowedTools {
			tools[t] = true
		}
		p.Tools = opencodesdk.F(tools)
	}
	return p
}

// splitModel divides an opencode model reference "provider/model" into its
// parts. The provider is the first path segment; the model id is the remainder
// (which may itself contain slashes, e.g. "localmlx/mlx-community/Qwen3.6").
// Returns ok=false when no provider prefix is present, in which case the model
// is omitted and opencode falls back to its configured default.
func splitModel(model string) (provider, id string, ok bool) {
	model = runtime.NormalizeModelName(model)
	if model == "" {
		return "", "", false
	}
	provider, id, found := strings.Cut(model, "/")
	if !found || provider == "" || id == "" {
		return "", "", false
	}
	return provider, id, true
}

func writeLogRecord(w io.Writer, rec logRecord) {
	if w == nil {
		return
	}
	if data, err := json.Marshal(rec); err == nil {
		_, _ = w.Write(data)
		_, _ = w.Write([]byte("\n"))
	}
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
