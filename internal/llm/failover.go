package llm

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// StatsRecorder persists call statistics. Implemented by db.Store.
type StatsRecorder interface {
	RecordProviderCall(projectID int64, provider, model string, latencyMs int64, inputToks, outputToks int, callErr error) error
}

// ProviderScore holds runtime stats for a provider used in dynamic reordering.
type ProviderScore struct {
	Provider     string
	SuccessCount int
	ErrorCount   int
	TotalCostUSD float64
}

// score returns a composite score (lower is better).
// Weighs error rate heavily, then cost.
func (ps ProviderScore) score() float64 {
	total := ps.SuccessCount + ps.ErrorCount
	if total == 0 {
		return 0 // untested providers get neutral score
	}
	errorRate := float64(ps.ErrorCount) / float64(total)
	avgCost := 0.0
	if ps.SuccessCount > 0 {
		avgCost = ps.TotalCostUSD / float64(ps.SuccessCount)
	}
	// Error rate dominates: 1 error = equivalent of $10 in cost penalty.
	return errorRate*10.0 + avgCost
}

// FailoverProvider wraps multiple providers and tries them in order.
// It tracks per-provider stats and dynamically reorders based on cost/reliability.
type FailoverProvider struct {
	providers []Provider
	recorder  StatsRecorder
	projectID int64

	mu     sync.Mutex
	scores map[string]*ProviderScore // keyed by provider name
}

// NewFailoverProvider creates a failover provider from one or more providers.
// The first provider is the primary; others are fallbacks tried in order.
// If recorder is non-nil, call stats are persisted to the database.
func NewFailoverProvider(providers []Provider, recorder StatsRecorder, projectID int64) *FailoverProvider {
	scores := make(map[string]*ProviderScore, len(providers))
	for _, p := range providers {
		scores[p.Name()] = &ProviderScore{Provider: p.Name()}
	}
	return &FailoverProvider{
		providers: providers,
		recorder:  recorder,
		projectID: projectID,
		scores:    scores,
	}
}

// Name returns a composite name.
func (f *FailoverProvider) Name() string {
	if len(f.providers) == 1 {
		return f.providers[0].Name()
	}
	return f.providers[0].Name() + "+failover"
}

// Complete tries each provider in order until one succeeds.
// Providers are dynamically reordered by their cost/reliability score.
func (f *FailoverProvider) Complete(ctx context.Context, req *Request) (*Response, error) {
	ordered := f.orderedProviders()

	var lastErr error
	for i, prov := range ordered {
		start := time.Now()
		resp, err := prov.Complete(ctx, req)
		latencyMs := time.Since(start).Milliseconds()

		if err == nil {
			f.recordSuccess(prov.Name(), req.Model, latencyMs, resp.InputToks, resp.OutputToks)
			return resp, nil
		}

		lastErr = err
		f.recordError(prov.Name(), req.Model, latencyMs, err)

		// Don't retry on context cancellation.
		if ctx.Err() != nil {
			break
		}

		// Last provider — don't retry.
		if i == len(ordered)-1 {
			break
		}
	}

	return nil, fmt.Errorf("all providers failed: %w", lastErr)
}

// Stats returns a snapshot of current provider scores.
func (f *FailoverProvider) Stats() []ProviderScore {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]ProviderScore, 0, len(f.scores))
	for _, s := range f.scores {
		result = append(result, *s)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].score() < result[j].score()
	})
	return result
}

// orderedProviders returns providers sorted by score (best first).
func (f *FailoverProvider) orderedProviders() []Provider {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Copy and sort by score.
	ordered := make([]Provider, len(f.providers))
	copy(ordered, f.providers)
	sort.SliceStable(ordered, func(i, j int) bool {
		si := f.scores[ordered[i].Name()]
		sj := f.scores[ordered[j].Name()]
		return si.score() < sj.score()
	})
	return ordered
}

func (f *FailoverProvider) recordSuccess(provider, model string, latencyMs int64, inputToks, outputToks int) {
	cost := LookupCost(model).Cost(inputToks, outputToks)

	f.mu.Lock()
	if s, ok := f.scores[provider]; ok {
		s.SuccessCount++
		s.TotalCostUSD += cost
	}
	f.mu.Unlock()

	if f.recorder != nil {
		_ = f.recorder.RecordProviderCall(f.projectID, provider, model, latencyMs, inputToks, outputToks, nil)
	}
}

func (f *FailoverProvider) recordError(provider, model string, latencyMs int64, err error) {
	f.mu.Lock()
	if s, ok := f.scores[provider]; ok {
		s.ErrorCount++
	}
	f.mu.Unlock()

	if f.recorder != nil {
		_ = f.recorder.RecordProviderCall(f.projectID, provider, model, latencyMs, 0, 0, err)
	}
}
