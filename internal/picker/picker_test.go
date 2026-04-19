package picker

import (
	"database/sql"
	"testing"

	"github.com/aptx-health/agent-minder/internal/daemon"
	"github.com/aptx-health/agent-minder/internal/db"
)

func TestFilterJobs(t *testing.T) {
	jobs := []*db.Job{
		{Agent: "spike", Name: "spike-issue-518", IssueNumber: 518, IssueTitle: sql.NullString{String: "[Feature] It'd be great if the...", Valid: true}, Status: "done", CostUSD: 0.88},
		{Agent: "ux-autopilot", Name: "ux-autopilot-issue-529", IssueNumber: 529, IssueTitle: sql.NullString{String: "Cleanup: remove old tour system", Valid: true}, Status: "reviewed", PRNumber: sql.NullInt64{Int64: 532, Valid: true}, CostUSD: 2.01},
		{Agent: "bug-fixer", Name: "bug-fixer-issue-521", IssueNumber: 521, IssueTitle: sql.NullString{String: "[Bug] Loading frog icon shows up", Valid: true}, Status: "reviewed", PRNumber: sql.NullInt64{Int64: 523, Valid: true}, CostUSD: 0.44},
	}

	tests := []struct {
		name   string
		filter string
		want   int
	}{
		{"empty filter returns all", "", 3},
		{"filter by agent name", "spike", 1},
		{"filter by issue number", "529", 1},
		{"filter by PR number", "532", 1},
		{"filter by title substring", "frog", 1},
		{"filter by status", "done", 1},
		{"case insensitive", "BUG-FIXER", 1},
		{"no match", "nonexistent", 0},
		{"partial agent match", "auto", 1},
		{"multiple matches", "reviewed", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterJobs(jobs, tt.filter)
			if len(got) != tt.want {
				t.Errorf("FilterJobs(%q) returned %d jobs, want %d", tt.filter, len(got), tt.want)
			}
		})
	}
}

func TestFilterRemoteJobs(t *testing.T) {
	jobs := []daemon.JobResponse{
		{Agent: "spike", Name: "spike-issue-518", IssueNumber: 518, Title: "[Feature] It'd be great if the...", Status: "done", CostUSD: 0.88},
		{Agent: "ux-autopilot", Name: "ux-autopilot-issue-529", IssueNumber: 529, Title: "Cleanup: remove old tour system", Status: "reviewed", PRNumber: 532, CostUSD: 2.01},
	}

	tests := []struct {
		name   string
		filter string
		want   int
	}{
		{"empty filter returns all", "", 2},
		{"filter by agent", "spike", 1},
		{"filter by PR number", "532", 1},
		{"no match", "xyz", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterRemoteJobs(jobs, tt.filter)
			if len(got) != tt.want {
				t.Errorf("FilterRemoteJobs(%q) returned %d jobs, want %d", tt.filter, len(got), tt.want)
			}
		})
	}
}
