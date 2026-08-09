package eventbus

import (
	"errors"
	"testing"
	"time"
)

func TestFanOutToIndependentSubscribers(t *testing.T) {
	const (
		subscriberCount = 5
		eventCount      = 100
	)
	bus := New[int](8)
	t.Cleanup(bus.Close)

	subscribers := make([]*Subscription[int], subscriberCount)
	for i := range subscribers {
		var err error
		subscribers[i], err = bus.Subscribe(0)
		if err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
		defer subscribers[i].Close()
	}

	received := make(chan []Event[int], subscriberCount)
	for _, subscriber := range subscribers {
		go func(subscription *Subscription[int]) {
			events := make([]Event[int], 0, eventCount)
			for len(events) < eventCount {
				events = append(events, <-subscription.Events())
			}
			received <- events
		}(subscriber)
	}

	for value := 1; value <= eventCount; value++ {
		if _, err := bus.Publish(value); err != nil {
			t.Fatalf("publish %d: %v", value, err)
		}
	}

	for i := 0; i < subscriberCount; i++ {
		select {
		case events := <-received:
			assertSequence(t, events, 1, eventCount)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for fan-out subscriber")
		}
	}
}

func TestSubscribeAfterCursorCatchesUpThenContinuesLive(t *testing.T) {
	bus := New[string](2)
	t.Cleanup(bus.Close)
	for _, value := range []string{"one", "two", "three"} {
		if _, err := bus.Publish(value); err != nil {
			t.Fatalf("publish %q: %v", value, err)
		}
	}

	subscription, err := bus.Subscribe(1)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer subscription.Close()

	if _, err := bus.Publish("four"); err != nil {
		t.Fatalf("publish live event: %v", err)
	}

	for cursor, value := range []string{"two", "three", "four"} {
		select {
		case event := <-subscription.Events():
			wantCursor := uint64(cursor + 2)
			if event.Cursor != wantCursor || event.Value != value {
				t.Fatalf("event = {%d %q}, want {%d %q}", event.Cursor, event.Value, wantCursor, value)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for cursor %d", cursor+2)
		}
	}
}

func TestSubscribeDuringPublishHasNoCatchUpLiveGap(t *testing.T) {
	const (
		beforeSubscribe = 100
		totalEvents     = 1000
	)
	bus := New[int](4)
	t.Cleanup(bus.Close)
	for value := 1; value <= beforeSubscribe; value++ {
		if _, err := bus.Publish(value); err != nil {
			t.Fatalf("publish initial event %d: %v", value, err)
		}
	}
	afterCursor := bus.Cursor()

	published := make(chan error, 1)
	go func() {
		for value := beforeSubscribe + 1; value <= totalEvents; value++ {
			if _, err := bus.Publish(value); err != nil {
				published <- err
				return
			}
		}
		published <- nil
	}()

	subscription, err := bus.Subscribe(afterCursor)
	if err != nil {
		t.Fatalf("subscribe while publishing: %v", err)
	}
	defer subscription.Close()

	events := make([]Event[int], 0, totalEvents-beforeSubscribe)
	for len(events) < totalEvents-beforeSubscribe {
		select {
		case event := <-subscription.Events():
			events = append(events, event)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d events", len(events))
		}
	}
	if err := <-published; err != nil {
		t.Fatalf("publish live events: %v", err)
	}
	assertSequence(t, events, beforeSubscribe+1, totalEvents)
}

func TestSlowSubscriberDoesNotBlockOrLoseEventsForOthers(t *testing.T) {
	const eventCount = 500
	bus := New[int](1)
	t.Cleanup(bus.Close)

	slow, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe slow: %v", err)
	}
	defer slow.Close()
	fast, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe fast: %v", err)
	}
	defer fast.Close()

	fastResult := make(chan []Event[int], 1)
	go func() {
		events := make([]Event[int], 0, eventCount)
		for len(events) < eventCount {
			events = append(events, <-fast.Events())
		}
		fastResult <- events
	}()

	published := make(chan error, 1)
	go func() {
		for value := 1; value <= eventCount; value++ {
			if _, err := bus.Publish(value); err != nil {
				published <- err
				return
			}
		}
		published <- nil
	}()

	select {
	case err := <-published:
		if err != nil {
			t.Fatalf("publish: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("publishing blocked on the slow subscriber")
	}

	select {
	case events := <-fastResult:
		assertSequence(t, events, 1, eventCount)
	case <-time.After(2 * time.Second):
		t.Fatal("fast subscriber was delayed by the slow subscriber")
	}

	slowEvents := make([]Event[int], 0, eventCount)
	for len(slowEvents) < eventCount {
		select {
		case event := <-slow.Events():
			slowEvents = append(slowEvents, event)
		case <-time.After(2 * time.Second):
			t.Fatalf("slow subscriber lost events after %d deliveries", len(slowEvents))
		}
	}
	assertSequence(t, slowEvents, 1, eventCount)
}

func TestSubscribeRejectsCursorAheadOfBus(t *testing.T) {
	bus := New[int](1)
	defer bus.Close()
	if _, err := bus.Subscribe(1); !errors.Is(err, ErrCursorAhead) {
		t.Fatalf("Subscribe error = %v, want ErrCursorAhead", err)
	}
}

func TestLaggingSubscriberTruncatedWithoutAffectingOthers(t *testing.T) {
	const (
		retention  = 4
		eventCount = 20
	)
	bus := NewWithRetention[int](1, retention)
	t.Cleanup(bus.Close)

	lagging, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe lagging: %v", err)
	}
	live, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe live: %v", err)
	}
	defer live.Close()

	// The live subscriber reads each event as it is published, so it never
	// falls behind retention; the lagging subscriber is never read at all.
	for value := 1; value <= eventCount; value++ {
		if _, err := bus.Publish(value); err != nil {
			t.Fatalf("publish %d: %v", value, err)
		}
		select {
		case event := <-live.Events():
			if event.Cursor != uint64(value) || event.Value != value {
				t.Fatalf("live event = {%d %d}, want {%d %d}", event.Cursor, event.Value, value, value)
			}
		case <-time.After(time.Second):
			t.Fatalf("live subscriber stalled at event %d", value)
		}
	}

	assertTruncatedClose(t, lagging)
	if err := live.Err(); err != nil {
		t.Fatalf("live subscriber Err() = %v, want nil", err)
	}
}

