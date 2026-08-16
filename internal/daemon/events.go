package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/aptx-health/agent-minder/internal/controlapi"
	"github.com/aptx-health/agent-minder/internal/coordinator"
	"github.com/aptx-health/agent-minder/internal/eventbus"
)

const (
	defaultEventHeartbeatInterval = 15 * time.Second
	defaultEventWriteTimeout      = 10 * time.Second
	defaultEventQueueSize         = 64
	defaultEventBatchSize         = 128
	defaultMaxClientStreams       = 4
	defaultMaxProcessStreams      = 64
)

var processV1StreamLimiter = NewStreamLimiter(defaultMaxClientStreams, defaultMaxProcessStreams)

var errSubscriberSaturated = errors.New("event subscriber saturated")

// StreamLimiter bounds long-lived event streams separately from the request
// token bucket. One instance is process-wide by default and can be injected in
// tests to make connection accounting deterministic.
type StreamLimiter struct {
	mu           sync.Mutex
	perClientMax int
	globalMax    int
	global       int
	clients      map[string]int
}

func NewStreamLimiter(perClientMax, globalMax int) *StreamLimiter {
	if perClientMax <= 0 {
		perClientMax = defaultMaxClientStreams
	}
	if globalMax <= 0 {
		globalMax = defaultMaxProcessStreams
	}
	return &StreamLimiter{perClientMax: perClientMax, globalMax: globalMax, clients: make(map[string]int)}
}

func (l *StreamLimiter) acquire(client string) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.global >= l.globalMax || l.clients[client] >= l.perClientMax {
		return nil, false
	}
	l.global++
	l.clients[client]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.global--
			l.clients[client]--
			if l.clients[client] == 0 {
				delete(l.clients, client)
			}
		})
	}, true
}

type eventStreamState struct {
	requested int64
	lastSent  int64
	reason    string
	internal  error
	started   time.Time
}

