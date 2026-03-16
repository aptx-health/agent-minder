// Package llm provides a provider-agnostic interface for LLM completions.
package llm

import (
	"context"
	"fmt"
	"log/slog"
)

// Message represents a chat message.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// Request is the input to a completion call.
type Request struct {
	Model       string
	System      string // system prompt
	Messages    []Message
	MaxTokens   int
	Temperature float64
}

// Response is the output from a completion call.
type Response struct {
	Content    string
	InputToks  int
	OutputToks int
}

// Provider is the interface that LLM backends implement.
type Provider interface {
	// Complete sends a chat completion request and returns the response.
	Complete(ctx context.Context, req *Request) (*Response, error)

	// Name returns the provider name (e.g., "anthropic", "openai").
	Name() string
}

// NewProvider creates a provider by name. Supported: "anthropic", "openai".
// For OpenAI-compatible providers (DeepInfra, etc.), use "openai" with baseURL.
func NewProvider(name string, opts ...Option) (Provider, error) {
	cfg := &providerConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	var p Provider
	var err error

	switch name {
	case "anthropic":
		p, err = newAnthropicProvider(cfg)
	case "openai":
		p, err = newOpenAIProvider(cfg)
	default:
		return nil, fmt.Errorf("unknown provider: %q", name)
	}
	if err != nil {
		return nil, err
	}

	return NewRetryProvider(p, cfg.Logger), nil
}

// Option configures a provider.
type Option func(*providerConfig)

type providerConfig struct {
	APIKey  string
	BaseURL string
	Logger  *slog.Logger
}

// WithAPIKey sets the API key (overrides env var).
func WithAPIKey(key string) Option {
	return func(c *providerConfig) { c.APIKey = key }
}

// WithBaseURL sets a custom base URL (for OpenAI-compatible providers).
func WithBaseURL(url string) Option {
	return func(c *providerConfig) { c.BaseURL = url }
}

// WithLogger sets a structured logger for retry debug logging.
func WithLogger(l *slog.Logger) Option {
	return func(c *providerConfig) { c.Logger = l }
}
