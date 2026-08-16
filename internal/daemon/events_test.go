package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/controlapi"
	"github.com/aptx-health/agent-minder/internal/coordinator"
	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/eventbus"
)

type eventProvider struct {
	coordinator.StateProvider
	store       *db.Store
	deployment  string
	incarnation string
	bus         *eventbus.Bus[coordinator.Envelope]
}

func (p *eventProvider) DeploymentID() string      { return p.deployment }
func (p *eventProvider) WorkerIncarnation() string { return p.incarnation }
func (p *eventProvider) SnapshotMarker() (coordinator.SnapshotMarker, error) {
	marker, err := p.store.SnapshotMarker()
	return coordinator.SnapshotMarker{Watermark: marker.Watermark, LogEpoch: marker.LogEpoch}, err
}
func (p *eventProvider) SubscribeEventsFromNow() (*eventbus.Subscription[coordinator.Envelope], error) {
	subscription, _, err := p.bus.SubscribeFromNow()
	return subscription, err
}
func (p *eventProvider) ReadEventBatch(after int64, epoch string, limit int) (coordinator.EventBatchRead, error) {
	batch, err := p.store.ReadEventBatch(p.deployment, after, epoch, limit)
	return coordinator.EventBatchRead{Events: batch.Events, RetentionFloor: batch.RetentionFloor, Head: batch.Head, LogEpoch: batch.LogEpoch}, err
}

func newEventFixture(t *testing.T, configure func(*ServerConfig)) (*Server, *eventProvider) {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	store := db.NewStore(conn)
	deployment := &db.Deployment{ID: "deploy-1", RepoDir: t.TempDir(), Owner: "acme", Repo: "widgets", Mode: "issues"}
	if err := store.CreateDeployment(deployment); err != nil {
		t.Fatal(err)
	}
	provider := &eventProvider{store: store, deployment: deployment.ID, incarnation: "incarnation-1", bus: eventbus.New[coordinator.Envelope](64)}
	t.Cleanup(provider.bus.Close)
	cfg := ServerConfig{DeployID: deployment.ID, APIKey: "secret", V1RequestID: func() string { return "events-request" }}
	if configure != nil {
		configure(&cfg)
	}
	server := NewServer(cfg)
	server.Provider = provider
	return server, provider
}

func appendEvent(t *testing.T, provider *eventProvider, deployment, summary string, at time.Time) *db.Event {
	t.Helper()
	event := &db.Event{Time: at, DeploymentID: deployment, Type: "info", Severity: "info", Summary: summary}
	if err := provider.store.AppendEvent(event); err != nil {
		t.Fatal(err)
	}
	return event
}

type streamResponse struct {
	header http.Header
	frames chan []byte
	mu     sync.Mutex
	status int
}

func newStreamResponse() *streamResponse {
	return &streamResponse{header: make(http.Header), frames: make(chan []byte, 256)}
}
func (w *streamResponse) Header() http.Header { return w.header }
func (w *streamResponse) WriteHeader(status int) {
	w.mu.Lock()
	if w.status == 0 {
		w.status = status
	}
	w.mu.Unlock()
}
func (w *streamResponse) Write(data []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	w.frames <- append([]byte(nil), data...)
	return len(data), nil
}
func (w *streamResponse) Flush() {}

func startEventStream(t *testing.T, server *Server, writer http.ResponseWriter, path string, headers map[string]string) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	request.Header.Set("X-API-Key", "secret")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.middleware(server.mux).ServeHTTP(writer, request)
	}()
	return cancel, done
}

func nextFrame(t *testing.T, response *streamResponse) string {
	t.Helper()
	select {
	case frame := <-response.frames:
		return string(frame)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE frame")
		return ""
	}
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not exit")
	}
}