func (s *Server) handleV1Events(w http.ResponseWriter, r *http.Request) {
	requested, epoch, ok := parseEventCursor(w, r)
	if !ok {
		return
	}
	if s.Provider == nil {
		writeV1Error(w, http.StatusServiceUnavailable, controlapi.ErrorProviderUnavailable, "state provider is unavailable")
		return
	}
	if s.Provider.DeploymentID() != s.deployID {
		writeV1Error(w, http.StatusNotFound, controlapi.ErrorNotFound, "resource was not found")
		return
	}

	clientKey := s.defaultV1ClientKey(r)
	if requestState, found := r.Context().Value(v1StateKey{}).(*v1RequestState); found && requestState.streamKey != "" {
		clientKey = requestState.streamKey
	}
	release, allowed := s.v1Streams.acquire(clientKey)
	if !allowed {
		w.Header().Set("Retry-After", "1")
		writeV1Error(w, http.StatusTooManyRequests, controlapi.ErrorRateLimited, "event stream limit exceeded")
		return
	}
	defer release()

	state := &eventStreamState{requested: requested, lastSent: requested, started: time.Now(), reason: "closed"}
	s.v1Logger.LogAttrs(r.Context(), slog.LevelInfo, "event stream opened",
		slog.String("deployment_id", s.deployID), slog.Int64("requested_cursor", requested))
	defer func() {
		attrs := []slog.Attr{
			slog.String("deployment_id", s.deployID),
			slog.Int64("requested_cursor", state.requested),
			slog.Int64("last_sent_cursor", state.lastSent),
			slog.Duration("duration", time.Since(state.started)),
			slog.String("disconnect_reason", state.reason),
		}
		if state.internal != nil {
			attrs = append(attrs, slog.String("internal_error", state.internal.Error()))
		}
		s.v1Logger.LogAttrs(r.Context(), slog.LevelInfo, "event stream closed", attrs...)
	}()

	// Clear http.Server's blanket WriteTimeout before subscription setup or
	// SQLite replay begins. Individual frame writes install their own bounded
	// deadline below; idle and provider-read periods must not inherit 30s.
	writer := newSSEWriter(w, s.eventWriteTimeout)
	if err := writer.clearDeadline(); err != nil {
		state.reason, state.internal = "deadline_clear_failed", err
		markV1InternalError(r, err)
		writeV1Error(w, http.StatusInternalServerError, controlapi.ErrorInternal, "an internal error occurred")
		return
	}

	// Registration precedes every SQLite replay read. Any commit after this
	// point either appears in that read or leaves a live wake-up on the queue.
	subscription, err := s.Provider.SubscribeEventsFromNow()
	if err != nil {
		state.reason, state.internal = "subscribe_failed", err
		markV1InternalError(r, err)
		writeV1Error(w, http.StatusServiceUnavailable, controlapi.ErrorProviderUnavailable, "state provider is unavailable")
		return
	}
	defer subscription.Close()

	producerCtx, cancelProducer := context.WithCancel(r.Context())
	defer cancelProducer()
	queue := make(chan coordinator.Envelope, s.eventQueueSize)
	saturated := make(chan struct{})
	producerDone := make(chan error, 1)
	go readEventSubscription(producerCtx, subscription, queue, saturated, producerDone)

	batch, err := s.Provider.ReadEventBatch(requested, epoch, s.eventBatchSize)
	if err != nil {
		if reason, semantic := eventResyncReason(err); semantic {
			prepareEventStreamHeaders(w)
			state.reason = string(reason)
			if writeErr := writer.writeJSON("resync-required", 0, false,
				s.resyncPayload(reason, requested, batch)); writeErr != nil {
				state.internal = writeErr
			}
			return
		}
		state.reason, state.internal = "replay_failed", err
		markV1InternalError(r, err)
		writeV1Error(w, http.StatusServiceUnavailable, controlapi.ErrorProviderUnavailable, "state provider is unavailable")
		return
	}

	prepareEventStreamHeaders(w)
	ready := controlapi.EventReady{
		Snapshot:                 s.eventSnapshot(batch.Head, batch.LogEpoch),
		HeartbeatIntervalSeconds: int(math.Ceil(s.eventHeartbeat.Seconds())),
	}
	if err := writer.writeJSON("ready", 0, false, ready); err != nil {
		state.reason, state.internal = streamWriteReason(r.Context(), err), err
		return
	}

	currentBatch := batch
	for {
		if isSignalled(saturated) {
			s.writeSaturationResync(writer, state, batch.LogEpoch)
			return
		}
		for _, event := range currentBatch.Events {
			if isSignalled(saturated) {
				s.writeSaturationResync(writer, state, batch.LogEpoch)
				return
			}
			dto, dtoErr := controlapi.DurableEventDTO(event)
			if dtoErr != nil {
				state.reason, state.internal = "event_encoding_failed", dtoErr
				return
			}
			if writeErr := writer.writeJSON("durable", event.ID, true, dto); writeErr != nil {
				state.reason, state.internal = streamWriteReason(r.Context(), writeErr), writeErr
				return
			}
			state.lastSent = event.ID
		}
		if len(currentBatch.Events) < s.eventBatchSize {
			break
		}
		currentBatch, err = s.Provider.ReadEventBatch(state.lastSent, batch.LogEpoch, s.eventBatchSize)
		if err != nil {
			if reason, semantic := eventResyncReason(err); semantic {
				state.reason = string(reason)
				_ = writer.writeJSON("resync-required", 0, false, s.resyncPayload(reason, state.lastSent, currentBatch))
				return
			}
			state.reason, state.internal = "replay_failed", err
			return
		}
	}

	heartbeat := time.NewTicker(s.eventHeartbeat)
	defer heartbeat.Stop()
	for {
		if isSignalled(saturated) {
			s.writeSaturationResync(writer, state, batch.LogEpoch)
			return
		}
		select {
		case <-r.Context().Done():
			state.reason = "client_disconnect"
			return
		case <-saturated:
			s.writeSaturationResync(writer, state, batch.LogEpoch)
			return
		case producerErr := <-producerDone:
			if errors.Is(producerErr, eventbus.ErrTruncated) {
				s.writeSaturationResync(writer, state, batch.LogEpoch)
				return
			}
			if producerErr != nil {
				state.internal = producerErr
			}
			state.reason = "subscription_closed"
			return
		case envelope := <-queue:
			if isSignalled(saturated) {
				s.writeSaturationResync(writer, state, batch.LogEpoch)
				return
			}
			currentBatch, err = s.drainDurable(writer, state, batch.LogEpoch, saturated)
			if err != nil {
				s.finishDrainError(writer, state, currentBatch, batch.LogEpoch, err)
				return
			}
			if envelope.ID != 0 {
				continue // durable bus envelopes are wake-ups only
			}
			if envelope.DeploymentID != s.deployID {
				continue
			}
			ephemeral, dtoErr := controlapi.EphemeralEventDTO(envelope, s.Provider.WorkerIncarnation())
			if dtoErr != nil {
				state.reason, state.internal = "event_encoding_failed", dtoErr
				return
			}
			if err := writer.writeJSON("ephemeral", 0, false, ephemeral); err != nil {
				state.reason, state.internal = streamWriteReason(r.Context(), err), err
				return
			}
		case <-heartbeat.C:
			if err := writer.writeComment("heartbeat"); err != nil {
				state.reason, state.internal = streamWriteReason(r.Context(), err), err
				return
			}
		}
	}
}

func parseEventCursor(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	raw := r.URL.Query().Get("after")
	if header := r.Header.Get("Last-Event-ID"); header != "" {
		raw = header
	}
	if raw == "" {
		raw = "0"
	}
	cursor, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || cursor < 0 {
		writeV1Error(w, http.StatusBadRequest, controlapi.ErrorInvalidCursor, "event cursor must be a nonnegative integer")
		return 0, "", false
	}
	return cursor, r.URL.Query().Get("log_epoch"), true
}

func prepareEventStreamHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

func readEventSubscription(ctx context.Context, subscription *eventbus.Subscription[coordinator.Envelope], queue chan<- coordinator.Envelope, saturated chan<- struct{}, done chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-subscription.Events():
			if !open {
				done <- subscription.Err()
				return
			}
			select {
			case queue <- event.Value:
			default:
				close(saturated)
				return
			}
		}
	}
}

