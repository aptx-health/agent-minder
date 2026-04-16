package cmd

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/aptx-health/agent-minder/internal/daemon"
	"github.com/aptx-health/agent-minder/internal/db"
)

func TestCandidateBranchNames(t *testing.T) {
	tests := []struct {
		name string
		job  *db.Job
		want []string
	}{
		{
			name: "issue job with recorded branch",
			job: &db.Job{
				Name:        "bug-fixer-issue-480",
				IssueNumber: 480,
				Branch:      sql.NullString{String: "agent/bug-fixer-issue-480", Valid: true},
			},
			// Recorded branch first, then supervisor convention for issues, then agent/<name>.
			want: []string{"agent/bug-fixer-issue-480", "agent/issue-480"},
		},
		{
			name: "issue job with no recorded branch",
			job: &db.Job{
				Name:        "bug-fixer-issue-480",
				IssueNumber: 480,
			},
			// Should prefer supervisor's actual convention (agent/issue-N), then fall back.
			want: []string{"agent/issue-480", "agent/bug-fixer-issue-480"},
		},
		{
			name: "proactive job with no issue number",
			job: &db.Job{
				Name: "weekly-deps-2026-04-01",
			},
			want: []string{"agent/weekly-deps-2026-04-01"},
		},
		{
			name: "recorded branch matches issue convention — no duplicates",
			job: &db.Job{
				Name:        "issue-42",
				IssueNumber: 42,
				Branch:      sql.NullString{String: "agent/issue-42", Valid: true},
			},
			want: []string{"agent/issue-42"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := candidateBranchNames(tt.job)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("candidateBranchNames() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCandidateBranchNamesRemote(t *testing.T) {
	tests := []struct {
		name string
		job  *daemon.JobResponse
		want []string
	}{
		{
			name: "issue job with recorded branch",
			job: &daemon.JobResponse{
				Name:        "bug-fixer-issue-480",
				IssueNumber: 480,
				Branch:      "agent/bug-fixer-issue-480",
			},
			want: []string{"agent/bug-fixer-issue-480", "agent/issue-480"},
		},
		{
			name: "issue job with no recorded branch",
			job: &daemon.JobResponse{
				Name:        "bug-fixer-issue-480",
				IssueNumber: 480,
			},
			want: []string{"agent/issue-480", "agent/bug-fixer-issue-480"},
		},
		{
			name: "no info at all",
			job:  &daemon.JobResponse{},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := candidateBranchNamesRemote(tt.job)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("candidateBranchNamesRemote() = %v, want %v", got, tt.want)
			}
		})
	}
}