func TestV1EventsExactFramesAndIDs(t *testing.T) {
	server, provider := newEventFixture(t, func(cfg *ServerConfig) { cfg.EventHeartbeatInterval = 20 * time.Millisecond })
	at := time.Date(2026, 8, 16, 19, 20, 21, 123456789, time.UTC)
	durable := appendEvent(t, provider, provider.deployment, "line one\nline two", at)
	epoch, _ := provider.store.EventLogEpoch()

	response := newStreamResponse()
	cancel, done := startEventStream(t, server, response, "/api/v1/events", nil)
	defer func() { cancel(); waitDone(t, done) }()

	wantReady := fmt.Sprintf("event: ready\ndata: {\"snapshot\":{\"watermark\":%d,\"log_epoch\":%q,\"incarnation\":\"incarnation-1\"},\"heartbeat_interval_seconds\":1}\n\n", durable.ID, epoch)
	if got := nextFrame(t, response); got != wantReady {
		t.Fatalf("ready frame\n got: %q\nwant: %q", got, wantReady)
	}
	wantDurable := fmt.Sprintf("id: %d\nevent: durable\ndata: {\"id\":%d,\"timestamp\":\"2026-08-16T19:20:21.123456789Z\",\"deployment_id\":\"deploy-1\",\"job_id\":null,\"run_id\":null,\"type\":\"info\",\"severity\":\"info\",\"summary\":\"line one\\nline two\",\"data\":null}\n\n", durable.ID, durable.ID)
	if got := nextFrame(t, response); got != wantDurable {
		t.Fatalf("durable frame\n got: %q\nwant: %q", got, wantDurable)
	}

	ephemeral := coordinator.Envelope{Time: at.Add(time.Second), DeploymentID: provider.deployment, Type: "info", Severity: "info", Summary: "working"}
	if _, err := provider.bus.Publish(ephemeral); err != nil {
		t.Fatal(err)
	}
	wantEphemeral := "event: ephemeral\ndata: {\"event\":{\"timestamp\":\"2026-08-16T19:20:22.123456789Z\",\"deployment_id\":\"deploy-1\",\"job_id\":null,\"run_id\":null,\"type\":\"info\",\"severity\":\"info\",\"summary\":\"working\",\"data\":null},\"incarnation\":\"incarnation-1\"}\n\n"
	if got := nextFrame(t, response); got != wantEphemeral {
		t.Fatalf("ephemeral frame\n got: %q\nwant: %q", got, wantEphemeral)
	}
	if got := nextFrame(t, response); got != ": heartbeat\n\n" {
		t.Fatalf("heartbeat = %q", got)
	}
	if response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
	}
	for _, frame := range []string{wantReady, wantEphemeral, ": heartbeat\n\n"} {
		if strings.Contains(frame, "id:") {
			t.Fatalf("id-less frame acquired an SSE id: %q", frame)
		}
	}
	for _, line := range strings.Split(wantDurable, "\n") {
		if strings.HasPrefix(line, "data:") && strings.Count(wantDurable, "\ndata:") != 1 {
			t.Fatal("durable JSON escaped into more than one SSE data line")
		}
	}
}

