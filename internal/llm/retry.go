package llm

import (
	"context"
	"fmt"
	"math"
	"time"
)

// Retry configuration constants. These can be promoted to configurable
// options later if needed.
const (
	maxRetries = 3
	baseDelay  = 1 * time.Second
	maxDelay   = 30 * time.Second
)

// retryProvider wraps a Provider with automatic retry logic for transient errors.
// Non-retryable errors (auth, bad request) fail immediately.
type retryProvider struct {
	inner Provider
	// waitFunc waits for the given duration, respecting context cancellation.
	// Returns ctx.Err() if the context is cancelled during the wait.
	// Defaults to waitWithContext; tests can override for instant execution.
	waitFunc func(ctx context.Context, d time.Duration) error
}

// NewRetryProvider wraps any Provider with retry middleware. Transient errors
// (rate limits, server errors, timeouts) are retried with exponential backoff.
// Non-retryable errors (auth, malformed request) fail immediately.
func NewRetryProvider(p Provider) Provider {
	return &retryProvider{
		inner:    p,
		waitFunc: waitWithContext,
	}
}

// newRetryProviderForTest creates a retry provider with a custom wait function
// so tests can run without actual delays.
func newRetryProviderForTest(p Provider, waitFn func(ctx context.Context, d time.Duration) error) *retryProvider {
	return &retryProvider{
		inner:    p,
		waitFunc: waitFn,
	}
}

func (r *retryProvider) Name() string {
	return r.inner.Name()
}

func (r *retryProvider) Complete(ctx context.Context, req *Request) (*Response, error) {
	var lastErr error

	for attempt := range maxRetries + 1 {
		resp, err := r.inner.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}

		lastErr = err
		kind, statusCode := classifyError(err)

		// Non-retryable errors fail immediately.
		if !isRetryable(kind) {
			return nil, err
		}

		// Don't retry if we've exhausted attempts.
		if attempt >= maxRetries {
			break
		}

		// Check context before waiting.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("llm retry aborted: %w", ctx.Err())
		}

		delay := backoffDelay(attempt)

		debugLogRetry(r.inner.Name(), attempt+1, maxRetries, kind, statusCode, delay)

		if err := r.waitFunc(ctx, delay); err != nil {
			return nil, fmt.Errorf("llm retry aborted: %w", err)
		}
	}

	return nil, fmt.Errorf("llm: %d retries exhausted: %w", maxRetries, lastErr)
}

// waitWithContext waits for the given duration, returning early if the context
// is cancelled.
func waitWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	select {
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// backoffDelay computes exponential backoff: baseDelay * 2^attempt, capped at maxDelay.
func backoffDelay(attempt int) time.Duration {
	delay := time.Duration(float64(baseDelay) * math.Pow(2, float64(attempt)))
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

// debugLogRetry logs retry attempts. This is a no-op placeholder that can be
// wired into the structured debug logger if MINDER_DEBUG is set.
func debugLogRetry(provider string, attempt, maxAttempts int, kind ErrorKind, statusCode int, delay time.Duration) {
	// Intentionally a no-op for now. The poller package owns the debug logger,
	// and adding a dependency here would create a circular import. This can be
	// wired via a callback or slog.Logger injection later.
}
