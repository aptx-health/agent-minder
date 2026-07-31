package opencode

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

const sampleBail = `{"reason":"blocked","files_examined":["a.go"],"plan":"need the API key","complexity":"high"}`

func TestExtractBailReportFromText(t *testing.T) {
	o := New()
	r := &runtime.Result{FinalText: "I cannot proceed.\n<bail-report>" + sampleBail + "</bail-report>\n"}
	rep := o.ExtractBailReport(r, "")
	if rep == nil {
		t.Fatal("expected a bail report")
	}
	if rep.Reason != "blocked" || rep.Detail != "need the API key" {
		t.Errorf("got reason=%q detail=%q, want blocked / need the API key", rep.Reason, rep.Detail)
	}
}

func TestExtractBailReportLogFallback(t *testing.T) {
	// No report in FinalText; it landed in the raw log (e.g. piped to a tool).
	p := filepath.Join(t.TempDir(), "agent.log")
	body := `{"type":"message.part.updated","text":"<bail-report>` + sampleBail + `</bail-report>"}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := New().ExtractBailReport(&runtime.Result{FinalText: "done"}, p)
	if rep == nil || rep.Reason != "blocked" {
		t.Fatalf("log fallback failed: %+v", rep)
	}
}

func TestExtractBailReportNone(t *testing.T) {
	o := New()
	if rep := o.ExtractBailReport(&runtime.Result{FinalText: "all good, PR opened"}, ""); rep != nil {
		t.Errorf("expected nil, got %+v", rep)
	}
	if rep := o.ExtractBailReport(nil, ""); rep != nil {
		t.Errorf("expected nil for nil result, got %+v", rep)
	}
}

func TestExtractBailReportPicksLastBlock(t *testing.T) {
	// An earlier quoted mention must not shadow the real final block.
	text := "example: `<bail-report>{}</bail-report>`\n<bail-report>" + sampleBail + "</bail-report>"
	rep := New().ExtractBailReport(&runtime.Result{FinalText: text}, "")
	if rep == nil || rep.Reason != "blocked" {
		t.Fatalf("expected the last valid block, got %+v", rep)
	}
}

func TestResumeEmptySessionID(t *testing.T) {
	_, err := New().Resume(context.Background(), "", nil, nil)
	if err == nil {
		t.Fatal("expected error for empty session id")
	}
}

func TestResumeParams(t *testing.T) {
	p := resumeParams()
	if len(p.Parts.Value) != 1 {
		t.Fatalf("resumeParams Parts len = %d, want 1", len(p.Parts.Value))
	}
	if p.Model.Present {
		t.Error("resume should not pin a model; the session's model is reused")
	}
}