func TestV1EventsCursorValidationAndResync(t *testing.T) {
	server, provider := newEventFixture(t, func(cfg *ServerConfig) { cfg.EventHeartbeatInterval = time.Hour })
	at := time.Date(2026, 8, 16, 20, 0, 0, 0, time.UTC)
	first := appendEvent(t, provider, provider.deployment, "first", at)
	epoch, _ := provider.store.EventLogEpoch()

	for _, raw := range []string{"nope", "-1"} {
		recorder := requestV1(server, "/api/v1/events?after="+raw)
		assertV1Error(t, recorder, http.StatusBadRequest, controlapi.ErrorInvalidCursor)
	}

	tests := []struct {
		name, path, wantReason string
	}{
		{"epoch required", fmt.Sprintf("/api/v1/events?after=%d", first.ID), "epoch_required"},
		{"epoch mismatch", fmt.Sprintf("/api/v1/events?after=%d&log_epoch=wrong", first.ID), "epoch_mismatch"},
		{"cursor ahead", fmt.Sprintf("/api/v1/events?after=%d&log_epoch=%s", first.ID+10, epoch), "cursor_ahead"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := newStreamResponse()
			_, done := startEventStream(t, server, response, test.path, nil)
			frame := nextFrame(t, response)
			waitDone(t, done)
			if !strings.HasPrefix(frame, "event: resync-required\ndata: ") || !strings.Contains(frame, `"reason":"`+test.wantReason+`"`) || strings.Contains(frame, "id:") {
				t.Fatalf("resync frame = %q", frame)
			}
		})
	}

	// Last-Event-ID wins over a malformed original query cursor. This accepted
	// reconnect starts at first.ID and therefore does not replay it.
	response := newStreamResponse()
	cancel, done := startEventStream(t, server, response, "/api/v1/events?after=garbage&log_epoch="+epoch, map[string]string{"Last-Event-ID": fmt.Sprint(first.ID)})
	if frame := nextFrame(t, response); !strings.HasPrefix(frame, "event: ready\n") {
		t.Fatalf("header precedence did not accept reconnect: %q", frame)
	}
	select {
	case frame := <-response.frames:
		t.Fatalf("reconnect unexpectedly replayed an event: %q", frame)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	waitDone(t, done)

	second := appendEvent(t, provider, provider.deployment, "second", at.Add(time.Second))
	third := appendEvent(t, provider, provider.deployment, "third", at.Add(2*time.Second))
	if _, err := provider.store.PruneEvents(2); err != nil {
		t.Fatal(err)
	}
	floor, _ := provider.store.EventsTruncatedThrough()
	if floor != first.ID {
		t.Fatalf("floor = %d, want %d", floor, first.ID)
	}

	response = newStreamResponse()
	cancel, done = startEventStream(t, server, response, fmt.Sprintf("/api/v1/events?after=%d&log_epoch=%s", floor, epoch), nil)
	_ = nextFrame(t, response) // ready
	if frame := nextFrame(t, response); !strings.HasPrefix(frame, fmt.Sprintf("id: %d\n", second.ID)) {
		t.Fatalf("floor equality did not replay second: %q", frame)
	}
	if frame := nextFrame(t, response); !strings.HasPrefix(frame, fmt.Sprintf("id: %d\n", third.ID)) {
		t.Fatalf("floor equality did not replay third: %q", frame)
	}
	cancel()
	waitDone(t, done)

	response = newStreamResponse()
	_, done = startEventStream(t, server, response, "/api/v1/events?after=0&log_epoch="+epoch, nil)
	frame := nextFrame(t, response)
	waitDone(t, done)
	if !strings.Contains(frame, `"reason":"cursor_truncated"`) || !strings.Contains(frame, fmt.Sprintf(`"retention_floor":%d`, floor)) {
		t.Fatalf("below-floor resync = %q", frame)
	}
}

