package supervisor

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
	runtimepkg "github.com/aptx-health/agent-minder/internal/runtime"
)

// withShortUsageLimitWait shortens the usage-limit sleep so the resume loop
// can be exercised in unit tests without burning an hour per attempt.
func withShortUsageLimitWait(t *testing.T) {
	t.Helper()
	prev := usageLimitWaitDuration
	usageLimitWaitDuration = 10 * time.Millisecond
	t.Cleanup(func() { usageLimitWaitDuration = prev })
}

// TestResume_HookInvokedWithSessionID verifies that Hooks.ResumeFn is called
// with the sessionID surfaced by RunStageFn when the code stage reports a
// usage limit, and that a successful resume completes the stage.
func TestResume_HookInvokedWithSessionID(t *testing.T) {
	withShortUsageLimitWait(t)
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = false
	})

	// First call: report usage limit with a session ID.
	// Subsequent calls (shouldn't happen on the happy resume path) just succeed.
	var stageCalls atomic.Int32
	h.hooks.RunStageFn = func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
		stageCalls.Add(1)
		return 0, &runtimepkg.Result{SessionID: "sess-abc"}, true, nil
	}

	var resumeCalls atomic.Int32
	var seenSessionID atomic.Value
	h.hooks.ResumeFn = func(ctx context.Context, sessionID string, logFile *os.File) (*runtimepkg.Result, bool, error) {
		resumeCalls.Add(1)
		seenSessionID.Store(sessionID)
		// Resume succeeds — no further usage limit.
		return &runtimepkg.Result{SessionID: sessionID}, false, nil
	}
	h.hooks.DetectPRFn = func(ctx context.Context) int { return 700 }

	job := testJob(t, h.store, h.deploy)
	contract := DefaultContract("autopilot")

	if err := h.run(context.Background(), job, contract); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := resumeCalls.Load(); got != 1 {
		t.Errorf("ResumeFn calls = %d, want 1", got)
	}
	if got, _ := seenSessionID.Load().(string); got != "sess-abc" {
		t.Errorf("ResumeFn sessionID = %q, want %q", got, "sess-abc")
	}
	if got := stageCalls.Load(); got != 1 {
		t.Errorf("RunStageFn calls = %d, want 1 (resume should not fall back to a fresh stage run)", got)
	}

	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !updated.PRNumber.Valid || updated.PRNumber.Int64 != 700 {
		t.Errorf("expected PR #700 after resume, got %+v", updated.PRNumber)
	}
}

// TestResume_ResumedSessionAlsoHitsLimit covers the loop branch where the
// resumed session also reports a usage limit. The supervisor should keep
// retrying up to maxUsageLimitRetries, and then bail when retries are
// exhausted.
func TestResume_ResumedSessionAlsoHitsLimit(t *testing.T) {
	withShortUsageLimitWait(t)
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = false
	})

	h.hooks.RunStageFn = func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
		return 0, &runtimepkg.Result{SessionID: "sess-loop"}, true, nil
	}

	var resumeCalls atomic.Int32
	h.hooks.ResumeFn = func(ctx context.Context, sessionID string, logFile *os.File) (*runtimepkg.Result, bool, error) {
		resumeCalls.Add(1)
		// Every resume hits the limit again — drives the loop to exhaustion.
		return &runtimepkg.Result{SessionID: sessionID}, true, nil
	}

	job := testJob(t, h.store, h.deploy)
	contract := DefaultContract("autopilot")

	if err := h.run(context.Background(), job, contract); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := resumeCalls.Load(); int(got) != maxUsageLimitRetries {
		t.Errorf("ResumeFn calls = %d, want %d (one per retry)", got, maxUsageLimitRetries)
	}

	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if updated.Status != db.StatusFailed {
		t.Errorf("status = %q, want %q after exhausting usage-limit retries", updated.Status, db.StatusFailed)
	}

	// Supervisor should have emitted the "exhausted N retries" error event.
	if !hasEvent(h.events(), "error", "exhausted") {
		t.Errorf("expected an error event mentioning 'exhausted'; got %+v", h.events())
	}
}

// TestResume_ErrNotSupportedFallsBackToFreshRun verifies that when the
// runtime cannot resume (ErrNotSupported), the supervisor re-invokes the
// code stage fresh — this is the safety net for runtimes without a session
// concept.
func TestResume_ErrNotSupportedFallsBackToFreshRun(t *testing.T) {
	withShortUsageLimitWait(t)
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = false
	})

	var stageCalls atomic.Int32
	h.hooks.RunStageFn = func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
		n := stageCalls.Add(1)
		// First call hits a usage limit with a session ID; second (fresh) call succeeds.
		if n == 1 {
			return 0, &runtimepkg.Result{SessionID: "sess-x"}, true, nil
		}
		return 0, &runtimepkg.Result{}, false, nil
	}
	h.hooks.ResumeFn = func(ctx context.Context, sessionID string, logFile *os.File) (*runtimepkg.Result, bool, error) {
		return nil, false, runtimepkg.ErrNotSupported
	}
	h.hooks.DetectPRFn = func(ctx context.Context) int { return 800 }

	job := testJob(t, h.store, h.deploy)
	contract := DefaultContract("autopilot")

	if err := h.run(context.Background(), job, contract); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := stageCalls.Load(); got != 2 {
		t.Errorf("RunStageFn calls = %d, want 2 (initial + fallback)", got)
	}
	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !updated.PRNumber.Valid || updated.PRNumber.Int64 != 800 {
		t.Errorf("expected PR #800 after fallback run, got %+v", updated.PRNumber)
	}
}
