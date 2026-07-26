package opencode

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

func TestName(t *testing.T) {
	if got := New().Name(); got != "opencode" {
		t.Errorf("Name() = %q, want %q", got, "opencode")
	}
}

func TestRegistered(t *testing.T) {
	rt, err := runtime.Lookup(Name)
	if err != nil {
		t.Fatalf("Lookup(%q) returned error: %v", Name, err)
	}
	if rt == nil {
		t.Fatalf("Lookup(%q) returned nil runtime", Name)
	}
	if rt.Name() != Name {
		t.Errorf("Lookup runtime Name = %q, want %q", rt.Name(), Name)
	}
}

func TestKnownNamesIncludesOpenCode(t *testing.T) {
	found := false
	for _, n := range runtime.KnownNames() {
		if n == Name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("KnownNames does not include %q: %v", Name, runtime.KnownNames())
	}
}

func TestPrepareAgentDef_WritesFile(t *testing.T) {
	dir := t.TempDir()
	o := New()
	body := []byte("---\nname: autopilot\n---\nhello")
	if err := o.PrepareAgentDef(context.Background(), runtime.Workspace{Dir: dir}, runtime.AgentDefinition{
		Name: "autopilot",
		Body: body,
	}); err != nil {
		t.Fatalf("PrepareAgentDef: %v", err)
	}
	// opencode reads the SINGULAR `.opencode/agent/` directory.
	got, err := os.ReadFile(filepath.Join(dir, ".opencode", "agent", "autopilot.md"))
	if err != nil {
		t.Fatalf("read written def: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("written body = %q, want %q", got, body)
	}
}

func TestPrepareAgentDef_Idempotent(t *testing.T) {
	dir := t.TempDir()
	o := New()
	ctx := context.Background()
	first := []byte("first body")
	second := []byte("second body")
	if err := o.PrepareAgentDef(ctx, runtime.Workspace{Dir: dir}, runtime.AgentDefinition{Name: "x", Body: first}); err != nil {
		t.Fatalf("first PrepareAgentDef: %v", err)
	}
	if err := o.PrepareAgentDef(ctx, runtime.Workspace{Dir: dir}, runtime.AgentDefinition{Name: "x", Body: second}); err != nil {
		t.Fatalf("second PrepareAgentDef: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".opencode", "agent", "x.md"))
	if err != nil {
		t.Fatalf("read written def: %v", err)
	}
	if !bytes.Equal(got, second) {
		t.Errorf("written body = %q, want overwritten %q", got, second)
	}
}

func TestPrepareAgentDef_NoWorkspace(t *testing.T) {
	o := New()
	if err := o.PrepareAgentDef(context.Background(), runtime.Workspace{}, runtime.AgentDefinition{Name: "x", Body: []byte("y")}); err != nil {
		t.Errorf("expected nil for empty workspace, got %v", err)
	}
}

func TestPrepareAgentDef_Errors(t *testing.T) {
	o := New()
	ctx := context.Background()
	dir := t.TempDir()
	if err := o.PrepareAgentDef(ctx, runtime.Workspace{Dir: dir}, runtime.AgentDefinition{Name: "", Body: []byte("x")}); err == nil {
		t.Error("expected error for empty name")
	}
	if err := o.PrepareAgentDef(ctx, runtime.Workspace{Dir: dir}, runtime.AgentDefinition{Name: "x", Body: nil}); err == nil {
		t.Error("expected error for empty body")
	}
}

// TestStubbedMethodsNotSupported covers the methods still stubbed for later
// issues (#539 ParseResult/ClassifyOutcome, #540 Resume/ExtractBailReport).
// Run is implemented (#538) and is exercised by the pure-logic and live tests.
func TestStubbedMethodsNotSupported(t *testing.T) {
	o := New()
	if _, err := o.Resume(context.Background(), "sid", nil, nil); !errors.Is(err, runtime.ErrNotSupported) {
		t.Errorf("Resume err = %v, want ErrNotSupported", err)
	}
	if _, err := o.ParseResult(""); !errors.Is(err, runtime.ErrNotSupported) {
		t.Errorf("ParseResult err = %v, want ErrNotSupported", err)
	}
	if got := o.ClassifyOutcome(nil, runtime.Limits{}); got != (runtime.Outcome{}) {
		t.Errorf("ClassifyOutcome = %+v, want zero Outcome", got)
	}
	if got := o.ExtractBailReport(nil, ""); got != nil {
		t.Errorf("ExtractBailReport = %+v, want nil", got)
	}
}
