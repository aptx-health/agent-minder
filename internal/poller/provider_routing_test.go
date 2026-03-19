package poller

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/dustinlange/agent-minder/internal/claudecli"
	"github.com/dustinlange/agent-minder/internal/db"
)

// trackingCompleter records every Complete call so we can verify routing.
type trackingCompleter struct {
	calls atomic.Int32
}

func (c *trackingCompleter) Complete(_ context.Context, _ *claudecli.Request) (*claudecli.Response, error) {
	c.calls.Add(1)
	return &claudecli.Response{Result: `{"analysis":"ok","concerns":[]}`}, nil
}

func TestNewPollerWithCompleter(t *testing.T) {
	completer := &trackingCompleter{}

	project := &db.Project{
		Name:               "test",
		LLMSummarizerModel: "haiku",
		LLMAnalyzerModel:   "sonnet",
	}

	p := New(nil, project, completer, nil)

	if p.Completer() != completer {
		t.Error("Completer() should return the completer passed to New()")
	}
}
