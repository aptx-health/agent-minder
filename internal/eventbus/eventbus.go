// Package eventbus provides an in-memory, cursor-based fan-out event bus.
package eventbus

import (
	"errors"
	"fmt"
	"sync"
)

const (
	DefaultSubscriberBuffer = 64
	// DefaultMaxRetainedEvents bounds the in-memory log when no explicit
	// retention is configured. The process is long-lived, so the log must
	// never grow without bound.
	DefaultMaxRetainedEvents = 4096
)

var (
	// ErrClosed is returned when publishing or subscribing to a closed bus.
	ErrClosed = errors.New("event bus is closed")
	// ErrCursorAhead is returned when a subscription asks to resume after a
	// cursor the bus has not issued yet.
	ErrCursorAhead = errors.New("event cursor is ahead of the bus")
	// ErrTruncated is returned when a cursor falls below the retention floor:
	// the events needed to resume from it have been discarded. Clients recover
	// by taking a fresh snapshot and resubscribing from its cursor.
	ErrTruncated = errors.New("event cursor is below the bus retention floor")
)

// Event is a value published to a Bus and its monotonically increasing cursor.
// Cursors start at 1; cursor 0 means "from the beginning" when subscribing.
type Event[T any] struct {
	Cursor uint64
	Value  T
}

// Bus fans every published value out to independent subscribers. Published
// events remain in a bounded in-memory log so subscribers can resume from a
// cursor. Durable retention and compaction intentionally belong to a future
// storage layer.
//
// Each subscription has a bounded delivery channel. A slow reader applies
// backpressure only to its own delivery goroutine: publishers and other
// subscribers continue independently, while the unread events remain in the
// log. When retention passes a subscription's unread events, that subscription
// terminates with ErrTruncated — loss is loud and recoverable
// (snapshot-then-resubscribe), never a silent drop.
type Bus[T any] struct {
	mu               sync.Mutex
	events           []Event[T]
	truncatedThrough uint64 // highest cursor discarded by retention; 0 = none
	maxRetained      int
	subscribers      map[*Subscription[T]]struct{}
	subscriberBuffer int
	closed           bool
}

// New creates a Bus whose subscriber delivery channels have the requested
// capacity, retaining at most DefaultMaxRetainedEvents in the log.
// Non-positive capacities use DefaultSubscriberBuffer.
func New[T any](subscriberBuffer int) *Bus[T] {
	return NewWithRetention[T](subscriberBuffer, DefaultMaxRetainedEvents)
}

// NewWithRetention creates a Bus retaining at most maxRetained events in its
// in-memory log. Non-positive values use DefaultMaxRetainedEvents.
func NewWithRetention[T any](subscriberBuffer, maxRetained int) *Bus[T] {
	if subscriberBuffer <= 0 {
		subscriberBuffer = DefaultSubscriberBuffer
	}
	if maxRetained <= 0 {
		maxRetained = DefaultMaxRetainedEvents
	}
	return &Bus[T]{
		subscribers:      make(map[*Subscription[T]]struct{}),
		subscriberBuffer: subscriberBuffer,
		maxRetained:      maxRetained,
	}
}

// Publish appends value to the log, assigns its cursor, and wakes every
// subscriber without waiting for any subscriber to read it. If the log exceeds
// the retention cap, the oldest events are discarded and any subscription that
// still needed them terminates with ErrTruncated.
func (b *Bus[T]) Publish(value T) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, ErrClosed
	}

	cursor := b.truncatedThrough + uint64(len(b.events)) + 1
	b.events = append(b.events, Event[T]{Cursor: cursor, Value: value})
	b.truncateLocked()
	for subscriber := range b.subscribers {
		subscriber.notify()
	}
	return cursor, nil
}

// truncateLocked discards the oldest events beyond the retention cap and
// terminates every subscription whose next unread event was discarded. Callers
// must hold b.mu.
func (b *Bus[T]) truncateLocked() {
	overflow := len(b.events) - b.maxRetained
	if overflow <= 0 {
		return
	}
	var zero Event[T]
	for i := 0; i < overflow; i++ {
		b.events[i] = zero
	}
	b.events = b.events[overflow:]
	b.truncatedThrough += uint64(overflow)

	for subscriber := range b.subscribers {
		if subscriber.nextCursor <= b.truncatedThrough {
			subscriber.truncateLocked()
		}
	}
}

// Cursor returns the cursor of the most recently published event, or 0 when
// no event has been published.
func (b *Bus[T]) Cursor() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncatedThrough + uint64(len(b.events))
}

// Subscribe returns events strictly after afterCursor, followed by live
// events. Registration and catch-up share the bus lock with Publish, so there
// is no gap or duplicate at the missed-to-live boundary. If afterCursor falls
// below the retention floor, Subscribe returns ErrTruncated; resubscribe from
// a fresh snapshot's cursor instead.
func (b *Bus[T]) Subscribe(afterCursor uint64) (*Subscription[T], error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil, ErrClosed
	}
	if afterCursor < b.truncatedThrough {
		return nil, fmt.Errorf("%w: requested %d, oldest retained %d", ErrTruncated, afterCursor, b.truncatedThrough+1)
	}
	current := b.truncatedThrough + uint64(len(b.events))
	if afterCursor > current {
		return nil, fmt.Errorf("%w: requested %d, current %d", ErrCursorAhead, afterCursor, current)
	}

	subscriber := b.subscribeLocked(afterCursor)
	if afterCursor < current {
		subscriber.notify()
	}
	return subscriber, nil
}

