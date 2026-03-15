package llm

import (
	"context"
	"errors"
	"testing"
)

// mockProvider implements Provider for testing.
type mockProvider struct {
	name  string
	resp  *Response
	err   error
	calls int
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Complete(_ context.Context, _ *Request) (*Response, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

// mockRecorder captures RecordProviderCall invocations.
type mockRecorder struct {
	calls []recordCall
}
type recordCall struct {
	provider string
	model    string
	err      error
}

func (r *mockRecorder) RecordProviderCall(_ int64, provider, model string, _ int64, _, _ int, callErr error) error {
	r.calls = append(r.calls, recordCall{provider, model, callErr})
	return nil
}

func TestFailoverProvider_PrimarySucceeds(t *testing.T) {
	primary := &mockProvider{name: "primary", resp: &Response{Content: "hello", InputToks: 10, OutputToks: 5}}
	fallback := &mockProvider{name: "fallback", resp: &Response{Content: "fallback-hello"}}
	rec := &mockRecorder{}

	fp := NewFailoverProvider([]Provider{primary, fallback}, rec, 1)
	resp, err := fp.Complete(context.Background(), &Request{Model: "test-model"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "hello" {
		t.Errorf("got %q, want %q", resp.Content, "hello")
	}
	if primary.calls != 1 {
		t.Errorf("primary calls = %d, want 1", primary.calls)
	}
	if fallback.calls != 0 {
		t.Errorf("fallback calls = %d, want 0", fallback.calls)
	}
	if len(rec.calls) != 1 || rec.calls[0].provider != "primary" || rec.calls[0].err != nil {
		t.Errorf("recorder calls = %+v, want 1 success for primary", rec.calls)
	}
}

func TestFailoverProvider_PrimaryFails(t *testing.T) {
	primary := &mockProvider{name: "primary", err: errors.New("rate limited")}
	fallback := &mockProvider{name: "fallback", resp: &Response{Content: "fallback-ok"}}
	rec := &mockRecorder{}

	fp := NewFailoverProvider([]Provider{primary, fallback}, rec, 1)
	resp, err := fp.Complete(context.Background(), &Request{Model: "test-model"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "fallback-ok" {
		t.Errorf("got %q, want %q", resp.Content, "fallback-ok")
	}
	if primary.calls != 1 {
		t.Errorf("primary calls = %d, want 1", primary.calls)
	}
	if fallback.calls != 1 {
		t.Errorf("fallback calls = %d, want 1", fallback.calls)
	}
	// Should have 2 recorder calls: error for primary, success for fallback.
	if len(rec.calls) != 2 {
		t.Fatalf("recorder calls = %d, want 2", len(rec.calls))
	}
	if rec.calls[0].err == nil {
		t.Error("expected error recorded for primary")
	}
	if rec.calls[1].err != nil {
		t.Error("expected success recorded for fallback")
	}
}

func TestFailoverProvider_AllFail(t *testing.T) {
	primary := &mockProvider{name: "primary", err: errors.New("err1")}
	fallback := &mockProvider{name: "fallback", err: errors.New("err2")}

	fp := NewFailoverProvider([]Provider{primary, fallback}, nil, 1)
	_, err := fp.Complete(context.Background(), &Request{Model: "test-model"})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if !errors.Is(err, fallback.err) {
		t.Errorf("got %v, want wrapped %v", err, fallback.err)
	}
}

func TestFailoverProvider_DynamicReorder(t *testing.T) {
	// After primary accumulates errors, fallback should be tried first.
	primary := &mockProvider{name: "primary", err: errors.New("always fails")}
	fallback := &mockProvider{name: "fallback", resp: &Response{Content: "ok"}}

	fp := NewFailoverProvider([]Provider{primary, fallback}, nil, 1)

	// First call: primary fails, fallback succeeds.
	_, _ = fp.Complete(context.Background(), &Request{Model: "m"})

	// Now primary has 1 error, fallback has 1 success.
	// Fix primary so we can see reordering.
	primary.err = nil
	primary.resp = &Response{Content: "primary-ok"}

	// Reset call counts.
	primary.calls = 0
	fallback.calls = 0

	// Second call: fallback should be tried first (lower score).
	resp, err := fp.Complete(context.Background(), &Request{Model: "m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Fallback should be first because primary has worse score.
	if resp.Content != "ok" {
		t.Errorf("expected fallback to be tried first, got %q", resp.Content)
	}
	if fallback.calls != 1 {
		t.Errorf("fallback calls = %d, want 1", fallback.calls)
	}
	if primary.calls != 0 {
		t.Errorf("primary calls = %d, want 0 (should not be reached)", primary.calls)
	}
}

func TestFailoverProvider_SingleProvider(t *testing.T) {
	p := &mockProvider{name: "solo", resp: &Response{Content: "ok"}}
	fp := NewFailoverProvider([]Provider{p}, nil, 1)

	if fp.Name() != "solo" {
		t.Errorf("name = %q, want %q", fp.Name(), "solo")
	}

	resp, err := fp.Complete(context.Background(), &Request{Model: "m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("got %q, want %q", resp.Content, "ok")
	}
}

func TestFailoverProvider_Stats(t *testing.T) {
	primary := &mockProvider{name: "primary", resp: &Response{Content: "ok", InputToks: 100, OutputToks: 50}}
	fp := NewFailoverProvider([]Provider{primary}, nil, 1)

	_, _ = fp.Complete(context.Background(), &Request{Model: "claude-haiku-4-5"})
	_, _ = fp.Complete(context.Background(), &Request{Model: "claude-haiku-4-5"})

	stats := fp.Stats()
	if len(stats) != 1 {
		t.Fatalf("stats len = %d, want 1", len(stats))
	}
	if stats[0].SuccessCount != 2 {
		t.Errorf("success count = %d, want 2", stats[0].SuccessCount)
	}
	if stats[0].TotalCostUSD <= 0 {
		t.Errorf("total cost should be > 0, got %f", stats[0].TotalCostUSD)
	}
}

func TestLookupCost(t *testing.T) {
	c := LookupCost("claude-haiku-4-5")
	if c.InputPerMTok == 0 {
		t.Error("expected non-zero cost for claude-haiku-4-5")
	}

	// Unknown model returns zero cost.
	c2 := LookupCost("unknown-model")
	if c2.InputPerMTok != 0 || c2.OutputPerMTok != 0 {
		t.Error("expected zero cost for unknown model")
	}
}

func TestModelCost_Cost(t *testing.T) {
	c := ModelCost{InputPerMTok: 1.0, OutputPerMTok: 2.0}
	cost := c.Cost(1_000_000, 500_000)
	expected := 1.0 + 1.0 // 1M input * $1/M + 500K output * $2/M
	if cost != expected {
		t.Errorf("cost = %f, want %f", cost, expected)
	}
}