func TestV1EventsDeploymentHolesAndRestart(t *testing.T) {
	server, provider := newEventFixture(t, func(cfg *ServerConfig) { cfg.EventHeartbeatInterval = time.Hour })
	at := time.Date(2026, 8, 16, 21, 0, 0, 0, time.UTC)
	mine1 := appendEvent(t, provider, provider.deployment, "mine one", at)
	foreign := appendEvent(t, provider, "deploy-2", "foreign", at)
	mine2 := appendEvent(t, provider, provider.deployment, "mine two", at)
	epoch, _ := provider.store.EventLogEpoch()

	response := newStreamResponse()
	cancel, done := startEventStream(t, server, response, "/api/v1/events", nil)
	ready := nextFrame(t, response)
	if !strings.Contains(ready, fmt.Sprintf(`"watermark":%d`, mine2.ID)) {
		t.Fatalf("ready watermark = %q", ready)
	}
	for _, want := range []int64{mine1.ID, mine2.ID} {
		if frame := nextFrame(t, response); !strings.HasPrefix(frame, fmt.Sprintf("id: %d\n", want)) || strings.Contains(frame, fmt.Sprintf("id: %d\n", foreign.ID)) {
			t.Fatalf("scoped replay frame = %q", frame)
		}
	}
	// Live envelopes are scoped too: foreign durable envelopes are wake-ups
	// only and foreign ephemeral envelopes are never forwarded.
	if _, err := provider.bus.Publish(coordinator.Envelope{ID: foreign.ID, Time: at, DeploymentID: "deploy-2", Type: "info", Severity: "info", Summary: "foreign wake"}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.bus.Publish(coordinator.Envelope{Time: at, DeploymentID: "deploy-2", Type: "info", Severity: "info", Summary: "foreign live"}); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-response.frames:
		t.Fatalf("foreign live event leaked: %q", frame)
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := provider.bus.Publish(coordinator.Envelope{Time: at, DeploymentID: provider.deployment, Type: "info", Severity: "info", Summary: "own live"}); err != nil {
		t.Fatal(err)
	}
	if frame := nextFrame(t, response); !strings.Contains(frame, `"summary":"own live"`) {
		t.Fatalf("own live event missing after foreign wakeups: %q", frame)
	}
	cancel()
	waitDone(t, done)

	// A global id owned by another deployment is a valid cursor, not a gap.
	response = newStreamResponse()
	cancel, done = startEventStream(t, server, response, fmt.Sprintf("/api/v1/events?after=%d&log_epoch=%s", foreign.ID, epoch), nil)
	_ = nextFrame(t, response)
	if frame := nextFrame(t, response); !strings.HasPrefix(frame, fmt.Sprintf("id: %d\n", mine2.ID)) {
		t.Fatalf("resume from global hole = %q", frame)
	}
	cancel()
	waitDone(t, done)

	oldBus := provider.bus
	restarted := &eventProvider{store: provider.store, deployment: provider.deployment, incarnation: "incarnation-2", bus: eventbus.New[coordinator.Envelope](64)}
	t.Cleanup(restarted.bus.Close)
	server.Provider = restarted
	response = newStreamResponse()
	cancel, done = startEventStream(t, server, response, fmt.Sprintf("/api/v1/events?after=%d&log_epoch=%s", mine2.ID, epoch), nil)
	ready = nextFrame(t, response)
	if !strings.Contains(ready, `"log_epoch":"`+epoch+`"`) || !strings.Contains(ready, `"incarnation":"incarnation-2"`) {
		t.Fatalf("restart ready = %q", ready)
	}
	cancel()
	waitDone(t, done)
	oldBus.Close()
}

type blockingResponse struct {
	*streamResponse
	blockOn int64
	writes  atomic.Int64
	entered chan struct{}
	release chan struct{}
}

func (w *blockingResponse) Write(data []byte) (int, error) {
	if w.writes.Add(1) == w.blockOn {
		close(w.entered)
		<-w.release
	}
	return w.streamResponse.Write(data)
}

func TestV1EventsSubscriberSaturation(t *testing.T) {
	server, provider := newEventFixture(t, func(cfg *ServerConfig) {
		cfg.EventHeartbeatInterval = time.Hour
		cfg.EventQueueSize = 1
	})
	response := &blockingResponse{streamResponse: newStreamResponse(), blockOn: 2, entered: make(chan struct{}), release: make(chan struct{})}
	_, done := startEventStream(t, server, response, "/api/v1/events", nil)
	_ = nextFrame(t, response.streamResponse) // ready
	base := coordinator.Envelope{Time: time.Now(), DeploymentID: provider.deployment, Type: "info", Severity: "info"}
	if _, err := provider.bus.Publish(base); err != nil {
		t.Fatal(err)
	}
	select {
	case <-response.entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not stall on ephemeral frame")
	}
	for i := 0; i < 10; i++ {
		base.Summary = fmt.Sprintf("queued-%d", i)
		if _, err := provider.bus.Publish(base); err != nil {
			t.Fatal(err)
		}
	}
	close(response.release)
	resync := ""
	for resync == "" {
		frame := nextFrame(t, response.streamResponse)
		if strings.HasPrefix(frame, "event: resync-required\n") {
			resync = frame
		}
	}
	waitDone(t, done)
	if !strings.Contains(resync, `"reason":"subscriber_saturated"`) || strings.Contains(resync, "id:") {
		t.Fatalf("saturation resync = %q", resync)
	}
}

func TestV1EventsConnectionLimitsRelease(t *testing.T) {
	limiter := NewStreamLimiter(4, 64)
	server, _ := newEventFixture(t, func(cfg *ServerConfig) {
		cfg.EventHeartbeatInterval = time.Hour
		cfg.V1StreamLimiter = limiter
		cfg.V1ClientKey = func(*http.Request) string { return "same-client" }
	})
	type active struct {
		cancel context.CancelFunc
		done   <-chan struct{}
	}
	connections := make([]active, 0, 4)
	for i := 0; i < 4; i++ {
		response := newStreamResponse()
		cancel, done := startEventStream(t, server, response, "/api/v1/events", nil)
		_ = nextFrame(t, response)
		connections = append(connections, active{cancel, done})
	}
	rejected := requestV1(server, "/api/v1/events")
	assertV1Error(t, rejected, http.StatusTooManyRequests, controlapi.ErrorRateLimited)
	if rejected.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q", rejected.Header().Get("Retry-After"))
	}
	connections[0].cancel()
	waitDone(t, connections[0].done)
	response := newStreamResponse()
	cancel, done := startEventStream(t, server, response, "/api/v1/events", nil)
	_ = nextFrame(t, response)
	cancel()
	waitDone(t, done)
	for _, connection := range connections[1:] {
		connection.cancel()
		waitDone(t, connection.done)
	}
}

