package llm

import (
	"context"
	"errors"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go"
)

// ErrorKind classifies LLM API errors into actionable categories.
type ErrorKind int

const (
	// ErrorKindUnknown is the default for unclassified errors.
	ErrorKindUnknown ErrorKind = iota
	// ErrorKindRateLimit indicates a 429 Too Many Requests response.
	ErrorKindRateLimit
	// ErrorKindAuth indicates 401 Unauthorized or 403 Forbidden.
	ErrorKindAuth
	// ErrorKindTimeout indicates a context deadline exceeded or network timeout.
	ErrorKindTimeout
	// ErrorKindServer indicates a 5xx server error.
	ErrorKindServer
	// ErrorKindBadRequest indicates a 400-level client error (not rate limit or auth).
	ErrorKindBadRequest
)

// String returns a human-readable label for the error kind.
func (k ErrorKind) String() string {
	switch k {
	case ErrorKindRateLimit:
		return "rate_limit"
	case ErrorKindAuth:
		return "auth"
	case ErrorKindTimeout:
		return "timeout"
	case ErrorKindServer:
		return "server"
	case ErrorKindBadRequest:
		return "bad_request"
	default:
		return "unknown"
	}
}

// classifyError inspects an error and returns its kind and HTTP status code (0 if not applicable).
func classifyError(err error) (kind ErrorKind, statusCode int) {
	if err == nil {
		return ErrorKindUnknown, 0
	}

	// Check for context errors first (deadline exceeded, canceled).
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ErrorKindTimeout, 0
	}

	// Try Anthropic SDK error type.
	var anthropicErr *anthropic.Error
	if errors.As(err, &anthropicErr) {
		return classifyStatusCode(anthropicErr.StatusCode), anthropicErr.StatusCode
	}

	// Try OpenAI SDK error type.
	var openaiErr *openai.Error
	if errors.As(err, &openaiErr) {
		return classifyStatusCode(openaiErr.StatusCode), openaiErr.StatusCode
	}

	return ErrorKindUnknown, 0
}

// classifyStatusCode maps an HTTP status code to an ErrorKind.
func classifyStatusCode(code int) ErrorKind {
	switch {
	case code == http.StatusTooManyRequests:
		return ErrorKindRateLimit
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return ErrorKindAuth
	case code == http.StatusRequestTimeout || code == http.StatusGatewayTimeout:
		return ErrorKindTimeout
	case code >= 500:
		return ErrorKindServer
	case code >= 400:
		return ErrorKindBadRequest
	default:
		return ErrorKindUnknown
	}
}

// isRetryable returns true if the error kind represents a transient failure
// that may succeed on retry.
func isRetryable(kind ErrorKind) bool {
	switch kind {
	case ErrorKindRateLimit, ErrorKindServer, ErrorKindTimeout:
		return true
	default:
		return false
	}
}
