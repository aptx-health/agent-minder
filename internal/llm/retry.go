package llm

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
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
// Non-retryable errors (auth, bad request, canceled) fail immediately.
type retryProvider struct {
	inner Provider
	// waitFunc waits for the given duration, respecting context cancellation.
	// Returns ctx.Err() if the context is cancelled during the wait.
	// Defaults to waitWithContext; tests can override for instant execution.
	waitFunc func(ctx context.Context, d time.Duration) error
	// logger for retry attempts. Nil disables logging.
	logger *slog.Logger
}

// NewRetryProvider wraps any Provider with retry middleware. Transient errors
// (rate limits, server errors, timeouts) are retried with exponential backoff
// and jitter. Non-retryable errors (auth, malformed request, canceled) fail
// immediately.
func NewRetryProvider(p Provider, logger *slog.Logger) Provider {
	return &retryProvider{
		inner:    p,
		waitFunc: waitWithContext,
		logger:   logger,
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

		r.logRetry(attempt+1, maxRetries, kind, statusCode, delay)

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

// backoffDelay computes exponential backoff with jitter:
// baseDelay * 2^attempt * (0.5 + rand(0, 0.5)), capped at maxDelay.
// The jitter prevents thundering herd when multiple agents retry simultaneously.
func backoffDelay(attempt int) time.Duration {
	base := float64(baseDelay) * math.Pow(2, float64(attempt))
	// Add jitter: multiply by random factor in [0.5, 1.0)
	jittered := base * (0.5 + rand.Float64()*0.5)
	delay := time.Duration(jittered)
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

// logRetry logs a retry attempt via the structured logger.
func (r *retryProvider) logRetry(attempt, maxAttempts int, kind ErrorKind, statusCode int, delay time.Duration) {
	if r.logger == nil {
		return
	}
	r.logger.Info("llm retry",
		"stage", "retry",
		"step", "backoff",
		"provider", r.inner.Name(),
		"attempt", attempt,
		"max_attempts", maxAttempts,
		"error_kind", kind.String(),
		"status_code", statusCode,
		"delay", delay.String(),
	)
}
