package claudecli

import (
	"context"
	"encoding/json"
	"testing"
)

func TestParseOutput_ValidJSON(t *testing.T) {
	raw := `{"result":"Hello world","structured_output":null,"is_error":false,"total_cost_usd":0.005,"num_turns":1,"session_id":"sess-123","usage":{"input_tokens":100,"output_tokens":50}}`
	resp, err := parseOutput([]byte(raw))
	if err != nil {
		t.Fatalf("parseOutput: %v", err)
	}
	if resp.Result != "Hello world" {
		t.Errorf("Result = %q, want %q", resp.Result, "Hello world")
	}
	if resp.CostUSD != 0.005 {
		t.Errorf("CostUSD = %f, want 0.005", resp.CostUSD)
	}
	if resp.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", resp.InputTokens)
	}
	if resp.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", resp.OutputTokens)
	}
	if resp.SessionID != "sess-123" {
		t.Errorf("SessionID = %q, want %q", resp.SessionID, "sess-123")
	}
	if resp.IsError {
		t.Error("IsError should be false")
	}
}

func TestParseOutput_StructuredOutput(t *testing.T) {
	structured := `{"analysis":"all good","concerns":[]}`
	raw := `{"result":"","structured_output":` + structured + `,"is_error":false,"total_cost_usd":0.01,"num_turns":1,"session_id":"s2","usage":{"input_tokens":200,"output_tokens":100}}`
	resp, err := parseOutput([]byte(raw))
	if err != nil {
		t.Fatalf("parseOutput: %v", err)
	}
	if resp.Result != "" {
		t.Errorf("Result should be empty, got %q", resp.Result)
	}
	if len(resp.StructuredOutput) == 0 {
		t.Fatal("StructuredOutput should not be empty")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(resp.StructuredOutput, &parsed); err != nil {
		t.Fatalf("unmarshal structured: %v", err)
	}
	if parsed["analysis"] != "all good" {
		t.Errorf("analysis = %v", parsed["analysis"])
	}
}

func TestParseOutput_InvalidJSON(t *testing.T) {
	_, err := parseOutput([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestResponse_Content_PreferStructured(t *testing.T) {
	resp := &Response{
		Result:           "fallback",
		StructuredOutput: json.RawMessage(`{"key":"value"}`),
	}
	if resp.Content() != `{"key":"value"}` {
		t.Errorf("Content() = %q, want structured output", resp.Content())
	}
}

func TestResponse_Content_FallbackToResult(t *testing.T) {
	resp := &Response{
		Result:           "hello",
		StructuredOutput: nil,
	}
	if resp.Content() != "hello" {
		t.Errorf("Content() = %q, want %q", resp.Content(), "hello")
	}
}

func TestResponse_Content_NullStructured(t *testing.T) {
	resp := &Response{
		Result:           "hello",
		StructuredOutput: json.RawMessage("null"),
	}
	if resp.Content() != "hello" {
		t.Errorf("Content() = %q, want %q", resp.Content(), "hello")
	}
}

// mockCompleter is a test helper implementing Completer.
type mockCompleter struct {
	response *Response
	err      error
	lastReq  *Request
}

func (m *mockCompleter) Complete(_ context.Context, req *Request) (*Response, error) {
	m.lastReq = req
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func TestMockCompleter(t *testing.T) {
	mock := &mockCompleter{
		response: &Response{Result: "test response"},
	}
	resp, err := mock.Complete(context.Background(), &Request{
		Prompt: "hello",
		Model:  "haiku",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Result != "test response" {
		t.Errorf("Result = %q", resp.Result)
	}
	if mock.lastReq.Model != "haiku" {
		t.Errorf("lastReq.Model = %q", mock.lastReq.Model)
	}
}
