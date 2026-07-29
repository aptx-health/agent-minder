package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

// writeLog builds a run log from mirrored event lines plus one or more result
// records, mirroring what Run/streamEvents produce, and returns its path.
func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.log")
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func recordLine(t *testing.T, exitCode int, info string, parts string) string {
	t.Helper()
	rec := logRecord{
		Marker:    resultMarker,
		SessionID: "ses_abc",
		ExitCode:  exitCode,
		Info:      json.RawMessage(info),
		Parts:     json.RawMessage(parts),
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseResultSuccess(t *testing.T) {
	info := `{"cost":0.0123,"tokens":{"total":100,"input":80,"output":20,"reasoning":0},"error":{"name":"","data":{}}}`
	parts := `[{"type":"step-start"},{"type":"reasoning","text":"let me think"},{"type":"text","text":"the "},{"type":"text","text":"answer"},{"type":"step-finish"}]`
	log := writeLog(t,
		`{"type":"message.part.updated","properties":{"sessionID":"ses_abc"}}`, // mirrored event, must be ignored
		recordLine(t, 0, info, parts),
	)

	r, err := New().ParseResult(log)
	if err != nil {
		t.Fatalf("ParseResult err: %v", err)
	}
	if r == nil {
		t.Fatal("expected a result")
	}
	if r.SessionID != "ses_abc" {
		t.Errorf("SessionID = %q, want ses_abc", r.SessionID)
	}
	if r.TotalCostUSD != 0.0123 {
		t.Errorf("TotalCostUSD = %v, want 0.0123", r.TotalCostUSD)
	}
	if r.NumTurns != 1 {
		t.Errorf("NumTurns = %d, want 1 (one step-start)", r.NumTurns)
	}
	if r.FinalText != "the answer" {
		t.Errorf("FinalText = %q, want %q (text parts joined, reasoning excluded)", r.FinalText, "the answer")
	}
	if r.IsError {
		t.Errorf("IsError = true, want false")
	}
}

func TestParseResultError(t *testing.T) {
	info := `{"cost":0.5,"tokens":{"total":10},"error":{"name":"APIError","data":{"message":"rate limit exceeded"}}}`
	parts := `[{"type":"step-start"}]`
	log := writeLog(t, recordLine(t, 1, info, parts))

	r, err := New().ParseResult(log)
	if err != nil {
		t.Fatalf("ParseResult err: %v", err)
	}
	if !r.IsError {
		t.Errorf("IsError = false, want true")
	}
	if r.StopReason != "APIError: rate limit exceeded" {
		t.Errorf("StopReason = %q, want 'APIError: rate limit exceeded'", r.StopReason)
	}
}

func TestParseResultLastRecordWins(t *testing.T) {
	// A Resume appends a second record; ParseResult must return the latest.
	first := recordLine(t, 1, `{"cost":0.1,"error":{"name":"APIError","data":{"message":"rate limit"}}}`, `[]`)
	second := recordLine(t, 0, `{"cost":0.2,"tokens":{"total":5},"error":{"name":"","data":{}}}`, `[{"type":"text","text":"done"}]`)
	r, err := New().ParseResult(writeLog(t, first, second))
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError || r.TotalCostUSD != 0.2 || r.FinalText != "done" {
		t.Errorf("last record not chosen: %+v", r)
	}
}

func TestClassifyOutcome(t *testing.T) {
	rt := New()
	cases := []struct {
		name   string
		r      *runtime.Result
		limits runtime.Limits
		status string
		reason string
	}{
		{"nil result", nil, runtime.Limits{}, "", ""},
		{"clean", &runtime.Result{NumTurns: 2, TotalCostUSD: 0.1}, runtime.Limits{MaxTurns: 10, MaxBudgetUSD: 1}, "", ""},
		{"max turns", &runtime.Result{NumTurns: 10}, runtime.Limits{MaxTurns: 10}, "failed", "max_turns"},
		{"max budget", &runtime.Result{TotalCostUSD: 0.96}, runtime.Limits{MaxBudgetUSD: 1}, "failed", "max_budget"},
		{"plain error", &runtime.Result{IsError: true, StopReason: "boom"}, runtime.Limits{}, "failed", "error"},
		{"usage limit", &runtime.Result{IsError: true, StopReason: "APIError: rate limit exceeded"}, runtime.Limits{}, "failed", "usage_limit"},
	}
	for _, c := range cases {
		got := rt.ClassifyOutcome(c.r, c.limits)
		if got.Status != c.status || got.FailureReason != c.reason {
			t.Errorf("%s: got {%q,%q}, want {%q,%q}", c.name, got.Status, got.FailureReason, c.status, c.reason)
		}
	}
}
