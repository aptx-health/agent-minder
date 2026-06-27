package codex

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

type stubSink struct {
	steps       int
	toolStarts  []string
	toolInputs  []string
	toolEnds    int
	usageLimits int
}

func (s *stubSink) OnAssistantStep() { s.steps++ }
func (s *stubSink) OnToolStart(name, inputSummary string) {
	s.toolStarts = append(s.toolStarts, name)
	s.toolInputs = append(s.toolInputs, inputSummary)
}
func (s *stubSink) OnToolEnd()    { s.toolEnds++ }
func (s *stubSink) OnUsageLimit() { s.usageLimits++ }

func TestScanStream_NormalizesCodexEvents(t *testing.T) {
	lines := []string{
		`{"type":"thread.started","thread_id":"thread-1"}`,
		`{"type":"item.started","item":{"type":"command_execution","name":"shell","input":{"command":"go test ./..."}}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":25}}`,
	}
	input := strings.Join(lines, "\n") + "\n"

	var logBuf bytes.Buffer
	sink := &stubSink{}
	var state codexRunState
	scanStream(strings.NewReader(input), &logBuf, sink, &state, runtime.Limits{}, nil)

	if logBuf.String() != input {
		t.Errorf("log mirror = %q, want %q", logBuf.String(), input)
	}
	if state.SessionID != "thread-1" {
		t.Errorf("SessionID = %q, want thread-1", state.SessionID)
	}
	if state.FinalText != "done" {
		t.Errorf("FinalText = %q, want done", state.FinalText)
	}
	if state.NumTurns != 1 {
		t.Errorf("NumTurns = %d, want 1", state.NumTurns)
	}
	if state.InputTok != 100 || state.OutputTok != 25 {
		t.Errorf("usage = (%d,%d), want (100,25)", state.InputTok, state.OutputTok)
	}
	if len(sink.toolStarts) != 1 || sink.toolStarts[0] != "shell" {
		t.Errorf("toolStarts = %v, want [shell]", sink.toolStarts)
	}
	if len(sink.toolInputs) != 1 || sink.toolInputs[0] != "go test ./..." {
		t.Errorf("toolInputs = %v, want [go test ./...]", sink.toolInputs)
	}
	if sink.toolEnds != 1 {
		t.Errorf("toolEnds = %d, want 1", sink.toolEnds)
	}
	if sink.steps != 1 {
		t.Errorf("steps = %d, want 1", sink.steps)
	}
}

func TestScanStream_RecordsErrorsAndUsageLimit(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"turn.failed","error":{"message":"rate limit exceeded"}}`,
		`{"type":"item.completed","item":{"type":"error","text":"billing quota reached"}}`,
	}, "\n") + "\n"

	var state codexRunState
	sink := &stubSink{}
	scanStream(strings.NewReader(input), nil, sink, &state, runtime.Limits{}, nil)

	if !state.IsError {
		t.Fatal("expected IsError")
	}
	if !strings.Contains(state.ErrorText, "billing") {
		t.Errorf("ErrorText = %q, want latest billing error", state.ErrorText)
	}
	if sink.usageLimits != 2 {
		t.Errorf("usageLimits = %d, want 2", sink.usageLimits)
	}
}

func TestScanStream_UnknownEventsArePreserved(t *testing.T) {
	var state codexRunState
	scanStream(strings.NewReader(`{"type":"future.event","seq":7}`+"\n"), nil, nil, &state, runtime.Limits{}, nil)
	if len(state.Unknown) != 1 || !strings.Contains(state.Unknown[0], "future.event") {
		t.Errorf("Unknown = %v, want future.event entry", state.Unknown)
	}
}

func TestScanStream_CurrentCodexAliases(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"session_meta","thread_id":"thread-2","model":"gpt-5.4"}`,
		`{"type":"response_item","item":{"type":"custom_tool_call","name":"apply_patch","input":{"description":"edit file"}}}`,
		`{"type":"response_item","item":{"type":"custom_tool_call_output","call_id":"call-1"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","usage":{"input_tokens":10,"cached_input_tokens":2,"output_tokens":3}}}`,
	}, "\n") + "\n"

	var state codexRunState
	sink := &stubSink{}
	scanStream(strings.NewReader(input), nil, sink, &state, runtime.Limits{}, nil)

	if state.SessionID != "thread-2" || state.Model != "gpt-5.4" {
		t.Errorf("state meta = (%q,%q), want (thread-2,gpt-5.4)", state.SessionID, state.Model)
	}
	if state.InputTok != 10 || state.CachedTok != 2 || state.OutputTok != 3 {
		t.Errorf("usage = (%d,%d,%d), want (10,2,3)", state.InputTok, state.CachedTok, state.OutputTok)
	}
	if len(sink.toolStarts) != 1 || sink.toolStarts[0] != "apply_patch" {
		t.Errorf("toolStarts = %v, want [apply_patch]", sink.toolStarts)
	}
	if sink.toolEnds != 1 {
		t.Errorf("toolEnds = %d, want 1", sink.toolEnds)
	}
}

func TestScanStream_MaxTurnsCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var state codexRunState
	scanStream(strings.NewReader(`{"type":"turn.completed"}`+"\n"), nil, nil, &state, runtime.Limits{MaxTurns: 1}, cancel)

	select {
	case <-ctx.Done():
	default:
		t.Fatal("expected context to be cancelled after max turns")
	}
	if state.NumTurns != 1 {
		t.Errorf("NumTurns = %d, want 1", state.NumTurns)
	}
}
