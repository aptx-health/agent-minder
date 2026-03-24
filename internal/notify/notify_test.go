package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestNewReturnsNilWithoutURL(t *testing.T) {
	n := New(Config{})
	if n != nil {
		t.Fatal("expected nil notifier when URL is empty")
	}
}

func TestNotifyNilSafe(t *testing.T) {
	// Calling methods on nil notifier should not panic.
	var n *Notifier
	n.Notify(Event{Type: EventTaskBailed})
	n.Flush()
	n.Close()
}

func TestSingleEventSlackPayload(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(Config{
		WebhookURL: srv.URL,
		Format:     "slack",
		BatchDelay: 50 * time.Millisecond,
	})

	n.Notify(Event{
		Type:        EventTaskBailed,
		Project:     "test-project",
		Summary:     "Agent bailed on #42",
		IssueNumber: 42,
		IssueTitle:  "Fix the widget",
		Timestamp:   time.Now(),
	})

	// Wait for batch to flush.
	time.Sleep(200 * time.Millisecond)

	if len(received) == 0 {
		t.Fatal("expected webhook to be called")
	}

	var payload slackPayload
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if payload.Text == "" {
		t.Error("expected non-empty text field")
	}
	if len(payload.Blocks) == 0 {
		t.Error("expected at least one block")
	}
}

func TestBatchedEvents(t *testing.T) {
	var mu sync.Mutex
	var calls int
	var lastBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		lastBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(Config{
		WebhookURL: srv.URL,
		Format:     "slack",
		BatchDelay: 100 * time.Millisecond,
	})

	// Send 3 events in rapid succession.
	for i := 0; i < 3; i++ {
		n.Notify(Event{
			Type:        EventTaskCompleted,
			Project:     "test",
			Summary:     "done",
			IssueNumber: i + 1,
			Timestamp:   time.Now(),
		})
	}

	// Wait for batch to flush.
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if calls != 1 {
		t.Fatalf("expected 1 batched call, got %d", calls)
	}

	var payload slackPayload
	if err := json.Unmarshal(lastBody, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Batched payload should mention count.
	if payload.Text == "" {
		t.Error("expected non-empty text")
	}
}

func TestGenericFormat(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(Config{
		WebhookURL: srv.URL,
		Format:     "generic",
		BatchDelay: 50 * time.Millisecond,
	})

	n.Notify(Event{
		Type:        EventTaskStarted,
		Project:     "test",
		Summary:     "started",
		IssueNumber: 1,
		Timestamp:   time.Now(),
	})

	time.Sleep(200 * time.Millisecond)

	var payload genericPayload
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(payload.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(payload.Events))
	}
	if payload.Events[0].Type != EventTaskStarted {
		t.Errorf("expected task.started, got %s", payload.Events[0].Type)
	}
}

func TestEventFilter(t *testing.T) {
	var mu sync.Mutex
	var calls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Only subscribe to bailed events.
	n := New(Config{
		WebhookURL: srv.URL,
		Format:     "slack",
		Events:     []EventType{EventTaskBailed},
		BatchDelay: 50 * time.Millisecond,
	})

	// Send a non-matching event.
	n.Notify(Event{Type: EventTaskStarted, Project: "test", Summary: "started"})
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	c := calls
	mu.Unlock()
	if c != 0 {
		t.Fatalf("expected 0 calls for filtered event, got %d", c)
	}

	// Send a matching event.
	n.Notify(Event{Type: EventTaskBailed, Project: "test", Summary: "bailed"})
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	c = calls
	mu.Unlock()
	if c != 1 {
		t.Fatalf("expected 1 call for matching event, got %d", c)
	}
}

func TestMapEventType(t *testing.T) {
	tests := []struct {
		input    string
		expected EventType
	}{
		{"started", EventTaskStarted},
		{"completed", EventTaskCompleted},
		{"bailed", EventTaskBailed},
		{"failed", EventTaskFailed},
		{"stopped", EventTaskStopped},
		{"finished", EventFinished},
		{"discovered", EventDiscovered},
		{"error", EventAgentError},
		{"info", ""},
		{"warning", ""},
		{"graph-update", ""},
	}

	for _, tt := range tests {
		got := MapEventType(tt.input)
		if got != tt.expected {
			t.Errorf("MapEventType(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestParseEventTypes(t *testing.T) {
	result := ParseEventTypes("")
	if result != nil {
		t.Error("expected nil for empty string")
	}

	result = ParseEventTypes("task.bailed, task.failed")
	if len(result) != 2 {
		t.Fatalf("expected 2 events, got %d", len(result))
	}
	if result[0] != EventTaskBailed {
		t.Errorf("expected task.bailed, got %s", result[0])
	}
	if result[1] != EventTaskFailed {
		t.Errorf("expected task.failed, got %s", result[1])
	}
}

func TestDiscordFormat(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	n := New(Config{
		WebhookURL: srv.URL,
		Format:     "discord",
		BatchDelay: 50 * time.Millisecond,
	})

	n.Notify(Event{
		Type:        EventTaskBailed,
		Project:     "test-project",
		Summary:     "Agent bailed on #42",
		IssueNumber: 42,
		IssueTitle:  "Fix the widget",
		Timestamp:   time.Now(),
	})

	time.Sleep(200 * time.Millisecond)

	if len(received) == 0 {
		t.Fatal("expected webhook to be called")
	}

	var payload discordWebhookPayload
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(payload.Embeds) != 1 {
		t.Fatalf("expected 1 embed, got %d", len(payload.Embeds))
	}
	if payload.Embeds[0].Color != 0xe67e22 { // orange for bailed
		t.Errorf("expected orange color for bailed, got %d", payload.Embeds[0].Color)
	}
}

func TestDiscordEventColors(t *testing.T) {
	tests := []struct {
		typ   EventType
		color int
	}{
		{EventTaskStarted, 0x3498db},
		{EventTaskCompleted, 0x2ecc71},
		{EventTaskBailed, 0xe67e22},
		{EventTaskFailed, 0xe74c3c},
		{EventBudgetLimit, 0xf1c40f},
	}
	for _, tt := range tests {
		got := discordEventColor(tt.typ)
		if got != tt.color {
			t.Errorf("discordEventColor(%s) = %d, want %d", tt.typ, got, tt.color)
		}
	}
}

func TestCloseFlushes(t *testing.T) {
	var mu sync.Mutex
	var calls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(Config{
		WebhookURL: srv.URL,
		Format:     "slack",
		BatchDelay: 10 * time.Second, // Very long — should not wait this long.
	})

	n.Notify(Event{Type: EventTaskBailed, Project: "test", Summary: "bailed"})
	n.Close()

	// Give a moment for the HTTP call to complete.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	c := calls
	mu.Unlock()
	if c != 1 {
		t.Fatalf("expected Close() to flush, got %d calls", c)
	}
}
