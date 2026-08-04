// Package eventbus provides an in-memory, cursor-based fan-out event bus.
package eventbus

import (
	"errors"
	"fmt"
	"sync"
)

const DefaultSubscriberBuffer = 64

var (
	// ErrClosed is returned when publishing or subscribing to a closed bus.
	ErrClosed = errors.New("event bus is closed")
	// ErrCursorAhead is returned when a subscription asks to resume after a
	// cursor the bus has not issued yet.
	ErrCursorAhead = errors.New("event cursor is ahead of the bus")
)

// Event is a value published to a Bus and its monotonically increasing cursor.
// Cursors start at 1; cursor 0 means "from the beginning" when subscribing.
type Event[T any] struct {
	Cursor uint64
	Value  T
}

// Bus fans every published value out to independent subscribers. Published
// events remain in an in-memory log so subscribers can resume from a cursor.
// Durable retention and compaction intentionally belong to a future storage
// layer.
//
// Each subscription has a bounded delivery channel. A slow reader applies
// backpressure only to its own delivery goroutine: publishers and other
// subscribers continue independently, while the unread events remain in the
// log. Events are therefore never silently dropped.
type Bus[T any] struct {
	mu               sync.Mutex
	events           []Event[T]
	subscribers      map[*Subscription[T]]struct{}
	subscriberBuffer int
	closed           bool
}

// New creates a Bus whose subscriber delivery channels have the requested
// capacity. Non-positive capacities use DefaultSubscriberBuffer.
func New[T any](subscriberBuffer int) *Bus[T] {
	if subscriberBuffer <= 0 {
		subscriberBuffer = DefaultSubscriberBuffer
	}
	return &Bus[T]{
		subscribers:      make(map[*Subscription[T]]struct{}),
		subscriberBuffer: subscriberBuffer,
	}
}

// Publish appends value to the log, assigns its cursor, and wakes every
// subscriber without waiting for any subscriber to read it.
func (b *Bus[T]) Publish(value T) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, ErrClosed
	}

	cursor := uint64(len(b.events) + 1)
	b.events = append(b.events, Event[T]{Cursor: cursor, Value: value})
	for subscriber := range b.subscribers {
		subscriber.notify()
	}
	return cursor, nil
}

// Cursor returns the cursor of the most recently published event, or 0 when
// no event has been published.
func (b *Bus[T]) Cursor() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return uint64(len(b.events))
}

// Subscribe returns events strictly after afterCursor, followed by live
// events. Registration and catch-up share the bus lock with Publish, so there
// is no gap or duplicate at the missed-to-live boundary.
func (b *Bus[T]) Subscribe(afterCursor uint64) (*Subscription[T], error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil, ErrClosed
	}
	current := uint64(len(b.events))
	if afterCursor > current {
		return nil, fmt.Errorf("%w: requested %d, current %d", ErrCursorAhead, afterCursor, current)
	}

	subscriber := &Subscription[T]{
		bus:        b,
		nextCursor: afterCursor + 1,
		events:     make(chan Event[T], b.subscriberBuffer),
		wake:       make(chan struct{}, 1),
		done:       make(chan struct{}),
	}
	b.subscribers[subscriber] = struct{}{}
	go subscriber.run()
	if afterCursor < current {
		subscriber.notify()
	}
	return subscriber, nil
}

// Replay returns a point-in-time copy of all events strictly after
// afterCursor. It is useful for synchronous inspection; live consumers should
// use Subscribe.
func (b *Bus[T]) Replay(afterCursor uint64) ([]Event[T], error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	current := uint64(len(b.events))
	if afterCursor > current {
		return nil, fmt.Errorf("%w: requested %d, current %d", ErrCursorAhead, afterCursor, current)
	}
	replayed := make([]Event[T], len(b.events)-int(afterCursor))
	copy(replayed, b.events[afterCursor:])
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
	closeOnce  sync.Once
}

// Events returns the bounded channel carrying missed-then-live events.
func (s *Subscription[T]) Events() <-chan Event[T] { return s.events }

// Close stops this subscription. It is safe to call more than once.
func (s *Subscription[T]) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.bus.mu.Lock()
		delete(s.bus.subscribers, s)
		s.bus.mu.Unlock()
	})
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
		case <-s.done:
			return
		}
	}
}

func (s *Subscription[T]) next() (Event[T], bool, bool) {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	if s.nextCursor <= uint64(len(s.bus.events)) {
		event := s.bus.events[s.nextCursor-1]
		s.nextCursor++
		return event, true, s.bus.closed
	}
	var zero Event[T]
	return zero, false, s.bus.closed
}
