package supervisor

import (
	"errors"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/eventbus"
)

// drainEvents reads all currently-buffered events without blocking.
func drainEvents(s *Supervisor) []Event {
	return s.DrainEvents()
}

// waitForEvent reads up to one event with a short deadline. Returns ok=false if none arrived.
func waitForEvent(s *Supervisor, d time.Duration) (Event, bool) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if events := s.DrainEvents(); len(events) > 0 {
			return events[0], true
		}
		time.Sleep(time.Millisecond)
	}
	return Event{}, false
}

func newNetTestSupervisor() *Supervisor {
	s := &Supervisor{
		running:   make(map[int64]*runState),
		maxAgents: 3,
		events:    eventbus.New[Event](32),
	}
	return s
}

func TestTryFetchSuccessNoEvents(t *testing.T) {
	s := newNetTestSupervisor()
	s.fetchFn = func() error { return nil }

	s.tryFetch()
	if s.offline {
		t.Fatal("expected online after successful fetch")
	}
	if got := drainEvents(s); len(got) != 0 {
		t.Fatalf("expected no events on healthy fetch, got %d: %+v", len(got), got)
	}
}

func TestTryFetchTransitionsOfflineThenSilent(t *testing.T) {
	s := newNetTestSupervisor()
	s.fetchFn = func() error { return errors.New("operation timed out") }

	// First failure: one warning event.
	s.tryFetch()
	if !s.offline {
		t.Fatal("expected offline after network failure")
	}
	evt, ok := waitForEvent(s, 200*time.Millisecond)
	if !ok {
		t.Fatal("expected an offline warning event")
	}
	if evt.Type != "warning" {
		t.Fatalf("expected warning event, got type=%q summary=%q", evt.Type, evt.Summary)
	}

	// Subsequent failures while offline must be silent and must not actually
	// attempt the fetch when nextProbeAt is in the future.
	calls := 0
	s.fetchFn = func() error {
		calls++
		return errors.New("operation timed out")
	}
	for range 5 {
		s.tryFetch()
	}
	if calls != 0 {
		t.Fatalf("expected 0 fetch attempts while in backoff, got %d", calls)
	}
	if got := drainEvents(s); len(got) != 0 {
		t.Fatalf("expected silence while offline, got %d events: %+v", len(got), got)
	}
}

func TestTryFetchBackoffAdvancesOnConsecutiveFailures(t *testing.T) {
	s := newNetTestSupervisor()
	s.fetchFn = func() error { return errors.New("connection refused") }

	// Force first failure, then jump time forward and fail again.
	s.tryFetch()
	if !s.offline || s.offlineBackoffIdx != 0 {
		t.Fatalf("expected idx=0 after first failure, got offline=%v idx=%d", s.offline, s.offlineBackoffIdx)
	}
	_ = drainEvents(s)

	// Simulate clock passing the probe deadline by clearing nextProbeAt.
	for i := 1; i < len(offlineBackoffSchedule); i++ {
		s.nextProbeAt = time.Time{}
		s.tryFetch()
		if s.offlineBackoffIdx != i {
			t.Fatalf("after probe %d, expected idx=%d got %d", i+1, i, s.offlineBackoffIdx)
		}
	}

	// One more probe past the schedule end: index should clamp, not panic.
	s.nextProbeAt = time.Time{}
	s.tryFetch()
	if s.offlineBackoffIdx != len(offlineBackoffSchedule)-1 {
		t.Fatalf("expected idx clamped to %d, got %d", len(offlineBackoffSchedule)-1, s.offlineBackoffIdx)
	}

	// No spam during backoff transitions.
	if got := drainEvents(s); len(got) != 0 {
		t.Fatalf("expected silence during backoff escalation, got %d events", len(got))
	}
}

func TestTryFetchRecoveryEmitsOnlineEvent(t *testing.T) {
	s := newNetTestSupervisor()
	s.fetchFn = func() error { return errors.New("network is unreachable") }

	s.tryFetch()
	if !s.offline {
		t.Fatal("expected offline after failure")
	}
	_ = drainEvents(s)

	// Flip to healthy and pretend the backoff window has elapsed.
	s.nextProbeAt = time.Time{}
	s.fetchFn = func() error { return nil }
	s.tryFetch()

	if s.offline {
		t.Fatal("expected online after successful probe")
	}
	if s.offlineBackoffIdx != 0 {
		t.Fatalf("expected backoff reset to 0, got %d", s.offlineBackoffIdx)
	}
	evt, ok := waitForEvent(s, 200*time.Millisecond)
	if !ok || evt.Type != "info" {
		t.Fatalf("expected info recovery event, got ok=%v evt=%+v", ok, evt)
	}
}

func TestTryFetchNonNetworkErrorSurfaces(t *testing.T) {
	s := newNetTestSupervisor()
	s.fetchFn = func() error { return errors.New("fatal: bad object refs/heads/foo") }

	s.tryFetch()
	if s.offline {
		t.Fatal("non-network errors must not flip to offline")
	}
	evt, ok := waitForEvent(s, 200*time.Millisecond)
	if !ok || evt.Type != "warning" {
		t.Fatalf("expected warning event for non-network error, got ok=%v evt=%+v", ok, evt)
	}
}

func TestIsOnlineGatesCheckMergedPRs(t *testing.T) {
	s := newNetTestSupervisor()
	s.offline = true
	if s.isOnline() {
		t.Fatal("expected isOnline=false when offline")
	}
	s.offline = false
	if !s.isOnline() {
		t.Fatal("expected isOnline=true when online")
	}
}
