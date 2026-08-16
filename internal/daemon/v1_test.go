package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/controlapi"
	"github.com/aptx-health/agent-minder/internal/coordinator"
)

type metaProvider struct {
	coordinator.StateProvider
	marker      coordinator.SnapshotMarker
	incarnation string
	err         error
	panicRead   bool
}

func (p *metaProvider) SnapshotMarker() (coordinator.SnapshotMarker, error) {
	if p.panicRead {
		panic("provider exploded")
	}
	return p.marker, p.err
}

func (p *metaProvider) WorkerIncarnation() string { return p.incarnation }

type fixedLimiter struct {
	allowed bool
	retry   time.Duration
	key     string
}

func (l *fixedLimiter) Allow(key string, _ time.Time) (bool, time.Duration) {
	l.key = key
	return l.allowed, l.retry
}

func newV1TestServer(provider coordinator.StateProvider, overrides func(*ServerConfig)) *Server {
	cfg := ServerConfig{
		DeployID: "deploy-1", APIKey: "secret", BuildVersion: "1.2.3",
		V1RequestID: func() string { return "request-1" },
	}
	if overrides != nil {
		overrides(&cfg)
	}
	server := NewServer(cfg)
	server.Provider = provider
	return server
}

func requestV1(server *Server, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-API-Key", "secret")
	recorder := httptest.NewRecorder()
	server.middleware(server.mux).ServeHTTP(recorder, req)
	return recorder
}

func TestV1MetaGolden(t *testing.T) {
	provider := &metaProvider{
		marker:      coordinator.SnapshotMarker{Watermark: 42, LogEpoch: "epoch-1"},
		incarnation: "incarnation-1",
	}
	server := newV1TestServer(provider, nil)
	recorder := requestV1(server, "/api/v1/meta")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Request-ID"); got != "request-1" {
		t.Fatalf("X-Request-ID = %q", got)
	}
	want, err := os.ReadFile("testdata/meta.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var gotJSON, wantJSON any
	if err := json.Unmarshal(recorder.Body.Bytes(), &gotJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("meta changed\n got: %s\nwant: %s", recorder.Body.Bytes(), want)
	}
}

func TestV1MetaProviderUnavailable(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider coordinator.StateProvider
	}{
		{name: "nil"},
		{name: "failing", provider: &metaProvider{err: errors.New("sqlite details must stay private")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newV1TestServer(test.provider, nil)
			recorder := requestV1(server, "/api/v1/meta")
			assertV1Error(t, recorder, http.StatusServiceUnavailable, controlapi.ErrorProviderUnavailable)
			if strings.Contains(recorder.Body.String(), "sqlite") {
				t.Fatalf("response exposed provider error: %s", recorder.Body.String())
			}
		})
	}
}

func TestV1MetaEncodingFailure(t *testing.T) {
	provider := &metaProvider{marker: coordinator.SnapshotMarker{LogEpoch: "epoch"}, incarnation: "inc"}
	server := newV1TestServer(provider, func(cfg *ServerConfig) {
		cfg.V1Encoder = func(io.Writer, any) error { return errors.New("encoder failure") }
	})
	recorder := requestV1(server, "/api/v1/meta")
	assertV1Error(t, recorder, http.StatusInternalServerError, controlapi.ErrorInternal)
}

func TestV1PanicRecoveryKeepsServerAlive(t *testing.T) {
	provider := &metaProvider{panicRead: true}
	server := newV1TestServer(provider, nil)
	recorder := requestV1(server, "/api/v1/meta")
	assertV1Error(t, recorder, http.StatusInternalServerError, controlapi.ErrorInternal)

	provider.panicRead = false
	provider.marker = coordinator.SnapshotMarker{LogEpoch: "epoch"}
	provider.incarnation = "inc"
	if next := requestV1(server, "/api/v1/meta"); next.Code != http.StatusOK {
		t.Fatalf("server did not survive panic: status %d", next.Code)
	}
}

func TestV1StructuredRequestLog(t *testing.T) {
	var logOutput bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logOutput, nil))
	provider := &metaProvider{err: errors.New("internal database failure")}
	server := newV1TestServer(provider, func(cfg *ServerConfig) { cfg.Logger = logger })
	_ = requestV1(server, "/api/v1/meta")

	var record map[string]any
	lines := strings.Split(strings.TrimSpace(logOutput.String()), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &record); err != nil {
		t.Fatalf("decode log: %v; %s", err, logOutput.String())
	}
	for key, want := range map[string]any{
		"request_id": "request-1", "method": "GET", "route": "GET /api/v1/meta",
		"deployment_id": "deploy-1", "status": float64(http.StatusServiceUnavailable),
		"internal_error": "internal database failure",
	} {
		if got := record[key]; got != want {
			t.Errorf("log[%s] = %#v, want %#v", key, got, want)
		}
	}
	if _, ok := record["duration"]; !ok {
		t.Error("log omitted duration")
	}
}

func TestV1RateLimit(t *testing.T) {
	limiter := &fixedLimiter{allowed: false, retry: 1500 * time.Millisecond}
	server := newV1TestServer(&metaProvider{}, func(cfg *ServerConfig) {
		cfg.V1Limiter = limiter
		cfg.V1ClientKey = func(*http.Request) string { return "client-1" }
	})
	recorder := requestV1(server, "/api/v1/meta")
	assertV1Error(t, recorder, http.StatusTooManyRequests, controlapi.ErrorRateLimited)
	if got := recorder.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}
	if limiter.key != "client-1" {
		t.Fatalf("limiter key = %q", limiter.key)
	}
}

func TestTokenBucketDefaults(t *testing.T) {
	limiter := NewTokenBucketLimiter(0, 0)
	now := time.Unix(0, 0)
	for i := 0; i < defaultRateLimitBurst; i++ {
		if ok, _ := limiter.Allow("client", now); !ok {
			t.Fatalf("request %d unexpectedly rejected", i+1)
		}
	}
	if ok, retry := limiter.Allow("client", now); ok || retry != time.Second {
		t.Fatalf("burst+1 = allowed %v retry %s", ok, retry)
	}
}

func assertV1Error(t *testing.T, recorder *httptest.ResponseRecorder, status int, code controlapi.ErrorCode) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", recorder.Code, status, recorder.Body.String())
	}
	var envelope controlapi.ErrorEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if envelope.Error.Code != code {
		t.Fatalf("code = %q, want %q", envelope.Error.Code, code)
	}
}
