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

func TestRegisterRejectsDuplicate(t *testing.T) {
	resetRegistry(t)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Register did not panic on duplicate name")
		}
	}()
	Register(NameClaudeCode, func() AgentRuntime { return &stubRuntime{name: NameClaudeCode} })
}