func TestAbandonedSubscriptionReapedByTruncation(t *testing.T) {
	bus := NewWithRetention[int](1, 2)
	t.Cleanup(bus.Close)

	// Abandoned: no reader, Close never called. Its delivery goroutine parks
	// on a full channel until retention passes it.
	abandoned, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	for value := 1; value <= 10; value++ {
		if _, err := bus.Publish(value); err != nil {
			t.Fatalf("publish %d: %v", value, err)
		}
	}

	assertTruncatedClose(t, abandoned)
}

func TestSubscribeBelowRetentionFloorReturnsTruncated(t *testing.T) {
	bus := NewWithRetention[int](1, 2)
	t.Cleanup(bus.Close)
	for value := 1; value <= 5; value++ {
		if _, err := bus.Publish(value); err != nil {
			t.Fatalf("publish %d: %v", value, err)
		}
	}

	// Events 1-3 are discarded; 4 and 5 remain, so cursors stay monotonic
	// across truncation.
	if cursor := bus.Cursor(); cursor != 5 {
		t.Fatalf("Cursor() = %d, want 5", cursor)
	}
	for _, afterCursor := range []uint64{0, 2} {
		if _, err := bus.Subscribe(afterCursor); !errors.Is(err, ErrTruncated) {
			t.Fatalf("Subscribe(%d) error = %v, want ErrTruncated", afterCursor, err)
		}
	}

	subscription, err := bus.Subscribe(3)
	if err != nil {
		t.Fatalf("Subscribe(3) at the retention floor: %v", err)
	}
	defer subscription.Close()
	for want := 4; want <= 5; want++ {
		select {
		case event := <-subscription.Events():
			if event.Cursor != uint64(want) || event.Value != want {
				t.Fatalf("event = {%d %d}, want {%d %d}", event.Cursor, event.Value, want, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for retained event %d", want)
		}
	}
}

func TestReplayRespectsRetentionFloor(t *testing.T) {
	bus := NewWithRetention[int](1, 2)
	t.Cleanup(bus.Close)
	for value := 1; value <= 5; value++ {
		if _, err := bus.Publish(value); err != nil {
			t.Fatalf("publish %d: %v", value, err)
		}
	}

	if _, err := bus.Replay(0); !errors.Is(err, ErrTruncated) {
		t.Fatalf("Replay(0) error = %v, want ErrTruncated", err)
	}
	events, err := bus.Replay(3)
	if err != nil {
		t.Fatalf("Replay(3) at the retention floor: %v", err)
	}
	assertSequence(t, events, 4, 5)
}

// assertTruncatedClose drains a subscription until its channel closes and
// asserts it terminated with ErrTruncated. The close proves the delivery
// goroutine exited rather than parking forever.
func assertTruncatedClose(t *testing.T, subscription *Subscription[int]) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, open := <-subscription.Events():
			if open {
				continue
			}
			if err := subscription.Err(); !errors.Is(err, ErrTruncated) {
				t.Fatalf("Err() after truncated close = %v, want ErrTruncated", err)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for the truncated subscription to close")
		}
	}
}

func assertSequence(t *testing.T, events []Event[int], first, last int) {
	t.Helper()
	if len(events) != last-first+1 {
		t.Fatalf("received %d events, want %d", len(events), last-first+1)
	}
	for i, event := range events {
		want := first + i
		if event.Cursor != uint64(want) || event.Value != want {
			t.Fatalf("event %d = {%d %d}, want {%d %d}", i, event.Cursor, event.Value, want, want)
		}
	}
}