type deadlineResponse struct {
	header   http.Header
	mu       sync.Mutex
	deadline time.Time
}

func (w *deadlineResponse) Header() http.Header { return w.header }
func (w *deadlineResponse) WriteHeader(int)     {}
func (w *deadlineResponse) Flush()              {}
func (w *deadlineResponse) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadline = deadline
	w.mu.Unlock()
	return nil
}
func (w *deadlineResponse) Write([]byte) (int, error) {
	w.mu.Lock()
	deadline := w.deadline
	w.mu.Unlock()
	if deadline.IsZero() {
		return 0, errors.New("write had no deadline")
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	<-timer.C
	return 0, os.ErrDeadlineExceeded
}

func TestV1EventsStalledWriterTimesOutAndReleasesLimit(t *testing.T) {
	limiter := NewStreamLimiter(1, 1)
	server, _ := newEventFixture(t, func(cfg *ServerConfig) {
		cfg.V1StreamLimiter = limiter
		cfg.EventWriteTimeout = 20 * time.Millisecond
	})
	writer := &deadlineResponse{header: make(http.Header)}
	_, done := startEventStream(t, server, writer, "/api/v1/events", nil)
	waitDone(t, done)

	response := newStreamResponse()
	cancel, nextDone := startEventStream(t, server, response, "/api/v1/events", nil)
	if frame := nextFrame(t, response); !strings.HasPrefix(frame, "event: ready\n") {
		t.Fatalf("connection slot was not released: %q", frame)
	}
	cancel()
	waitDone(t, nextDone)
}

func TestV1EventsRealServerSurvivesDefaultWriteTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("35-second real-server liveness test")
	}
	server, _ := newEventFixture(t, nil)
	if server.srv.WriteTimeout != 30*time.Second {
		t.Fatalf("server WriteTimeout = %s, want 30s", server.srv.WriteTimeout)
	}
	httpServer := httptest.NewUnstartedServer(server.srv.Handler)
	httpServer.Config.WriteTimeout = server.srv.WriteTimeout
	httpServer.Start()
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/v1/events", nil)
	request.Header.Set("X-API-Key", "secret")
	response, err := httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	reader := bufio.NewReader(response.Body)
	readFrame := func() string {
		var frame strings.Builder
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				t.Fatalf("stream ended before 35 seconds: %v", readErr)
			}
			frame.WriteString(line)
			if line == "\n" {
				return frame.String()
			}
		}
	}
	if ready := readFrame(); !strings.HasPrefix(ready, "event: ready\n") {
		t.Fatalf("ready = %q", ready)
	}
	started := time.Now()
	for i := 0; i < 2; i++ {
		if heartbeat := readFrame(); heartbeat != ": heartbeat\n\n" {
			t.Fatalf("heartbeat %d = %q", i+1, heartbeat)
		}
	}
	remaining := 35*time.Second - time.Since(started)
	if remaining > 0 {
		timer := time.NewTimer(remaining)
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestSSEPayloadsAreJSON(t *testing.T) {
	// Guard the test parser assumptions and the single-line data contract.
	payload := `{"summary":"line one\\nline two"}`
	if !json.Valid([]byte(payload)) || strings.Contains(payload, "\n") {
		t.Fatal("fixture is not single-line JSON")
	}
}
