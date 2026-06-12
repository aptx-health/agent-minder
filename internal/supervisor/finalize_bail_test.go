package supervisor

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/aptx-health/agent-minder/internal/db"
	runtimepkg "github.com/aptx-health/agent-minder/internal/runtime"
)

// classifyingRuntime is a runtime stub used by finalizeBail tests that lets
// the test inject the Outcome returned by ClassifyOutcome.
type classifyingRuntime struct {
	outcome runtimepkg.Outcome
}

func (r *classifyingRuntime) Name() string { return "classifying" }
func (r *classifyingRuntime) PrepareAgentDef(_ context.Context, _ runtimepkg.Workspace, _ runtimepkg.AgentDefinition) error {
	return nil
}
func (r *classifyingRuntime) Run(_ context.Context, _ runtimepkg.Invocation, _ runtimepkg.EventSink, _ io.Writer) (int, error) {
	return 0, nil
}
func (r *classifyingRuntime) Resume(_ context.Context, _ string, _ runtimepkg.EventSink, _ io.Writer) (int, error) {
	return 0, runtimepkg.ErrNotSupported
}
func (r *classifyingRuntime) ParseResult(_ string) (*runtimepkg.Result, error) {
	return &runtimepkg.Result{}, nil
}
func (r *classifyingRuntime) ClassifyOutcome(_ *runtimepkg.Result, _ runtimepkg.Limits) runtimepkg.Outcome {
	return r.outcome
}
func (r *classifyingRuntime) ExtractBailReport(_ *runtimepkg.Result, _ string) *runtimepkg.BailReport {
	return nil
}

// TestFinalizeBail_PreservesBailReportFields is a regression test for #517.
//
// Two writers race to UpdateJobFailure on the bail path:
//  1. handleBailReport writes the structured bail-report data
//     (failure_reason="bailed", failure_detail=<report reason>).
//  2. finalizeBail re-runs ClassifyOutcome and used to unconditionally write
//     the classifier output. For a clean agent exit with no max_turns /
//     max_budget / is_error signal, ClassifyOutcome returns Outcome{} (empty
//     strings), which clobbered the bail-report data with empty strings.
//
// finalizeBail must now skip the DB write when the classifier produced no
// reason, leaving handleBailReport's write authoritative.
func TestFinalizeBail_PreservesBailReportFields(t *testing.T) {
	h := newHarness(t)
	job := testJob(t, h.store, h.deploy)
	// Skip the GH RemoveLabel call inside finalizeBail by zeroing the
	// in-memory IssueNumber pointer (finalizeBail reads sc.Job, not the DB).
	job.IssueNumber = 0

	// Runtime whose ClassifyOutcome returns Outcome{} — the exact regression.
	h.sup.SetRuntime(&classifyingRuntime{})

	sc := h.newSlotContext(job)
	m := NewDefaultJobManager(sc, DefaultAutopilotContract())

	// Step 1: handleBailReport writes failure_reason="bailed",
	// failure_detail="smoke test bail".
	report := &runtimepkg.BailReport{
		Reason: "smoke test bail",
		Detail: "intentional bail per issue instructions",
		Native: json.RawMessage(`{"reason":"smoke test bail","plan":"intentional bail per issue instructions","complexity":"small"}`),
	}
	m.handleBailReport(context.Background(), report)

	mid, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob (mid): %v", err)
	}
	if !mid.FailureReason.Valid || mid.FailureReason.String != "bailed" {
		t.Fatalf("handleBailReport: expected failure_reason=bailed, got %+v", mid.FailureReason)
	}
	if !mid.FailureDetail.Valid || mid.FailureDetail.String != "smoke test bail" {
		t.Fatalf("handleBailReport: expected failure_detail=%q, got %+v", "smoke test bail", mid.FailureDetail)
	}

	// Step 2: finalizeBail must NOT overwrite the bail-report data when the
	// classifier returns an empty outcome.
	if err := m.finalizeBail(context.Background()); err != nil {
		t.Fatalf("finalizeBail: %v", err)
	}

	post, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob (post): %v", err)
	}
	if !post.FailureReason.Valid || post.FailureReason.String != "bailed" {
		t.Errorf("finalizeBail clobbered failure_reason: got %+v, want %q", post.FailureReason, "bailed")
	}
	if !post.FailureDetail.Valid || post.FailureDetail.String != "smoke test bail" {
		t.Errorf("finalizeBail clobbered failure_detail: got %+v, want %q", post.FailureDetail, "smoke test bail")
	}
	if post.Status != db.StatusBailed {
		t.Errorf("finalizeBail must still transition status to bailed; got %q", post.Status)
	}
}

// TestFinalizeBail_WritesClassifierOutcome verifies that finalizeBail still
// writes failure_reason/failure_detail when the classifier produces a
// non-empty outcome (e.g., max_turns exhaustion). This is the non-regression
// path: paths that do NOT go through handleBailReport rely on finalizeBail's
// write for the failure fields.
func TestFinalizeBail_WritesClassifierOutcome(t *testing.T) {
	h := newHarness(t)
	job := testJob(t, h.store, h.deploy)
	job.IssueNumber = 0

	h.sup.SetRuntime(&classifyingRuntime{
		outcome: runtimepkg.Outcome{
			FailureReason: "max_turns",
			FailureDetail: "exceeded 50 turns",
		},
	})

	sc := h.newSlotContext(job)
	m := NewDefaultJobManager(sc, DefaultAutopilotContract())

	if err := m.finalizeBail(context.Background()); err != nil {
		t.Fatalf("finalizeBail: %v", err)
	}

	post, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !post.FailureReason.Valid || post.FailureReason.String != "max_turns" {
		t.Errorf("expected failure_reason=max_turns, got %+v", post.FailureReason)
	}
	if !post.FailureDetail.Valid || post.FailureDetail.String != "exceeded 50 turns" {
		t.Errorf("expected failure_detail=%q, got %+v", "exceeded 50 turns", post.FailureDetail)
	}
}
