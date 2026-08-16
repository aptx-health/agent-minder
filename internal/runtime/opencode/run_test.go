package opencode

import (
	"testing"

	opencodesdk "github.com/sst/opencode-sdk-go"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

func TestSplitModel(t *testing.T) {
	cases := []struct {
		in           string
		provider, id string
		ok           bool
	}{
		{"anthropic/claude-3-5-sonnet", "anthropic", "claude-3-5-sonnet", true},
		{"localmlx/mlx-community/Qwen3.6-35B-A3B-4bit-DWQ", "localmlx", "mlx-community/Qwen3.6-35B-A3B-4bit-DWQ", true},
		{"openrouter/anthropic/claude-3-5-sonnet", "openrouter", "anthropic/claude-3-5-sonnet", true},
		{"", "", "", false},
		{"noprovider", "", "", false},
		{"/model", "", "", false},
		{"provider/", "", "", false},
		{"  spaced/model  ", "spaced", "model", true},
	}
	for _, c := range cases {
		p, id, ok := splitModel(c.in)
		if ok != c.ok || p != c.provider || id != c.id {
			t.Errorf("splitModel(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, p, id, ok, c.provider, c.id, c.ok)
		}
	}
}

func TestPromptParamsMapping(t *testing.T) {
	inv := runtime.Invocation{
		AgentName:    "autopilot",
		SystemPrompt: "be careful",
		Model:        "localmlx/qwen",
		Prompt:       "do the thing",
		AllowedTools: []string{"read", "edit"},
	}
	p := promptParams(inv, "/work/dir")

	if got := p.Directory.Value; got != "/work/dir" {
		t.Errorf("Directory = %q, want /work/dir", got)
	}
	if got := p.Agent.Value; got != "autopilot" {
		t.Errorf("Agent = %q, want autopilot", got)
	}
	if got := p.System.Value; got != "be careful" {
		t.Errorf("System = %q, want 'be careful'", got)
	}
	m := p.Model.Value
	if m.ProviderID.Value != "localmlx" || m.ModelID.Value != "qwen" {
		t.Errorf("Model = %q/%q, want localmlx/qwen", m.ProviderID.Value, m.ModelID.Value)
	}
	tools := p.Tools.Value
	if !tools["read"] || !tools["edit"] || len(tools) != 2 {
		t.Errorf("Tools = %v, want {read:true, edit:true}", tools)
	}
	if len(p.Parts.Value) != 1 {
		t.Fatalf("Parts len = %d, want 1", len(p.Parts.Value))
	}
	part, ok := p.Parts.Value[0].(opencodesdk.TextPartInputParam)
	if !ok || part.Text.Value != "do the thing" {
		t.Errorf("Parts[0] = %#v, want text 'do the thing'", p.Parts.Value[0])
	}
}

func TestModelOverrideReachesPromptConfig(t *testing.T) {
	params := promptParams(runtime.Invocation{
		Model:  " openrouter/anthropic/claude-sonnet-4 ",
		Prompt: "do the thing",
	}, "/work/dir")
	if !params.Model.Present {
		t.Fatalf("model override did not reach opencode prompt config: %#v", params.Model)
	}
	model := params.Model.Value
	if model.ProviderID.Value != "openrouter" || model.ModelID.Value != "anthropic/claude-sonnet-4" {
		t.Fatalf("model config = %q/%q, want openrouter/anthropic/claude-sonnet-4",
			model.ProviderID.Value, model.ModelID.Value)
	}
}

func TestPromptParamsOmitsEmptyOptionals(t *testing.T) {
	// No model, agent, system, or tools set: those fields must stay unset so
	// opencode falls back to its configured defaults.
	p := promptParams(runtime.Invocation{Prompt: "hi"}, "/d")
	if p.Model.Present {
		t.Errorf("Model should be absent when Invocation.Model is empty")
	}
	if p.Agent.Present {
		t.Errorf("Agent should be absent when AgentName is empty")
	}
	if p.System.Present {
		t.Errorf("System should be absent when SystemPrompt is empty")
	}
	if p.Tools.Present {
		t.Errorf("Tools should be absent when AllowedTools is empty")
	}
}

func TestBytesCutSSEData(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{`data: {"type":"x"}`, `{"type":"x"}`, true},
		{"data:{\"a\":1}\r", `{"a":1}`, true},
		{"", "", false},
		{"event: message", "", false},
		{"data: ", "", false},
		{": comment", "", false},
	}
	for _, c := range cases {
		got, ok := bytesCutSSEData([]byte(c.in))
		if ok != c.ok || string(got) != c.want {
			t.Errorf("bytesCutSSEData(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestTranslatorEvents(t *testing.T) {
	sink := &recordingSink{}
	tr := &translator{sink: sink, tools: map[string]bool{}}

	step := func(kind string) sseEvent {
		e := sseEvent{Type: "message.part.updated"}
		e.Properties.Part = &ssePart{Type: kind}
		return e
	}
	tr.handle(step("step-start"))
	tr.handle(step("step-finish"))
	if sink.steps != 1 || sink.toolEnds != 1 {
		t.Fatalf("step events: steps=%d toolEnds=%d, want 1/1", sink.steps, sink.toolEnds)
	}

	// A tool lifecycle: two running updates (deduped) then a completion.
	toolEvt := func(status string) sseEvent {
		e := sseEvent{Type: "message.part.updated"}
		p := &ssePart{Type: "tool", Tool: "bash", CallID: "call-1"}
		p.State.Status = status
		e.Properties.Part = p
		return e
	}
	tr.handle(toolEvt("running"))
	tr.handle(toolEvt("running"))
	tr.handle(toolEvt("completed"))
	if sink.toolStarts != 1 {
		t.Errorf("toolStarts = %d, want 1 (deduped by callID)", sink.toolStarts)
	}
	if sink.lastTool != "bash" {
		t.Errorf("lastTool = %q, want bash", sink.lastTool)
	}
	if sink.toolEnds != 2 { // step-finish + tool completion
		t.Errorf("toolEnds = %d, want 2", sink.toolEnds)
	}

	// Usage-limit signal on a message.updated carrying a rate-limit error.
	ul := sseEvent{Type: "message.updated"}
	ul.Properties.Info = []byte(`{"error":{"name":"APIError","data":{"message":"rate limit exceeded"}}}`)
	tr.handle(ul)
	if sink.caps != 1 {
		t.Errorf("usageLimits = %d, want 1", sink.caps)
	}
}

// TestTranslatorToolTerminalFirst covers a tool whose first observed event is
// already terminal (fast tool, or a pending/running event lost to a stream
// reconnect). OnToolStart must still fire so start/end stay balanced — the
// regression behind an observed live run reporting a written file but zero tool
// starts.
func TestTranslatorToolTerminalFirst(t *testing.T) {
	sink := &recordingSink{}
	tr := &translator{sink: sink, tools: map[string]bool{}}

	evt := sseEvent{Type: "message.part.updated"}
	p := &ssePart{Type: "tool", Tool: "write", CallID: "call-x"}
	p.State.Status = "completed"
	evt.Properties.Part = p
	tr.handle(evt)

	if sink.toolStarts != 1 {
		t.Errorf("toolStarts = %d, want 1 (synthesized for terminal-first tool)", sink.toolStarts)
	}
	if sink.toolEnds != 1 {
		t.Errorf("toolEnds = %d, want 1", sink.toolEnds)
	}
	if sink.lastTool != "write" {
		t.Errorf("lastTool = %q, want write", sink.lastTool)
	}

	// A subsequent duplicate terminal event for the same call must not re-fire a
	// start (dedup entry was cleared, but callID reuse in one message is rare and
	// each terminal event is one tool end).
	sink2 := &recordingSink{}
	tr2 := &translator{sink: sink2, tools: map[string]bool{"call-y": true}}
	e2 := sseEvent{Type: "message.part.updated"}
	p2 := &ssePart{Type: "tool", Tool: "bash", CallID: "call-y"}
	p2.State.Status = "error"
	e2.Properties.Part = p2
	tr2.handle(e2)
	if sink2.toolStarts != 0 {
		t.Errorf("toolStarts = %d, want 0 (start already fired for call-y)", sink2.toolStarts)
	}
	if sink2.toolEnds != 1 {
		t.Errorf("toolEnds = %d, want 1", sink2.toolEnds)
	}
}

func TestTranslatorFiltersOtherSessions(t *testing.T) {
	// streamEvents filters by sessionID before calling handle; verify handle
	// itself does not require a session and that a nil sink is safe.
	tr := &translator{sink: nil, tools: map[string]bool{}}
	tr.handle(sseEvent{Type: "message.part.updated"}) // must not panic on nil sink/part
}
