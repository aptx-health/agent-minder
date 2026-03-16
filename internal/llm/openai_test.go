package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// chatCompletionResponse mirrors the OpenAI chat completion response structure
// used by the httptest server to generate valid JSON responses.
type chatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
	Usage   chatCompletionUsage    `json:"usage"`
}

type chatCompletionChoice struct {
	Index        int                   `json:"index"`
	Message      chatCompletionMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

type chatCompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// newTestServer creates an httptest server that returns the given response body
// and status code for POST /chat/completions.
func newTestServer(t *testing.T, statusCode int, body any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
}

// requestCapture is used by tests that need to inspect the request body sent
// to the mock server.
type requestCapture struct {
	Body map[string]any
}

func newCaptureServer(t *testing.T, capture *requestCapture, resp chatCompletionResponse) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		capture.Body = body

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
}

func TestOpenAIComplete_Success(t *testing.T) {
	resp := chatCompletionResponse{
		ID:      "chatcmpl-test123",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "gpt-4",
		Choices: []chatCompletionChoice{
			{
				Index:        0,
				Message:      chatCompletionMessage{Role: "assistant", Content: "Hello, world!"},
				FinishReason: "stop",
			},
		},
		Usage: chatCompletionUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
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

	result, err := provider.Complete(context.Background(), &Request{
		Model:     "gpt-4",
		System:    "You are helpful.",
		Messages:  []Message{{Role: "user", Content: "Say hello"}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if result.Content != "Hello, world!" {
		t.Errorf("Content = %q, want %q", result.Content, "Hello, world!")
	}
	if result.InputToks != 10 {
		t.Errorf("InputToks = %d, want 10", result.InputToks)
	}
	if result.OutputToks != 5 {
		t.Errorf("OutputToks = %d, want 5", result.OutputToks)
	}
}

func TestOpenAIComplete_EmptyChoices(t *testing.T) {
	resp := chatCompletionResponse{
		ID:      "chatcmpl-empty",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "gpt-4",
		Choices: []chatCompletionChoice{},
		Usage: chatCompletionUsage{
			PromptTokens:     8,
			CompletionTokens: 0,
			TotalTokens:      8,
		},
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

	result, err := provider.Complete(context.Background(), &Request{
		Model:    "gpt-4",
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if result.Content != "" {
		t.Errorf("Content = %q, want empty string", result.Content)
	}
	if result.InputToks != 8 {
		t.Errorf("InputToks = %d, want 8", result.InputToks)
	}
	if result.OutputToks != 0 {
		t.Errorf("OutputToks = %d, want 0", result.OutputToks)
	}
}

func TestOpenAIComplete_APIError(t *testing.T) {
	errBody := map[string]any{
		"error": map[string]any{
			"message": "Invalid API key",
			"type":    "invalid_request_error",
			"code":    "invalid_api_key",
		},
	}

	srv := newTestServer(t, http.StatusUnauthorized, errBody)
	defer srv.Close()

	provider, err := NewProvider("openai",
		WithAPIKey("bad-key"),
		WithBaseURL(srv.URL),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	_, err = provider.Complete(context.Background(), &Request{
		Model:    "gpt-4",
		Messages: []Message{{Role: "user", Content: "Hello"}},
	})
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
}

func TestOpenAIComplete_TokenCounting(t *testing.T) {
	resp := chatCompletionResponse{
		ID:      "chatcmpl-tokens",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "gpt-4",
		Choices: []chatCompletionChoice{
			{
				Index:        0,
				Message:      chatCompletionMessage{Role: "assistant", Content: "counted"},
				FinishReason: "stop",
			},
		},
		Usage: chatCompletionUsage{
			PromptTokens:     42,
			CompletionTokens: 17,
			TotalTokens:      59,
		},
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

	result, err := provider.Complete(context.Background(), &Request{
		Model:    "gpt-4",
		Messages: []Message{{Role: "user", Content: "Count these tokens"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if result.InputToks != 42 {
		t.Errorf("InputToks = %d, want 42", result.InputToks)
	}
	if result.OutputToks != 17 {
		t.Errorf("OutputToks = %d, want 17", result.OutputToks)
	}
}

func TestOpenAIComplete_TemperaturePassthrough(t *testing.T) {
	resp := chatCompletionResponse{
		ID:      "chatcmpl-temp",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "gpt-4",
		Choices: []chatCompletionChoice{
			{
				Index:        0,
				Message:      chatCompletionMessage{Role: "assistant", Content: "warm"},
				FinishReason: "stop",
			},
		},
		Usage: chatCompletionUsage{
			PromptTokens:     5,
			CompletionTokens: 1,
			TotalTokens:      6,
		},
	}

	capture := &requestCapture{}
	srv := newCaptureServer(t, capture, resp)
	defer srv.Close()

	provider, err := NewProvider("openai",
		WithAPIKey("test-key"),
		WithBaseURL(srv.URL),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	_, err = provider.Complete(context.Background(), &Request{
		Model:       "gpt-4",
		Messages:    []Message{{Role: "user", Content: "test"}},
		Temperature: 0.7,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	temp, ok := capture.Body["temperature"]
	if !ok {
		t.Fatal("temperature not found in request body")
	}
	tempFloat, ok := temp.(float64)
	if !ok {
		t.Fatalf("temperature is %T, want float64", temp)
	}
	if tempFloat != 0.7 {
		t.Errorf("temperature = %v, want 0.7", tempFloat)
	}
}

func TestOpenAIComplete_TemperatureZeroOmitted(t *testing.T) {
	resp := chatCompletionResponse{
		ID:      "chatcmpl-notemp",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "gpt-4",
		Choices: []chatCompletionChoice{
			{
				Index:        0,
				Message:      chatCompletionMessage{Role: "assistant", Content: "cold"},
				FinishReason: "stop",
			},
		},
		Usage: chatCompletionUsage{
			PromptTokens:     5,
			CompletionTokens: 1,
			TotalTokens:      6,
		},
	}

	capture := &requestCapture{}
	srv := newCaptureServer(t, capture, resp)
	defer srv.Close()

	provider, err := NewProvider("openai",
		WithAPIKey("test-key"),
		WithBaseURL(srv.URL),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	_, err = provider.Complete(context.Background(), &Request{
		Model:       "gpt-4",
		Messages:    []Message{{Role: "user", Content: "test"}},
		Temperature: 0, // zero value, should NOT be sent
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if _, ok := capture.Body["temperature"]; ok {
		t.Error("temperature should be omitted when zero, but was present in request")
	}
}

func TestOpenAIComplete_SystemAndMultipleMessages(t *testing.T) {
	resp := chatCompletionResponse{
		ID:      "chatcmpl-multi",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "gpt-4",
		Choices: []chatCompletionChoice{
			{
				Index:        0,
				Message:      chatCompletionMessage{Role: "assistant", Content: "response"},
				FinishReason: "stop",
			},
		},
		Usage: chatCompletionUsage{
			PromptTokens:     20,
			CompletionTokens: 3,
			TotalTokens:      23,
		},
	}

	capture := &requestCapture{}
	srv := newCaptureServer(t, capture, resp)
	defer srv.Close()

	provider, err := NewProvider("openai",
		WithAPIKey("test-key"),
		WithBaseURL(srv.URL),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	_, err = provider.Complete(context.Background(), &Request{
		Model:  "gpt-4",
		System: "You are a test assistant.",
		Messages: []Message{
			{Role: "user", Content: "First message"},
			{Role: "assistant", Content: "First reply"},
			{Role: "user", Content: "Second message"},
		},
		MaxTokens: 200,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Verify messages include system + 3 conversation messages = 4 total.
	msgs, ok := capture.Body["messages"].([]any)
	if !ok {
		t.Fatal("messages not found or not an array in request")
	}
	if len(msgs) != 4 {
		t.Errorf("len(messages) = %d, want 4 (system + 3 conversation)", len(msgs))
	}

	// System message should be first.
	firstMsg, ok := msgs[0].(map[string]any)
	if !ok {
		t.Fatal("first message is not a map")
	}
	if firstMsg["role"] != "system" {
		t.Errorf("first message role = %q, want %q", firstMsg["role"], "system")
	}

	// Verify max_tokens was passed.
	maxTokens, ok := capture.Body["max_tokens"]
	if !ok {
		t.Fatal("max_tokens not found in request body")
	}
	if maxTokens.(float64) != 200 {
		t.Errorf("max_tokens = %v, want 200", maxTokens)
	}
}

func TestOpenAIComplete_NoSystemMessage(t *testing.T) {
	resp := chatCompletionResponse{
		ID:      "chatcmpl-nosys",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "gpt-4",
		Choices: []chatCompletionChoice{
			{
				Index:        0,
				Message:      chatCompletionMessage{Role: "assistant", Content: "ok"},
				FinishReason: "stop",
			},
		},
		Usage: chatCompletionUsage{PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4},
	}

	capture := &requestCapture{}
	srv := newCaptureServer(t, capture, resp)
	defer srv.Close()

	provider, err := NewProvider("openai",
		WithAPIKey("test-key"),
		WithBaseURL(srv.URL),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	_, err = provider.Complete(context.Background(), &Request{
		Model:    "gpt-4",
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Without a system message, there should be only the user message.
	msgs, ok := capture.Body["messages"].([]any)
	if !ok {
		t.Fatal("messages not found or not an array in request")
	}
	if len(msgs) != 1 {
		t.Errorf("len(messages) = %d, want 1 (no system message)", len(msgs))
	}
}

func TestOpenAIProviderName(t *testing.T) {
	provider, err := NewProvider("openai", WithAPIKey("test-key"))
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if provider.Name() != "openai" {
		t.Errorf("Name() = %q, want %q", provider.Name(), "openai")
	}
}
