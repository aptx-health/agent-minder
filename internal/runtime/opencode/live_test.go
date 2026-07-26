package opencode

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

// recordingSink counts EventSink callbacks for assertions.
type recordingSink struct {
	mu                                sync.Mutex
	steps, toolStarts, toolEnds, caps int
	lastTool                          string
}

func (s *recordingSink) OnAssistantStep() { s.mu.Lock(); s.steps++; s.mu.Unlock() }
func (s *recordingSink) OnToolStart(name, _ string) {
	s.mu.Lock()
	s.toolStarts++
	s.lastTool = name
	s.mu.Unlock()
}
func (s *recordingSink) OnToolEnd()    { s.mu.Lock(); s.toolEnds++; s.mu.Unlock() }
func (s *recordingSink) OnUsageLimit() { s.mu.Lock(); s.caps++; s.mu.Unlock() }

// TestLiveRun drives Run against an already-running `opencode serve`. It is an
// opt-in integration test: it only runs when OPENCODE_LIVE=1. Configure via env:
//
//	OPENCODE_LIVE=1
//	OPENCODE_BASEURL   (default http://127.0.0.1:4096)
//	OPENCODE_DIR       working directory holding provider config (required)
//	OPENCODE_MODEL     provider/model reference (required)
//	OPENCODE_PROMPT    prompt text (default a trivial arithmetic question)
func TestLiveRun(t *testing.T) {
	if os.Getenv("OPENCODE_LIVE") != "1" {
		t.Skip("set OPENCODE_LIVE=1 to run the live opencode integration test")
	}
	baseURL := envOr("OPENCODE_BASEURL", "http://127.0.0.1:4096")
	dir := os.Getenv("OPENCODE_DIR")
	model := os.Getenv("OPENCODE_MODEL")
	if dir == "" || model == "" {
		t.Fatal("OPENCODE_DIR and OPENCODE_MODEL are required")
	}
	prompt := envOr("OPENCODE_PROMPT", "What is 2+2? Answer with just the number.")

	rt := &OpencodeRuntime{BaseURL: baseURL}
	sink := &recordingSink{}
	var log bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	exit, err := rt.Run(ctx, runtime.Invocation{
		Workspace: runtime.Workspace{Dir: dir},
		AgentName: "build",
		Model:     model,
		Prompt:    prompt,
	}, sink, &log)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d; log:\n%s", exit, log.String())
	}
	if !strings.Contains(log.String(), resultMarker) {
		t.Fatalf("log missing result marker; log:\n%s", log.String())
	}
	if sink.steps == 0 {
		t.Errorf("expected at least one assistant step event")
	}
	t.Logf("exit=%d steps=%d toolStarts=%d toolEnds=%d usageLimits=%d logBytes=%d",
		exit, sink.steps, sink.toolStarts, sink.toolEnds, sink.caps, log.Len())
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
