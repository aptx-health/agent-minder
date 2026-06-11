package supervisor

import (
	"context"
	"encoding/json"
	"testing"

	runtimepkg "github.com/aptx-health/agent-minder/internal/runtime"
)

// TestHandleBailReport_DecodesNative covers the supervisor-side seam between
// runtime.ExtractBailReport and the stored BailAssessment shape: a runtime
// BailReport whose Native carries the full <bail-report> JSON should be
// stored verbatim as BailAssessment, including the fields not exposed on
// runtime.BailReport (files_examined, sub_issues, complexity).
func TestHandleBailReport_DecodesNative(t *testing.T) {
	h := newHarness(t)
	job := testJob(t, h.store, h.deploy)
	sc := h.newSlotContext(job)
	m := NewDefaultJobManager(sc, DefaultAutopilotContract())

	native := []byte(`{
		"reason": "Needs design decisions",
		"files_examined": ["a.go", "b.go", "c.go"],
		"plan": "1. Define flow\n2. Update middleware\n3. Add tests",
		"sub_issues": ["sub-A", "sub-B"],
		"complexity": "large"
	}`)
	report := &runtimepkg.BailReport{
		Reason: "Needs design decisions",
		Detail: "1. Define flow\n2. Update middleware\n3. Add tests",
		Native: json.RawMessage(native),
	}

	m.handleBailReport(context.Background(), report)

	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !updated.ResultJSON.Valid || updated.ResultJSON.String == "" {
		t.Fatalf("expected ResultJSON to be populated, got %+v", updated.ResultJSON)
	}

	var decoded BailAssessment
	if err := json.Unmarshal([]byte(updated.ResultJSON.String), &decoded); err != nil {
		t.Fatalf("ResultJSON does not decode as BailAssessment: %v", err)
	}
	if decoded.Reason != "Needs design decisions" {
		t.Errorf("reason = %q", decoded.Reason)
	}
	if decoded.Complexity != "large" {
		t.Errorf("complexity = %q", decoded.Complexity)
	}
	if len(decoded.FilesExamined) != 3 {
		t.Errorf("files_examined count = %d, want 3 (%+v)", len(decoded.FilesExamined), decoded.FilesExamined)
	}
	if len(decoded.SubIssues) != 2 {
		t.Errorf("sub_issues count = %d, want 2 (%+v)", len(decoded.SubIssues), decoded.SubIssues)
	}
	if decoded.Plan == "" {
		t.Errorf("plan should be populated")
	}

	if !updated.FailureReason.Valid || updated.FailureReason.String != "bailed" {
		t.Errorf("expected FailureReason=bailed, got %+v", updated.FailureReason)
	}
}

// TestHandleBailReport_FallbackToReasonDetail covers the case where Native is
// empty (e.g., runtime returned a minimal BailReport): the supervisor should
// still populate BailAssessment.Reason and Plan from report.Reason / Detail
// so the stored row is not empty.
func TestHandleBailReport_FallbackToReasonDetail(t *testing.T) {
	h := newHarness(t)
	job := testJob(t, h.store, h.deploy)
	sc := h.newSlotContext(job)
	m := NewDefaultJobManager(sc, DefaultAutopilotContract())

	report := &runtimepkg.BailReport{
		Reason: "stuck on auth",
		Detail: "outline of plan",
	}
	m.handleBailReport(context.Background(), report)

	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	var decoded BailAssessment
	if err := json.Unmarshal([]byte(updated.ResultJSON.String), &decoded); err != nil {
		t.Fatalf("ResultJSON does not decode: %v", err)
	}
	if decoded.Reason != "stuck on auth" {
		t.Errorf("reason fallback failed: %q", decoded.Reason)
	}
	if decoded.Plan != "outline of plan" {
		t.Errorf("plan fallback failed: %q", decoded.Plan)
	}
}

// TestHandleBailReport_NilNoop verifies that a nil report is a no-op (no DB
// write, no event), so callers can pass through rt.ExtractBailReport without
// guarding.
func TestHandleBailReport_NilNoop(t *testing.T) {
	h := newHarness(t)
	job := testJob(t, h.store, h.deploy)
	sc := h.newSlotContext(job)
	m := NewDefaultJobManager(sc, DefaultAutopilotContract())

	m.handleBailReport(context.Background(), nil)

	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if updated.ResultJSON.Valid && updated.ResultJSON.String != "" {
		t.Errorf("expected empty ResultJSON, got %q", updated.ResultJSON.String)
	}
}
