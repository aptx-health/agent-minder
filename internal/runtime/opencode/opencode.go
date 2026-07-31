// Package opencode is the opencode implementation of runtime.AgentRuntime.
//
// It drives a process-wide shared `opencode serve` via the opencode Go SDK:
// PrepareAgentDef writes native agent definitions, Run/Resume create or continue
// a session and stream progress to the sink while mirroring events (and a final
// result record) to the log, ParseResult/ClassifyOutcome lift the normalized
// result and outcome, and ExtractBailReport recovers the embedded <bail-report>.
// See design/opencode-mapping.md.
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
// the session id (for Resume), the final assistant message (cost/tokens/error),
// and its parts (final text + step count) for ParseResult.
type logRecord struct {
	Marker    string          `json:"marker"`
	SessionID string          `json:"sessionID"`
	ExitCode  int             `json:"exitCode"`
	Info      json.RawMessage `json:"info"`
	Parts     json.RawMessage `json:"parts"`
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
	baseURL, err := o.resolveServer(ctx, inv.Env)
	if err != nil {
		return 0, err
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

	return o.driveSession(ctx, client, baseURL, sess.ID, promptParams(inv, dir), sink, logFile)
}

// Resume continues a prior opencode session by id, typically after a usage-limit
// wait. opencode has no no-input continuation, but the session retains its full
// message history, so Resume re-prompts the same session with a continuation
// nudge — preserving the accumulated context (unlike a fresh Run). It reuses the
// shared server (already running in-process from the original Run) and writes a
// fresh result record that ParseResult's last-record-wins scan picks up.
func (o *OpencodeRuntime) Resume(ctx context.Context, sessionID string, sink runtime.EventSink, logFile io.Writer) (int, error) {
	if sessionID == "" {
		return 0, fmt.Errorf("opencode: Resume: empty session id")
	}
	baseURL, err := o.resolveServer(ctx, nil)
	if err != nil {
		return 0, err
	}
	client := opencodesdk.NewClient(option.WithBaseURL(baseURL))
	return o.driveSession(ctx, client, baseURL, sessionID, resumeParams(), sink, logFile)
}

// resolveServer returns the base URL of the server to drive: an explicit
// BaseURL override (tests) or the lazily-started shared server.
func (o *OpencodeRuntime) resolveServer(ctx context.Context, env map[string]string) (string, error) {
	if o.BaseURL != "" {
		return o.BaseURL, nil
	}
	return sharedManager.ensure(ctx, o.binPath(), env)
}

// driveSession runs one prompt against an existing session: it streams live
// progress to sink while Session.Prompt blocks, then appends the canonical
// result record to logFile. Shared by Run (fresh session) and Resume (existing
// session). exitCode is 0 on success, 1 when the prompt errors or the assistant
// message carries an error.
func (o *OpencodeRuntime) driveSession(ctx context.Context, client *opencodesdk.Client, baseURL, sessionID string, params opencodesdk.SessionPromptParams, sink runtime.EventSink, logFile io.Writer) (int, error) {
	streamCtx, stopStream := context.WithCancel(ctx)
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		streamEvents(streamCtx, baseURL, sessionID, sink, logFile)
	}()

	resp, promptErr := client.Session.Prompt(ctx, sessionID, params)

	stopStream()
	<-streamDone

	exitCode, info, parts := 0, json.RawMessage(nil), json.RawMessage(nil)
	if resp != nil {
		if raw, mErr := json.Marshal(resp.Info); mErr == nil {
			info = raw
		}
		if raw, mErr := json.Marshal(resp.Parts); mErr == nil {
			parts = raw
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
		SessionID: sessionID,
		ExitCode:  exitCode,
		Info:      info,
		Parts:     parts,
	})

	if promptErr != nil {
		return exitCode, fmt.Errorf("opencode: prompt: %w", promptErr)
	}
	return exitCode, nil
}

// resumePrompt nudges the model to continue an interrupted session. The session
// already holds the original task and any partial progress, so no task context
// is repeated here.
const resumePrompt = "Continue the task from where you left off; the previous turn was interrupted before it finished."

func resumeParams() opencodesdk.SessionPromptParams {
	return opencodesdk.SessionPromptParams{
		Parts: opencodesdk.F([]opencodesdk.SessionPromptParamsPartUnion{
			opencodesdk.TextPartInputParam{
				Type: opencodesdk.F(opencodesdk.TextPartInputTypeText),
				Text: opencodesdk.F(resumePrompt),
			},
		}),
	}
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
