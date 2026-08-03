package runtime

import (
	"context"
	"io"
	"strings"
	"testing"
)

// stubRuntime is a minimal AgentRuntime used only to populate the registry
// inside tests. Method bodies are inert because these tests only exercise
// Register/Lookup/Validate behavior, not the runtime contract itself.
type stubRuntime struct{ name string }

func (s *stubRuntime) Name() string { return s.name }
func (s *stubRuntime) PrepareAgentDef(context.Context, Workspace, AgentDefinition) error {
	return nil
}
func (s *stubRuntime) Run(context.Context, Invocation, EventSink, io.Writer) (int, error) {
	return 0, nil
}
func (s *stubRuntime) Resume(context.Context, string, EventSink, io.Writer) (int, error) {
	return 0, ErrNotSupported
}
func (s *stubRuntime) ParseResult(string) (*Result, error)           { return nil, nil }
func (s *stubRuntime) ClassifyOutcome(*Result, Limits) Outcome       { return Outcome{} }
func (s *stubRuntime) ExtractBailReport(*Result, string) *BailReport { return nil }

// resetRegistry restores the registry to a clean state and re-registers
// claude-code with a stub factory so tests that exercise DefaultName see a
// resolvable runtime even though this package never imports claudecode.
func resetRegistry(t *testing.T) {
	t.Helper()
	registryMu.Lock()
	factories = map[string]Factory{}
	registryMu.Unlock()
	Register(NameClaudeCode, func() AgentRuntime { return &stubRuntime{name: NameClaudeCode} })
}

func TestDefaultNameIsKnown(t *testing.T) {
	resetRegistry(t)
	if err := Validate(DefaultName); err != nil {
		t.Fatalf("DefaultName %q must validate: %v", DefaultName, err)
	}
	if DefaultName != NameClaudeCode {
		t.Fatalf("DefaultName changed unexpectedly: got %q, want %q", DefaultName, NameClaudeCode)
	}
}

func TestValidateKnown(t *testing.T) {
	resetRegistry(t)
	for _, n := range KnownNames() {
		if err := Validate(n); err != nil {
			t.Errorf("Validate(%q) returned error: %v", n, err)
		}
	}
}

func TestValidateRejectsUnknown(t *testing.T) {
	resetRegistry(t)
	cases := []string{"codex", "Claude-Code", "CLAUDE-CODE", "claude_code", "gpt", "  claude-code"}
	for _, c := range cases {
		err := Validate(c)
		if err == nil {
			t.Errorf("Validate(%q) accepted unknown runtime", c)
			continue
		}
		if !strings.Contains(err.Error(), "unknown runtime") {
			t.Errorf("Validate(%q) error %q missing %q", c, err, "unknown runtime")
		}
	}
}

func TestValidateRejectsEmpty(t *testing.T) {
	resetRegistry(t)
	err := Validate("")
	if err == nil {
		t.Fatal("Validate(\"\") accepted empty string")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("Validate(\"\") error %q missing %q", err, "empty")
	}
}

func TestRuntimeNameConstants(t *testing.T) {
	cases := map[string]string{
		"claude-code": NameClaudeCode,
		"codex":       NameCodex,
		"opencode":    NameOpenCode,
	}
	for want, got := range cases {
		if got != want {
			t.Errorf("runtime name constant = %q, want %q", got, want)
		}
	}
}

func TestKnownNamesIncludesRegistered(t *testing.T) {
	resetRegistry(t)
	Register(NameOpenCode, func() AgentRuntime { return &stubRuntime{name: NameOpenCode} })
	names := KnownNames()
	found := false
	for _, n := range names {
		if n == NameOpenCode {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("KnownNames does not include %q: %v", NameOpenCode, names)
	}
}

func TestKnownNamesIncludesClaudeCode(t *testing.T) {
	resetRegistry(t)
	names := KnownNames()
	if len(names) == 0 {
		t.Fatal("KnownNames returned empty slice")
	}
	found := false
	for _, n := range names {
		if n == NameClaudeCode {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("KnownNames does not include %q: %v", NameClaudeCode, names)
	}
}

func TestLookupReturnsInstance(t *testing.T) {
	resetRegistry(t)
	rt, err := Lookup(NameClaudeCode)
	if err != nil {
		t.Fatalf("Lookup(%q) returned error: %v", NameClaudeCode, err)
	}
	if rt == nil {
		t.Fatalf("Lookup(%q) returned nil runtime", NameClaudeCode)
	}
	if rt.Name() != NameClaudeCode {
		t.Errorf("Lookup runtime Name = %q, want %q", rt.Name(), NameClaudeCode)
	}
}

func TestLookupRejectsUnknown(t *testing.T) {
	resetRegistry(t)
	rt, err := Lookup("codex")
	if err == nil {
		t.Fatal("Lookup(\"codex\") accepted unknown runtime")
	}
	if rt != nil {
		t.Error("Lookup returned non-nil runtime on error")
	}
	if !strings.Contains(err.Error(), "unknown runtime") {
		t.Errorf("Lookup error %q missing %q", err, "unknown runtime")
	}
}

func TestLookupRejectsEmpty(t *testing.T) {
	resetRegistry(t)
	rt, err := Lookup("")
	if err == nil {
		t.Fatal("Lookup(\"\") accepted empty name")
	}
	if rt != nil {
		t.Error("Lookup returned non-nil runtime on error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("Lookup error %q missing %q", err, "empty")
	}
}

func TestValidateModelName(t *testing.T) {
	valid := []string{"", "opus", "gpt-5", "claude-opus-4-1-20250805", "provider/model:v1", " gpt-5 "}
	for _, name := range valid {
		if err := ValidateModelName(name); err != nil {
			t.Errorf("ValidateModelName(%q) returned error: %v", name, err)
		}
	}

	invalid := []string{"opus latest", "gpt;rm", "$(gpt)", "model,other"}
	for _, name := range invalid {
		err := ValidateModelName(name)
		if err == nil {
			t.Errorf("ValidateModelName(%q) accepted invalid model", name)
			continue
		}
		if !strings.Contains(err.Error(), "invalid model") {
			t.Errorf("ValidateModelName(%q) error %q missing invalid model", name, err)
		}
	}
}

func TestShutdownAllInvokesHooksInOrder(t *testing.T) {
	registryMu.Lock()
	saved := shutdownHooks
	shutdownHooks = nil
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		shutdownHooks = saved
		registryMu.Unlock()
	})

	var order []string
	RegisterShutdown(func() { order = append(order, "a") })
	RegisterShutdown(nil) // ignored, must not panic or add an entry
	RegisterShutdown(func() { order = append(order, "b") })

	ShutdownAll()
	if got := strings.Join(order, ","); got != "a,b" {
		t.Fatalf("hooks ran in order %q, want %q", got, "a,b")
	}

	// Safe to call more than once (shutdown paths may overlap).
	ShutdownAll()
	if got := strings.Join(order, ","); got != "a,b,a,b" {
		t.Fatalf("second ShutdownAll order %q, want %q", got, "a,b,a,b")
	}
}

func TestShutdownAllNoHooksIsSafe(t *testing.T) {
	registryMu.Lock()
	saved := shutdownHooks
	shutdownHooks = nil
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		shutdownHooks = saved
		registryMu.Unlock()
	})
	ShutdownAll() // must not panic with no registered hooks
}

func TestRegisterRejectsDuplicate(t *testing.T) {
	resetRegistry(t)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Register did not panic on duplicate name")
		}
	}()
	Register(NameClaudeCode, func() AgentRuntime { return &stubRuntime{name: NameClaudeCode} })
}
