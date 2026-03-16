//go:build integration

package llm

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestOpenAIIntegration_DeepInfra tests the OpenAI provider against DeepInfra's
// OpenAI-compatible endpoint. Run with:
//
//	doppler run -- go test -tags integration -run TestOpenAI -v ./internal/llm/ -timeout 60s
func TestOpenAIIntegration_DeepInfra(t *testing.T) {
	apiKey := os.Getenv("DEEPINFRA_API_KEY")
	if apiKey == "" {
		t.Skip("DEEPINFRA_API_KEY not set")
	}

	provider, err := NewProvider("openai",
		WithAPIKey(apiKey),
		WithBaseURL("https://api.deepinfra.com/v1/openai"),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	if provider.Name() != "openai" {
		t.Errorf("Name() = %q, want %q", provider.Name(), "openai")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := provider.Complete(ctx, &Request{
		Model:       "meta-llama/Meta-Llama-3.1-8B-Instruct",
		System:      "You are a helpful assistant. Reply in one short sentence.",
		Messages:    []Message{{Role: "user", Content: "What is 2+2?"}},
		MaxTokens:   64,
		Temperature: 0.1,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if result.Content == "" {
		t.Error("Content is empty")
	}
	t.Logf("Content: %s", result.Content)

	if result.InputToks <= 0 {
		t.Errorf("InputToks = %d, want > 0", result.InputToks)
	}
	t.Logf("InputToks: %d", result.InputToks)

	if result.OutputToks <= 0 {
		t.Errorf("OutputToks = %d, want > 0", result.OutputToks)
	}
	t.Logf("OutputToks: %d", result.OutputToks)
}

// TestOpenAIIntegration_DeepInfra_MultiTurn tests a multi-turn conversation.
func TestOpenAIIntegration_DeepInfra_MultiTurn(t *testing.T) {
	apiKey := os.Getenv("DEEPINFRA_API_KEY")
	if apiKey == "" {
		t.Skip("DEEPINFRA_API_KEY not set")
	}

	provider, err := NewProvider("openai",
		WithAPIKey(apiKey),
		WithBaseURL("https://api.deepinfra.com/v1/openai"),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := provider.Complete(ctx, &Request{
		Model:  "meta-llama/Meta-Llama-3.1-8B-Instruct",
		System: "You are a math tutor. Be concise.",
		Messages: []Message{
			{Role: "user", Content: "What is 3 * 4?"},
			{Role: "assistant", Content: "3 * 4 = 12"},
			{Role: "user", Content: "Now add 5 to that."},
		},
		MaxTokens:   64,
		Temperature: 0.1,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if result.Content == "" {
		t.Error("Content is empty for multi-turn conversation")
	}
	t.Logf("Multi-turn content: %s", result.Content)

	if result.InputToks <= 0 {
		t.Errorf("InputToks = %d, want > 0", result.InputToks)
	}
	if result.OutputToks <= 0 {
		t.Errorf("OutputToks = %d, want > 0", result.OutputToks)
	}
}
