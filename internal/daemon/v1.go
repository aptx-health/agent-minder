package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/aptx-health/agent-minder/internal/controlapi"
	"github.com/google/uuid"
)

const defaultRateLimitPerMinute = 120
const defaultRateLimitBurst = 30

// V1RateLimiter is injectable so a later Unix-socket transport can use a
// socket-local identity and tests can make allow/deny decisions deterministically.
type V1RateLimiter interface {
	Allow(key string, now time.Time) (allowed bool, retryAfter time.Duration)
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// TokenBucketLimiter is a concurrency-safe per-client token bucket.
type TokenBucketLimiter struct {
	mu       sync.Mutex
	rate     float64
	capacity float64
	buckets  map[string]*tokenBucket
}

func NewTokenBucketLimiter(perMinute, burst int) *TokenBucketLimiter {
	if perMinute <= 0 {
		perMinute = defaultRateLimitPerMinute
	}
	if burst <= 0 {
		burst = defaultRateLimitBurst
	}
	return &TokenBucketLimiter{
		rate:     float64(perMinute) / 60,
		capacity: float64(burst),
		buckets:  make(map[string]*tokenBucket),
	}
}

func (l *TokenBucketLimiter) Allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket := l.buckets[key]
	if bucket == nil {
		bucket = &tokenBucket{tokens: l.capacity, last: now}
		l.buckets[key] = bucket
	}
	if elapsed := now.Sub(bucket.last).Seconds(); elapsed > 0 {
		bucket.tokens = math.Min(l.capacity, bucket.tokens+elapsed*l.rate)
		bucket.last = now
	}
	if bucket.tokens >= 1 {
		bucket.tokens--
		return true, 0
	}
	wait := time.Duration(math.Ceil((1 - bucket.tokens) / l.rate * float64(time.Second)))
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

type v1RequestState struct {
	internalErr error
}

type v1StateKey struct{}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusRecorder) Flush() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.status = status
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(data []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (s *Server) v1Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := uuid.NewString()
		if s.v1RequestID != nil {
			requestID = s.v1RequestID()
		}
		w.Header().Set("X-Request-ID", requestID)
		recorder := &statusRecorder{ResponseWriter: w}
		state := &v1RequestState{}
		r = r.WithContext(context.WithValue(r.Context(), v1StateKey{}, state))

		defer func() {
			if recovered := recover(); recovered != nil {
				state.internalErr = fmt.Errorf("panic: %v", recovered)
				if !recorder.wrote {
					writeV1Error(recorder, http.StatusInternalServerError, controlapi.ErrorInternal, "an internal error occurred")
				}
				s.v1Logger.ErrorContext(r.Context(), "control API handler panic",
					"request_id", requestID, "deployment_id", s.deployID,
					"panic", recovered, "stack", string(debug.Stack()))
			}
			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			internalError := ""
			if state.internalErr != nil {
				internalError = state.internalErr.Error()
			}
			s.v1Logger.LogAttrs(r.Context(), slog.LevelInfo, "control API request",
				slog.String("request_id", requestID),
				slog.String("method", r.Method),
				slog.String("route", routeName(r)),
				slog.String("deployment_id", s.deployID),
				slog.Int("status", status),
				slog.Duration("duration", time.Since(started)),
				slog.String("internal_error", internalError))
		}()

		key := s.defaultV1ClientKey(r)
		if s.v1ClientKey != nil {
			key = s.v1ClientKey(r)
		}
		if allowed, retry := s.v1Limiter.Allow(key, started); !allowed {
			seconds := int(math.Ceil(retry.Seconds()))
			if seconds < 1 {
				seconds = 1
			}
			recorder.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeV1Error(recorder, http.StatusTooManyRequests, controlapi.ErrorRateLimited, "request rate limit exceeded")
			return
		}

		next.ServeHTTP(recorder, r)
	})
}

func routeName(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return r.URL.Path
}

func (s *Server) defaultV1ClientKey(r *http.Request) string {
	remote := r.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	identity := sha256.Sum256([]byte(r.Header.Get("X-API-Key")))
	return hex.EncodeToString(identity[:]) + "@" + remote
}

func (s *Server) handleV1Meta(w http.ResponseWriter, r *http.Request) {
	response, err := (controlapi.Service{Provider: s.Provider, BuildVersion: s.buildVersion}).Meta()
	if err != nil {
		markV1InternalError(r, err)
		writeV1Error(w, http.StatusServiceUnavailable, controlapi.ErrorProviderUnavailable, "state provider is unavailable")
		return
	}
	if err := s.writeV1JSON(w, http.StatusOK, response); err != nil {
		markV1InternalError(r, err)
		writeV1Error(w, http.StatusInternalServerError, controlapi.ErrorInternal, "an internal error occurred")
	}
}

func markV1InternalError(r *http.Request, err error) {
	if state, ok := r.Context().Value(v1StateKey{}).(*v1RequestState); ok {
		state.internalErr = err
	}
}

// writeV1JSON buffers before committing headers. Encoder failure can therefore
// still return the stable internal_error envelope instead of partial JSON.
func (s *Server) writeV1JSON(w http.ResponseWriter, status int, value any) error {
	var buffer bytes.Buffer
	encode := func(dst io.Writer, value any) error { return json.NewEncoder(dst).Encode(value) }
	if s.v1Encoder != nil {
		encode = s.v1Encoder
	}
	if err := encode(&buffer, value); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err := w.Write(buffer.Bytes())
	return err
}

func writeV1Error(w http.ResponseWriter, status int, code controlapi.ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(controlapi.NewErrorEnvelope(code, message))
}