func (s *Server) drainDurable(writer *sseWriter, state *eventStreamState, epoch string, saturated <-chan struct{}) (coordinator.EventBatchRead, error) {
	var latest coordinator.EventBatchRead
	for {
		if isSignalled(saturated) {
			return latest, errSubscriberSaturated
		}
		batch, err := s.Provider.ReadEventBatch(state.lastSent, epoch, s.eventBatchSize)
		if err != nil {
			return batch, err
		}
		latest = batch
		for _, event := range batch.Events {
			if isSignalled(saturated) {
				return batch, errSubscriberSaturated
			}
			dto, err := controlapi.DurableEventDTO(event)
			if err != nil {
				return batch, err
			}
			if err := writer.writeJSON("durable", event.ID, true, dto); err != nil {
				return batch, err
			}
			state.lastSent = event.ID
		}
		if len(batch.Events) < s.eventBatchSize {
			return batch, nil
		}
	}
}

func (s *Server) finishDrainError(writer *sseWriter, state *eventStreamState, batch coordinator.EventBatchRead, epoch string, err error) {
	if errors.Is(err, errSubscriberSaturated) {
		s.writeSaturationResync(writer, state, epoch)
		return
	}
	if reason, semantic := eventResyncReason(err); semantic {
		state.reason = string(reason)
		if writeErr := writer.writeJSON("resync-required", 0, false, s.resyncPayload(reason, state.lastSent, batch)); writeErr != nil {
			state.internal = writeErr
		}
		return
	}
	state.reason, state.internal = "replay_failed", err
}

func (s *Server) writeSaturationResync(writer *sseWriter, state *eventStreamState, epoch string) {
	batch, err := s.Provider.ReadEventBatch(state.lastSent, epoch, 1)
	if _, semantic := eventResyncReason(err); semantic {
		// Metadata is still returned on the typed refusal and is all the resync
		// frame needs. Saturation remains the reason presented to clients.
		err = nil
	}
	state.reason = string(controlapi.ResyncSubscriberSaturated)
	if err != nil {
		state.internal = err
	}
	if writeErr := writer.writeJSON("resync-required", 0, false,
		s.resyncPayload(controlapi.ResyncSubscriberSaturated, state.lastSent, batch)); writeErr != nil {
		state.internal = writeErr
	}
}

func eventResyncReason(err error) (controlapi.ResyncReason, bool) {
	switch {
	case errors.Is(err, coordinator.ErrEventCursorTruncated):
		return controlapi.ResyncCursorTruncated, true
	case errors.Is(err, coordinator.ErrEventCursorAhead):
		return controlapi.ResyncCursorAhead, true
	case errors.Is(err, coordinator.ErrEventEpochMismatch):
		return controlapi.ResyncEpochMismatch, true
	case errors.Is(err, coordinator.ErrEventEpochRequired):
		return controlapi.ResyncEpochRequired, true
	default:
		return "", false
	}
}

func (s *Server) eventSnapshot(watermark int64, epoch string) controlapi.Snapshot {
	return controlapi.Snapshot{Watermark: watermark, LogEpoch: epoch, Incarnation: s.Provider.WorkerIncarnation()}
}

func (s *Server) resyncPayload(reason controlapi.ResyncReason, requested int64, batch coordinator.EventBatchRead) controlapi.ResyncRequired {
	return controlapi.ResyncRequired{
		Reason: reason, RequestedCursor: requested, RetentionFloor: batch.RetentionFloor,
		Snapshot: s.eventSnapshot(batch.Head, batch.LogEpoch),
	}
}

func isSignalled(signal <-chan struct{}) bool {
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

func streamWriteReason(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return "client_disconnect"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "write_timeout"
	}
	return "write_failed"
}

type sseWriter struct {
	response http.ResponseWriter
	control  *http.ResponseController
	timeout  time.Duration
}

func newSSEWriter(response http.ResponseWriter, timeout time.Duration) *sseWriter {
	return &sseWriter{response: response, control: http.NewResponseController(response), timeout: timeout}
}

func (w *sseWriter) clearDeadline() error {
	err := w.control.SetWriteDeadline(time.Time{})
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func (w *sseWriter) writeJSON(eventName string, id int64, includeID bool, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var frame bytes.Buffer
	if includeID {
		fmt.Fprintf(&frame, "id: %d\n", id)
	}
	fmt.Fprintf(&frame, "event: %s\ndata: ", eventName)
	frame.Write(data)
	frame.WriteString("\n\n")
	return w.writeFrame(frame.Bytes())
}

func (w *sseWriter) writeComment(comment string) error {
	return w.writeFrame([]byte(": " + comment + "\n\n"))
}

func (w *sseWriter) writeFrame(frame []byte) error {
	if err := w.control.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	defer func() { _ = w.clearDeadline() }()
	if _, err := w.response.Write(frame); err != nil {
		return err
	}
	if err := w.control.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}
