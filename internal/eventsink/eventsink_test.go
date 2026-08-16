package eventsink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/eventbus"
	"github.com/aptx-health/agent-minder/internal/supervisor"
)

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"webhook only", Config{Events: []string{"completed"}, Webhook: "https://example.com"}, false},
		{"exec only", Config{Events: []string{"completed"}, Exec: "./notify.sh"}, false},
		{"both", Config{Events: []string{"completed"}, Webhook: "https://example.com", Exec: "./notify.sh"}, true},
		{"neither", Config{Events: []string{"completed"}}, true},
		{"no events", Config{Webhook: "https://example.com"}, true},
		{"bad pattern", Config{Events: []string{"["}, Webhook: "https://example.com"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestConfigMatches(t *testing.T) {
	cfg := Config{Events: []string{"completed", "review*"}}
	if !cfg.Matches("completed") {
		t.Error("expected exact match")
	}
	if !cfg.Matches("reviewed") {
		t.Error("expected glob match")
	}
	if cfg.Matches("bailed") {
		t.Error("expected no match")
	}
}

func TestManagerDeliversToWebhookAndExec(t *testing.T) {
	var got atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Payload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Errorf("decode webhook payload: %v", err)
		}
		if p.Type != string(supervisor.EventCompleted) {
			t.Errorf("webhook got type %q, want %q", p.Type, supervisor.EventCompleted)
		}
		got.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	execOut := filepath.Join(t.TempDir(), "out.json")
	script := "cat > " + execOut

	bus := eventbus.New[supervisor.Envelope](8)
	mgr, err := NewManager(bus, []Config{
		{Events: []string{"completed"}, Webhook: srv.URL},
		{Events: []string{"completed"}, Exec: script},
	}, Options{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Stop()

	if _, err := bus.Publish(supervisor.Envelope{Type: supervisor.EventCompleted, Summary: "done"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// Non-matching event: must not reach either sink.
	if _, err := bus.Publish(supervisor.Envelope{Type: supervisor.EventInfo, Summary: "noise"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool { return got.Load() == 1 })

	// The shell truncates/creates execOut via redirection before cat writes
	// its content, so a bare os.Stat can observe the file mid-write (or just
	// after creation, still empty). Poll until it holds valid JSON instead.
	var p Payload
	waitFor(t, 2*time.Second, func() bool {
		data, err := os.ReadFile(execOut)
		if err != nil {
			return false
		}
		return json.Unmarshal(data, &p) == nil
	})
	if p.Type != string(supervisor.EventCompleted) {
		t.Errorf("exec got type %q, want %q", p.Type, supervisor.EventCompleted)
	}

	waitFor(t, 2*time.Second, func() bool {
		for _, s := range mgr.Stats() {
			if s.Delivered != 1 {
				return false
			}
		}
		return true
	})
	if got.Load() != 1 {
		t.Errorf("webhook called %d times, want 1 (non-matching event must not fire)", got.Load())
	}
}

func TestManagerHangingSinkDropsRatherThanBlocks(t *testing.T) {
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // never returns during the test
		w.WriteHeader(http.StatusOK)
	}))
	defer hang.Close()
	defer close(release)

	bus := eventbus.New[supervisor.Envelope](64)
	mgr, err := NewManager(bus, []Config{
		{Events: []string{"*"}, Webhook: hang.URL},
	}, Options{QueueSize: 2, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Stop()

	// Publish far more than the queue can hold. This must return immediately
	// regardless of the sink hanging.
	publishDone := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			_, _ = bus.Publish(supervisor.Envelope{Type: supervisor.EventInfo, Summary: "spam"})
		}
		close(publishDone)
	}()

	select {
	case <-publishDone:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked on a hanging sink")
	}

	waitFor(t, 2*time.Second, func() bool {
		for _, s := range mgr.Stats() {
			if s.Dropped > 0 {
				return true
			}
		}
		return false
	})
}

func TestManagerRejectsInvalidSinkConfig(t *testing.T) {
	bus := eventbus.New[supervisor.Envelope](8)
	_, err := NewManager(bus, []Config{{Events: []string{"completed"}}}, Options{})
	if err == nil {
		t.Fatal("expected error for sink with neither webhook nor exec")
	}
}

func TestExecDeliverPassesPayloadOnStdin(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat not available")
	}
	deliver := execDeliver("cat > /dev/null")
	if err := deliver(context.Background(), []byte(`{"type":"completed"}`)); err != nil {
		t.Fatalf("execDeliver: %v", err)
	}
}
