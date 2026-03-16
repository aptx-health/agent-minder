package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- mock provider ---

// mockProvider records calls and returns errors from a predefined sequence.
// After the error sequence is exhausted, it returns successResp.
type mockProvider struct {
	name        string
	calls       atomic.Int32
	errSequence []error   // errors to return in order; nil means success
	successResp *Response // returned when errSequence is exhausted or entry is nil
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) Complete(_ context.Context, _ *Request) (*Response, error) {
	idx := int(m.calls.Add(1)) - 1
	if idx < len(m.errSequence) && m.errSequence[idx] != nil {
		return nil, m.errSequence[idx]
	}
	return m.successResp, nil
}

func (m *mockProvider) callCount() int {
	return int(m.calls.Load())
}

// noWait is a wait function that returns immediately (no actual delay).
func noWait(_ context.Context, _ time.Duration) error { return nil }

// --- helpers to create SDK-shaped errors ---

// newOpenAITestError creates an error from the OpenAI mock server with the given status code.
// We use a real httptest server to get a properly-typed *openai.Error.
func newOpenAITestError(t *testing.T, statusCode int) error {
	t.Helper()

	errBody := map[string]any{
		"error": map[string]any{
			"message": http.StatusText(statusCode),
			"type":    "test_error",
			"code":    fmt.Sprintf("test_%d", statusCode),
		},
	}

	srv := newTestServer(t, statusCode, errBody)
	defer srv.Close()

	provider, err := newOpenAIProvider(&providerConfig{
		APIKey:  "test-key",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("newOpenAIProvider: %v", err)
	}

	_, err = provider.Complete(context.Background(), &Request{
		Model:    "gpt-4",
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err == nil {
		t.Fatalf("expected error for status %d, got nil", statusCode)
	}
	return err
}

// --- error classification tests ---

func TestClassifyError_RateLimit(t *testing.T) {
	err := newOpenAITestError(t, http.StatusTooManyRequests)
	kind, code := classifyError(err)
	if kind != ErrorKindRateLimit {
		t.Errorf("kind = %v, want %v", kind, ErrorKindRateLimit)
	}
	if code != 429 {
		t.Errorf("code = %d, want 429", code)
	}
	if !isRetryable(kind) {
		t.Error("rate limit should be retryable")
	}
}

func TestClassifyError_Auth(t *testing.T) {
	err := newOpenAITestError(t, http.StatusUnauthorized)
	kind, code := classifyError(err)
	if kind != ErrorKindAuth {
		t.Errorf("kind = %v, want %v", kind, ErrorKindAuth)
	}
	if code != 401 {
		t.Errorf("code = %d, want 401", code)
	}
	if isRetryable(kind) {
		t.Error("auth error should NOT be retryable")
	}
}

func TestClassifyError_Forbidden(t *testing.T) {
	err := newOpenAITestError(t, http.StatusForbidden)
	kind, _ := classifyError(err)
	if kind != ErrorKindAuth {
		t.Errorf("kind = %v, want %v", kind, ErrorKindAuth)
	}
	if isRetryable(kind) {
		t.Error("forbidden error should NOT be retryable")
	}
}

func TestClassifyError_ServerError(t *testing.T) {
	err := newOpenAITestError(t, http.StatusInternalServerError)
	kind, code := classifyError(err)
	if kind != ErrorKindServer {
		t.Errorf("kind = %v, want %v", kind, ErrorKindServer)
	}
	if code != 500 {
		t.Errorf("code = %d, want 500", code)
	}
	if !isRetryable(kind) {
		t.Error("server error should be retryable")
	}
}

func TestClassifyError_BadGateway(t *testing.T) {
	err := newOpenAITestError(t, http.StatusBadGateway)
	kind, _ := classifyError(err)
	if kind != ErrorKindServer {
		t.Errorf("kind = %v, want %v", kind, ErrorKindServer)
	}
	if !isRetryable(kind) {
		t.Error("bad gateway should be retryable")
	}
}

func TestClassifyError_BadRequest(t *testing.T) {
	err := newOpenAITestError(t, http.StatusBadRequest)
	kind, code := classifyError(err)
	if kind != ErrorKindBadRequest {
		t.Errorf("kind = %v, want %v", kind, ErrorKindBadRequest)
	}
	if code != 400 {
		t.Errorf("code = %d, want 400", code)
	}
	if isRetryable(kind) {
		t.Error("bad request should NOT be retryable")
	}
}

func TestClassifyError_ContextDeadlineExceeded(t *testing.T) {
	kind, code := classifyError(context.DeadlineExceeded)
	if kind != ErrorKindTimeout {
		t.Errorf("kind = %v, want %v", kind, ErrorKindTimeout)
	}
	if code != 0 {
		t.Errorf("code = %d, want 0", code)
	}
	if !isRetryable(kind) {
		t.Error("timeout should be retryable")
	}
}

func TestClassifyError_ContextCanceled(t *testing.T) {
	kind, _ := classifyError(context.Canceled)
	if kind != ErrorKindCanceled {
		t.Errorf("kind = %v, want %v", kind, ErrorKindCanceled)
	}
	if isRetryable(kind) {
		t.Error("canceled should NOT be retryable")
	}
}

func TestClassifyError_NilError(t *testing.T) {
	kind, code := classifyError(nil)
	if kind != ErrorKindUnknown {
		t.Errorf("kind = %v, want %v", kind, ErrorKindUnknown)
	}
	if code != 0 {
		t.Errorf("code = %d, want 0", code)
	}
}

func TestClassifyError_GenericError(t *testing.T) {
	kind, code := classifyError(errors.New("something broke"))
	if kind != ErrorKindUnknown {
		t.Errorf("kind = %v, want %v", kind, ErrorKindUnknown)
	}
	if code != 0 {
		t.Errorf("code = %d, want 0", code)
	}
}

func TestClassifyStatusCode(t *testing.T) {
	tests := []struct {
		code int
		want ErrorKind
	}{
		{429, ErrorKindRateLimit},
		{401, ErrorKindAuth},
		{403, ErrorKindAuth},
		{408, ErrorKindTimeout},
		{504, ErrorKindTimeout},
		{500, ErrorKindServer},
		{502, ErrorKindServer},
		{503, ErrorKindServer},
		{400, ErrorKindBadRequest},
		{404, ErrorKindBadRequest},
		{422, ErrorKindBadRequest},
		{200, ErrorKindUnknown},
		{301, ErrorKindUnknown},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.code), func(t *testing.T) {
			got := classifyStatusCode(tt.code)
			if got != tt.want {
				t.Errorf("classifyStatusCode(%d) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestErrorKindString(t *testing.T) {
	tests := []struct {
		kind ErrorKind
		want string
	}{
		{ErrorKindRateLimit, "rate_limit"},
		{ErrorKindAuth, "auth"},
		{ErrorKindTimeout, "timeout"},
		{ErrorKindServer, "server"},
		{ErrorKindBadRequest, "bad_request"},
		{ErrorKindCanceled, "canceled"},
		{ErrorKindUnknown, "unknown"},
	}
	for _, tt := range tests {
		if got := tt.kind.String(); got != tt.want {
			t.Errorf("%v.String() = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

// --- retry middleware tests ---

func TestRetry_SuccessOnFirstAttempt(t *testing.T) {
	mock := &mockProvider{
		name:        "test",
		successResp: &Response{Content: "ok", InputToks: 10, OutputToks: 5},
	}

	rp := newRetryProviderForTest(mock, noWait)

	resp, err := rp.Complete(context.Background(), &Request{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("Content = %q, want %q", resp.Content, "ok")
	}
	if mock.callCount() != 1 {
		t.Errorf("callCount = %d, want 1", mock.callCount())
	}
}

func TestRetry_TransientThenSuccess_RateLimit(t *testing.T) {
	rateLimitErr := newOpenAITestError(t, http.StatusTooManyRequests)

	mock := &mockProvider{
		name: "test",
		errSequence: []error{
			rateLimitErr, // attempt 0: rate limit
			rateLimitErr, // attempt 1: rate limit
			nil,          // attempt 2: success
		},
		successResp: &Response{Content: "recovered", InputToks: 5, OutputToks: 3},
	}

	rp := newRetryProviderForTest(mock, noWait)

	resp, err := rp.Complete(context.Background(), &Request{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "recovered" {
		t.Errorf("Content = %q, want %q", resp.Content, "recovered")
	}
	if mock.callCount() != 3 {
		t.Errorf("callCount = %d, want 3", mock.callCount())
	}
}

func TestRetry_TransientThenSuccess_ServerError(t *testing.T) {
	serverErr := newOpenAITestError(t, http.StatusInternalServerError)

	mock := &mockProvider{
		name: "test",
		errSequence: []error{
			serverErr, // attempt 0: 500
			nil,       // attempt 1: success
		},
		successResp: &Response{Content: "recovered"},
	}

	rp := newRetryProviderForTest(mock, noWait)

	resp, err := rp.Complete(context.Background(), &Request{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "recovered" {
		t.Errorf("Content = %q, want %q", resp.Content, "recovered")
	}
	if mock.callCount() != 2 {
		t.Errorf("callCount = %d, want 2", mock.callCount())
	}
}

func TestRetry_PermanentError_AuthFailsImmediately(t *testing.T) {
	authErr := newOpenAITestError(t, http.StatusUnauthorized)

	mock := &mockProvider{
		name: "test",
		errSequence: []error{
			authErr, // should not retry
		},
		successResp: &Response{Content: "should not reach"},
	}

	rp := newRetryProviderForTest(mock, noWait)

	_, err := rp.Complete(context.Background(), &Request{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if mock.callCount() != 1 {
		t.Errorf("callCount = %d, want 1 (should not retry auth error)", mock.callCount())
	}
}

func TestRetry_PermanentError_BadRequestFailsImmediately(t *testing.T) {
	badReqErr := newOpenAITestError(t, http.StatusBadRequest)

	mock := &mockProvider{
		name: "test",
		errSequence: []error{
			badReqErr,
		},
		successResp: &Response{Content: "should not reach"},
	}

	rp := newRetryProviderForTest(mock, noWait)

	_, err := rp.Complete(context.Background(), &Request{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if mock.callCount() != 1 {
		t.Errorf("callCount = %d, want 1 (should not retry bad request)", mock.callCount())
	}
}

func TestRetry_AllRetriesExhausted(t *testing.T) {
	serverErr := newOpenAITestError(t, http.StatusInternalServerError)

	mock := &mockProvider{
		name: "test",
		errSequence: []error{
			serverErr, // attempt 0
			serverErr, // attempt 1
			serverErr, // attempt 2
			serverErr, // attempt 3 (maxRetries+1)
		},
		successResp: &Response{Content: "should not reach"},
	}

	rp := newRetryProviderForTest(mock, noWait)

	_, err := rp.Complete(context.Background(), &Request{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected error after retries exhausted, got nil")
	}

	expectedCalls := maxRetries + 1
	if mock.callCount() != expectedCalls {
		t.Errorf("callCount = %d, want %d", mock.callCount(), expectedCalls)
	}

	// Error message should mention retries exhausted.
	if !strings.Contains(err.Error(), "retries exhausted") {
		t.Errorf("error = %q, want it to contain 'retries exhausted'", err.Error())
	}
}

func TestRetry_ContextCanceled_StopsRetrying(t *testing.T) {
	serverErr := newOpenAITestError(t, http.StatusInternalServerError)

	mock := &mockProvider{
		name: "test",
		errSequence: []error{
			serverErr, // attempt 0: will trigger retry
			serverErr, // should not be reached
		},
		successResp: &Response{Content: "should not reach"},
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel the context when the retry provider tries to wait.
	rp := newRetryProviderForTest(mock, func(_ context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	})

	_, err := rp.Complete(ctx, &Request{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected error from context cancellation, got nil")
	}
	// Should have made exactly 1 call (the initial attempt), then bailed.
	if mock.callCount() != 1 {
		t.Errorf("callCount = %d, want 1", mock.callCount())
	}
}

func TestRetry_NameDelegates(t *testing.T) {
	mock := &mockProvider{name: "my-provider"}
	rp := newRetryProviderForTest(mock, noWait)
	if got := rp.Name(); got != "my-provider" {
		t.Errorf("Name() = %q, want %q", got, "my-provider")
	}
}

func TestRetry_BackoffDelaysRecorded(t *testing.T) {
	serverErr := newOpenAITestError(t, http.StatusInternalServerError)

	mock := &mockProvider{
		name: "test",
		errSequence: []error{
			serverErr, // attempt 0
			serverErr, // attempt 1
			nil,       // attempt 2: success
		},
		successResp: &Response{Content: "ok"},
	}

	var delays []time.Duration
	rp := newRetryProviderForTest(mock, func(_ context.Context, d time.Duration) error {
		delays = append(delays, d)
		return nil
	})

	_, err := rp.Complete(context.Background(), &Request{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have waited twice (after attempt 0 and after attempt 1).
	if len(delays) != 2 {
		t.Fatalf("len(delays) = %d, want 2", len(delays))
	}
	// First delay: baseDelay * 2^0 * [0.5, 1.0) = [500ms, 1s)
	if delays[0] < 500*time.Millisecond || delays[0] > 1*time.Second {
		t.Errorf("delays[0] = %v, want [500ms, 1s]", delays[0])
	}
	// Second delay: baseDelay * 2^1 * [0.5, 1.0) = [1s, 2s)
	if delays[1] < 1*time.Second || delays[1] > 2*time.Second {
		t.Errorf("delays[1] = %v, want [1s, 2s]", delays[1])
	}
}

func TestBackoffDelay(t *testing.T) {
	tests := []struct {
		attempt int
		minMs   int64 // baseDelay * 2^attempt * 0.5
		maxMs   int64 // baseDelay * 2^attempt * 1.0 (or maxDelay)
	}{
		{0, 500, 1000},     // 1s * [0.5, 1.0)
		{1, 1000, 2000},    // 2s * [0.5, 1.0)
		{2, 2000, 4000},    // 4s * [0.5, 1.0)
		{3, 4000, 8000},    // 8s * [0.5, 1.0)
		{4, 8000, 16000},   // 16s * [0.5, 1.0)
		{5, 15000, 30000},  // capped at maxDelay
		{10, 15000, 30000}, // capped at maxDelay
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("attempt_%d", tt.attempt), func(t *testing.T) {
			got := backoffDelay(tt.attempt)
			gotMs := got.Milliseconds()
			if gotMs < tt.minMs || gotMs > tt.maxMs {
				t.Errorf("backoffDelay(%d) = %v (%dms), want [%dms, %dms]",
					tt.attempt, got, gotMs, tt.minMs, tt.maxMs)
			}
		})
	}
}

// --- integration: NewProvider wraps with retry ---

func TestNewProvider_WrapsWithRetry(t *testing.T) {
	resp := chatCompletionResponse{
		ID:      "chatcmpl-retry",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "gpt-4",
		Choices: []chatCompletionChoice{
			{Index: 0, Message: chatCompletionMessage{Role: "assistant", Content: "hello"}, FinishReason: "stop"},
		},
		Usage: chatCompletionUsage{PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6},
	}

	srv := newTestServer(t, http.StatusOK, resp)
	defer srv.Close()

	provider, err := NewProvider("openai",
		WithAPIKey("test-key"),
		WithBaseURL(srv.URL),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	// Verify it's wrapped in a retryProvider.
	if _, ok := provider.(*retryProvider); !ok {
		t.Errorf("NewProvider returned %T, want *retryProvider", provider)
	}

	// Verify it still works end-to-end.
	result, err := provider.Complete(context.Background(), &Request{
		Model:    "gpt-4",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if result.Content != "hello" {
		t.Errorf("Content = %q, want %q", result.Content, "hello")
	}
}
