package poller

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/llm"
)

// trackingProvider records every Complete call so we can verify routing.
type trackingProvider struct {
	name  string
	calls atomic.Int32
}

func (p *trackingProvider) Name() string { return p.name }

func (p *trackingProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	p.calls.Add(1)
	return &llm.Response{Content: `{"analysis":"ok","concerns":[]}`}, nil
}

func (p *trackingProvider) callCount() int { return int(p.calls.Load()) }

func TestNewPollerSeparateProviders(t *testing.T) {
	summ := &trackingProvider{name: "summarizer"}
	anlz := &trackingProvider{name: "analyzer"}

	project := &db.Project{
		Name:               "test",
		LLMSummarizerModel: "haiku",
		LLMAnalyzerModel:   "sonnet",
	}

	p := New(nil, project, summ, anlz, nil)

	if p.summarizerProvider.Name() != "summarizer" {
		t.Errorf("summarizerProvider.Name() = %q, want %q", p.summarizerProvider.Name(), "summarizer")
	}
	if p.analyzerProvider.Name() != "analyzer" {
		t.Errorf("analyzerProvider.Name() = %q, want %q", p.analyzerProvider.Name(), "analyzer")
	}
	if p.AnalyzerProvider().Name() != "analyzer" {
		t.Errorf("AnalyzerProvider() = %q, want %q", p.AnalyzerProvider().Name(), "analyzer")
	}
}

func TestNewPollerSameProvider(t *testing.T) {
	shared := &trackingProvider{name: "anthropic"}

	project := &db.Project{
		Name:               "test",
		LLMSummarizerModel: "haiku",
		LLMAnalyzerModel:   "sonnet",
	}

	p := New(nil, project, shared, shared, nil)

	if p.summarizerProvider != p.analyzerProvider {
		t.Error("expected same provider instance for both tiers")
	}
}
