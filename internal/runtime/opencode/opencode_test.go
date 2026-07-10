package opencode

import (
	"context"
	"errors"
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

func TestStubbedMethodsNotSupported(t *testing.T) {
	o := New()
	if err := o.PrepareAgentDef(context.Background(), runtime.Workspace{}, runtime.AgentDefinition{}); !errors.Is(err, runtime.ErrNotSupported) {
		t.Errorf("PrepareAgentDef err = %v, want ErrNotSupported", err)
	}
	if _, err := o.Run(context.Background(), runtime.Invocation{}, nil, nil); !errors.Is(err, runtime.ErrNotSupported) {
		t.Errorf("Run err = %v, want ErrNotSupported", err)
	}
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
