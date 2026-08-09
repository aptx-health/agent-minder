package cmd

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
)

func TestBuildJobsHistoryJSON_Shape(t *testing.T) {
	queuedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	startedAt := queuedAt.Add(time.Minute)
	completedAt := startedAt.Add(5 * time.Minute)

	jobs := []*db.Job{
		{
			ID:            7,
			Name:          "nightly-scan-3",
			Status:        db.StatusDone,
			SourceType:    sql.NullString{String: "cron", Valid: true},
			SourceName:    sql.NullString{String: "nightly-scan", Valid: true},
			CostUSD:       1.23,
			PRNumber:      sql.NullInt64{Int64: 99, Valid: true},
			QueuedAt:      sql.NullTime{Time: queuedAt, Valid: true},
			StartedAt:     sql.NullTime{Time: startedAt, Valid: true},
			CompletedAt:   sql.NullTime{Time: completedAt, Valid: true},
			FailureReason: sql.NullString{},
		},
		{
			ID:         6,
			Name:       "nightly-scan-2",
			Status:     db.StatusBailed,
			SourceType: sql.NullString{String: "cron", Valid: true},
			SourceName: sql.NullString{String: "nightly-scan", Valid: true},
			FailureReason: sql.NullString{
				String: "gave up after 40 turns without opening a PR",
				Valid:  true,
			},
		},
	}

	got := buildJobsHistoryJSON("nightly-scan", "deploy-a", jobs)

	if got.Automation != "nightly-scan" || got.DeployID != "deploy-a" {
		t.Fatalf("got automation=%q deploy=%q, want nightly-scan/deploy-a", got.Automation, got.DeployID)
	}
	if len(got.Runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(got.Runs))
	}

	first := got.Runs[0]
	if first.JobID != 7 || first.SourceType != "cron" || first.Status != db.StatusDone {
		t.Errorf("unexpected first entry: %+v", first)
	}
	if first.PRNumber != 99 {
		t.Errorf("PRNumber = %d, want 99", first.PRNumber)
	}
	if first.CostUSD != 1.23 {
		t.Errorf("CostUSD = %v, want 1.23", first.CostUSD)
	}
	if first.QueuedAt == "" || first.StartedAt == "" || first.CompletedAt == "" {
		t.Errorf("expected timestamps to be populated, got %+v", first)
	}
	if first.FailureReason != "" {
		t.Errorf("FailureReason = %q, want empty", first.FailureReason)
	}

	second := got.Runs[1]
	if second.FailureReason != "gave up after 40 turns without opening a PR" {
		t.Errorf("FailureReason = %q, want the bail reason", second.FailureReason)
	}
	if second.PRNumber != 0 {
		t.Errorf("PRNumber = %d, want 0 for a job with no PR", second.PRNumber)
	}

	// The JSON must round-trip through encoding/json (golden-shape guard):
	// omitempty fields on a zero-valued run should not appear.
	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var decoded jobsHistoryJSON
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(decoded.Runs) != 2 {
		t.Fatalf("round-tripped %d runs, want 2", len(decoded.Runs))
	}
}

func TestBuildJobsHistoryJSON_Empty(t *testing.T) {
	got := buildJobsHistoryJSON("no-runs-yet", "deploy-a", nil)
	if got.Runs == nil {
		t.Fatal("Runs should be an empty slice, not nil, so JSON encodes [] rather than null")
	}
	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if string(blob) != `{"automation":"no-runs-yet","deploy_id":"deploy-a","runs":[]}` {
		t.Errorf("got %s", blob)
	}
}
