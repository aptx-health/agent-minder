package opencode

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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

	// Close the loop through ParseResult against the real run log.
	logPath := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(logPath, log.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := rt.ParseResult(logPath)
	if err != nil {
		t.Fatalf("ParseResult err: %v", err)
	}
	if res == nil {
		t.Fatal("ParseResult returned nil for a completed run")
	}
	if res.SessionID == "" {
		t.Errorf("ParseResult: empty SessionID")
	}
	if res.IsError {
		t.Errorf("ParseResult: IsError=true, StopReason=%q", res.StopReason)
	}
	if strings.TrimSpace(res.FinalText) == "" {
		t.Errorf("ParseResult: empty FinalText")
	}
	t.Logf("run: exit=%d steps=%d toolStarts=%d toolEnds=%d usageLimits=%d logBytes=%d",
		exit, sink.steps, sink.toolStarts, sink.toolEnds, sink.caps, log.Len())
	t.Logf("parsed: session=%s turns=%d costUSD=%.6f finalText=%q",
		res.SessionID, res.NumTurns, res.TotalCostUSD, truncate(res.FinalText, 60))

	// Exercise the real Resume path: continue the same session and confirm it
	// produces a fresh result record that ParseResult recovers.
	var rlog bytes.Buffer
	rexit, rerr := rt.Resume(ctx, res.SessionID, &recordingSink{}, &rlog)
	if rerr != nil {
		t.Fatalf("Resume error: %v", rerr)
	}
	rpath := filepath.Join(t.TempDir(), "resume.log")
	if err := os.WriteFile(rpath, rlog.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	rres, err := rt.ParseResult(rpath)
	if err != nil || rres == nil {
		t.Fatalf("Resume ParseResult: res=%v err=%v", rres, err)
	}
	if rres.SessionID != res.SessionID {
		t.Errorf("Resume session = %s, want same session %s", rres.SessionID, res.SessionID)
	}
	t.Logf("resume: exit=%d finalText=%q", rexit, truncate(rres.FinalText, 60))
}

// TestLiveToolUse verifies headless permission autonomy end-to-end: it drives a
// prompt that must invoke a tool (write a file) through minder's OWN managed
// server — New() with no BaseURL, so server.ensure() injects the
// defaultHeadlessPermission policy — and asserts the run completes without
// stalling on an approval gate. If permissions were left at opencode's
// interactive "ask", Session.Prompt would hang until the context deadline and
// this test would fail on timeout.
//
// Opt-in and model-dependent (the model must actually emit a tool call):
//
//	OPENCODE_LIVE=1
//	OPENCODE_DIR    working directory holding provider config (also the write target)
//	OPENCODE_MODEL  provider/model reference
func TestLiveToolUse(t *testing.T) {
	if os.Getenv("OPENCODE_LIVE") != "1" {
		t.Skip("set OPENCODE_LIVE=1 to run the live opencode tool-use test")
	}
	dir := os.Getenv("OPENCODE_DIR")
	model := os.Getenv("OPENCODE_MODEL")
	if dir == "" || model == "" {
		t.Fatal("OPENCODE_DIR and OPENCODE_MODEL are required")
	}

	fileName := "minder-tooluse.txt"
	target := filepath.Join(dir, fileName)
	_ = os.Remove(target)
	t.Cleanup(func() { _ = os.Remove(target) })

	// New() (no BaseURL) → the shared manager starts a server and injects the
	// headless permission policy; this is the path we want to exercise.
	rt := New()
	sink := &recordingSink{}
	var log bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	prompt := "Use your file-writing tool to create a file named " + fileName +
		" in the current directory containing exactly the text OK. Do not ask for confirmation."

	exit, err := rt.Run(ctx, runtime.Invocation{
		Workspace: runtime.Workspace{Dir: dir},
		AgentName: "build",
		Model:     model,
		Prompt:    prompt,
	}, sink, &log)
	if err != nil {
		t.Fatalf("Run error (a stall on approval surfaces as a deadline here): %v", err)
	}
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d; log:\n%s", exit, log.String())
	}
	if p := os.Getenv("OPENCODE_LIVE_LOG"); p != "" {
		_ = os.WriteFile(p, log.Bytes(), 0o644)
		t.Logf("wrote mirrored event log to %s", p)
	}

	// Both signals below are best-effort (model- and translator-dependent). The
	// HARD guarantee this test enforces is "no approval stall": Run returned nil
	// with exit 0 above; a gated "ask" with no responder would instead hang until
	// the context deadline and fail before reaching here.
	if _, statErr := os.Stat(target); statErr != nil {
		t.Logf("tool-written file not found (%v); model likely did not call the write tool", statErr)
	} else {
		t.Logf("tool wrote %s successfully — permissions did not stall", target)
	}
	t.Logf("run: exit=%d toolStarts=%d logBytes=%d", exit, sink.toolStarts, log.Len())
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