// SubscribeFromNow atomically captures the current internal cursor and
// registers a subscriber for values published after it. Callers must use this
// instead of Cursor followed by Subscribe: a Publish between those two calls
// would otherwise be replayed even though the caller asked for live-only
// delivery. The returned cursor is internal bookkeeping and must never be
// exposed as a durable or wire cursor.
func (b *Bus[T]) SubscribeFromNow() (*Subscription[T], uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, 0, ErrClosed
	}
	cursor := b.truncatedThrough + uint64(len(b.events))
	return b.subscribeLocked(cursor), cursor, nil
}

// subscribeLocked registers a subscription whose cursor has already been
// validated. Callers must hold b.mu.
func (b *Bus[T]) subscribeLocked(afterCursor uint64) *Subscription[T] {
	subscriber := &Subscription[T]{
		bus:        b,
		nextCursor: afterCursor + 1,
		events:     make(chan Event[T], b.subscriberBuffer),
		wake:       make(chan struct{}, 1),
		done:       make(chan struct{}),
		truncated:  make(chan struct{}),
	}
	b.subscribers[subscriber] = struct{}{}
	go subscriber.run()
	return subscriber
}

// Replay returns a point-in-time copy of all retained events strictly after
// afterCursor. It is useful for synchronous inspection; live consumers should
// use Subscribe. If afterCursor falls below the retention floor, Replay
// returns ErrTruncated.
func (b *Bus[T]) Replay(afterCursor uint64) ([]Event[T], error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if afterCursor < b.truncatedThrough {
		return nil, fmt.Errorf("%w: requested %d, oldest retained %d", ErrTruncated, afterCursor, b.truncatedThrough+1)
	}
	current := b.truncatedThrough + uint64(len(b.events))
	if afterCursor > current {
		return nil, fmt.Errorf("%w: requested %d, current %d", ErrCursorAhead, afterCursor, current)
	}
	replayed := make([]Event[T], current-afterCursor)
	copy(replayed, b.events[afterCursor-b.truncatedThrough:])
	return replayed, nil
}

// Close rejects future publishes and subscriptions. Existing subscribers drain
// the events already in the log before their event channels close.
func (b *Bus[T]) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for subscriber := range b.subscribers {
		subscriber.notify()
	}
}

// Subscription is one independent view of a Bus. Close releases it without
// affecting the bus or any other subscriber.
type Subscription[T any] struct {
	bus        *Bus[T]
	nextCursor uint64
	events     chan Event[T]
	wake       chan struct{}
	done       chan struct{}
	truncated  chan struct{}
	closeOnce  sync.Once

	// err records why Events was closed. Written under bus.mu before the
	// truncated channel closes; read via Err.
	err error
	// truncatedSignalled guards the truncated channel's single close; it is
	// protected by bus.mu.
	truncatedSignalled bool
}

// Events returns the bounded channel carrying missed-then-live events. When it
// closes, Err reports whether the subscription ended because retention passed
// it (ErrTruncated) or ended normally (nil).
func (s *Subscription[T]) Events() <-chan Event[T] { return s.events }

// Err returns ErrTruncated when retention discarded events this subscription
// had not yet delivered, and nil otherwise. It is meaningful once Events is
// closed.
func (s *Subscription[T]) Err() error {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	return s.err
}

// Close stops this subscription. It is safe to call more than once.
func (s *Subscription[T]) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.bus.mu.Lock()
		delete(s.bus.subscribers, s)
		s.bus.mu.Unlock()
	})
}

// truncateLocked marks the subscription as passed by retention and wakes its
// delivery goroutine so it terminates instead of stalling — this is also how
// an abandoned subscription (reader gone, Close never called) gets reaped.
// Callers must hold bus.mu.
func (s *Subscription[T]) truncateLocked() {
	if s.truncatedSignalled {
		return
	}
	s.truncatedSignalled = true
	s.err = fmt.Errorf("%w: next cursor %d, oldest retained %d", ErrTruncated, s.nextCursor, s.bus.truncatedThrough+1)
	close(s.truncated)
}

func (s *Subscription[T]) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Subscription[T]) run() {
	defer close(s.events)
	defer func() {
		s.bus.mu.Lock()
		delete(s.bus.subscribers, s)
		s.bus.mu.Unlock()
	}()

	for {
		event, ok, busClosed := s.next()
		if ok {
			select {
			case s.events <- event:
			case <-s.truncated:
				return
			case <-s.done:
				return
			}
			continue
		}
		if busClosed {
			return
		}
		select {
		case <-s.wake:
		case <-s.truncated:
			return
		case <-s.done:
			return
		}
	}
}

func (s *Subscription[T]) next() (Event[T], bool, bool) {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	var zero Event[T]
	if s.nextCursor <= s.bus.truncatedThrough {
		// Retention passed this subscription; truncateLocked already
		// signalled it, so run() exits via the truncated channel.
		return zero, false, false
	}
	index := s.nextCursor - s.bus.truncatedThrough - 1
	if index < uint64(len(s.bus.events)) {
		event := s.bus.events[index]
		s.nextCursor++
		return event, true, s.bus.closed
	}
	return zero, false, s.bus.closed
}
