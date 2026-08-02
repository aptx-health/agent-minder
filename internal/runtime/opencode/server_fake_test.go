package opencode

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

// fakeOpencodeServer stands in for `opencode serve`: it implements the three
// endpoints the runtime drives — POST /session (create), POST
// /session/{id}/message (prompt), GET /event (SSE) — with no real model. It lets
// Run be exercised end-to-end deterministically via the BaseURL override, so the
// result path (log record → ParseResult → ClassifyOutcome) is covered without a
// live server or model.
func fakeOpencodeServer(t *testing.T, promptBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"ses_fake","title":"agent-minder","version":"1"}`))

		case r.Method == http.MethodGet && r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			// Best-effort progress frame; the deterministic assertions come from
			// the prompt response, not from SSE timing.
			_, _ = w.Write([]byte(`data: {"type":"message.part.updated","properties":{"sessionID":"ses_fake","part":{"type":"step-start"}}}` + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
			<-r.Context().Done() // hold open until Run cancels the stream

		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/session/") && strings.HasSuffix(r.URL.Path, "/message"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(promptBody))

		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunAgainstFakeServer(t *testing.T) {
	// A successful prompt: real USD cost, one step-start (→ NumTurns=1), a text
	// part (→ FinalText), and a reasoning part that must be excluded.
	promptBody := `{
		"info": {"id":"msg_1","role":"assistant","sessionID":"ses_fake","cost":0.0123,
		         "tokens":{"input":10,"output":5,"reasoning":0},"error":{}},
		"parts": [
			{"type":"step-start"},
			{"type":"reasoning","text":"thinking hard"},
			{"type":"text","text":"hello from fake"}
		]
	}`
	srv := fakeOpencodeServer(t, promptBody)

	rt := &OpencodeRuntime{BaseURL: srv.URL}
	sink := &recordingSink{}
	var log bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	exit, err := rt.Run(ctx, runtime.Invocation{
		Workspace: runtime.Workspace{Dir: "/work"},
		AgentName: "autopilot",
		Model:     "localmlx/qwen",
		Prompt:    "do it",
	}, sink, &log)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0; log:\n%s", exit, log.String())
	}
	if !strings.Contains(log.String(), resultMarker) {
		t.Fatalf("log missing result marker:\n%s", log.String())
	}

	// Close the loop through ParseResult against the fake run log.
	logPath := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(logPath, log.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := rt.ParseResult(logPath)
	if err != nil || res == nil {
		t.Fatalf("ParseResult: res=%v err=%v", res, err)
	}
	if res.SessionID != "ses_fake" {
		t.Errorf("SessionID = %q, want ses_fake", res.SessionID)
	}
	if res.TotalCostUSD != 0.0123 {
		t.Errorf("TotalCostUSD = %v, want 0.0123", res.TotalCostUSD)
	}
	if res.FinalText != "hello from fake" {
		t.Errorf("FinalText = %q, want 'hello from fake' (reasoning excluded)", res.FinalText)
	}
	if res.NumTurns != 1 {
		t.Errorf("NumTurns = %d, want 1", res.NumTurns)
	}
	if res.IsError {
		t.Errorf("IsError = true, want false; StopReason=%q", res.StopReason)
	}

	// ClassifyOutcome: within budget/turns → no failure.
	if oc := rt.ClassifyOutcome(res, runtime.Limits{MaxTurns: 50, MaxBudgetUSD: 5}); oc.Status != "" {
		t.Errorf("ClassifyOutcome = %+v, want empty (success)", oc)
	}
	// Budget breach is detected from the same cost.
	if oc := rt.ClassifyOutcome(res, runtime.Limits{MaxBudgetUSD: 0.01}); oc.FailureReason != "max_budget" {
		t.Errorf("ClassifyOutcome budget = %+v, want max_budget", oc)
	}
}

func TestRunAgainstFakeServerError(t *testing.T) {
	// An assistant message carrying an error → exit 1 and IsError.
	promptBody := `{
		"info": {"id":"msg_1","role":"assistant","sessionID":"ses_fake","cost":0,
		         "tokens":{},"error":{"name":"ProviderError","data":{"message":"boom"}}},
		"parts": []
	}`
	srv := fakeOpencodeServer(t, promptBody)

	rt := &OpencodeRuntime{BaseURL: srv.URL}
	var log bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	exit, err := rt.Run(ctx, runtime.Invocation{
		Workspace: runtime.Workspace{Dir: "/work"},
		AgentName: "autopilot",
		Prompt:    "do it",
	}, &recordingSink{}, &log)
	if err != nil {
		t.Fatalf("Run returned transport error: %v", err)
	}
	if exit != 1 {
		t.Fatalf("exit = %d, want 1 for errored assistant message", exit)
	}

	logPath := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(logPath, log.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := rt.ParseResult(logPath)
	if err != nil || res == nil {
		t.Fatalf("ParseResult: res=%v err=%v", res, err)
	}
	if !res.IsError {
		t.Errorf("IsError = false, want true")
	}
	if !strings.Contains(res.StopReason, "ProviderError") || !strings.Contains(res.StopReason, "boom") {
		t.Errorf("StopReason = %q, want it to mention ProviderError/boom", res.StopReason)
	}
}
