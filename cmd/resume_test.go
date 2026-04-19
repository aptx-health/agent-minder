package cmd

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/aptx-health/agent-minder/internal/db"
)

func TestBuildResumePrompt(t *testing.T) {
	tests := []struct {
		name     string
		job      *db.Job
		contains []string
	}{
		{
			name: "bailed job dumps failure info",
			job: &db.Job{
				Agent:         "bug-fixer",
				IssueNumber:   520,
				IssueTitle:    sql.NullString{String: "Fix the login bug", Valid: true},
				IssueBody:     sql.NullString{String: "Users can't log in", Valid: true},
				Status:        db.StatusBailed,
				FailureReason: sql.NullString{String: "permission_denied", Valid: true},
				FailureDetail: sql.NullString{String: "Could not write to file", Valid: true},
			},
			contains: []string{
				"#520", "bug-fixer", "bailed",
				"permission_denied", "Could not write to file",
				"Users can't log in",
			},
		},
		{
			name: "PR job includes all metadata",
			job: &db.Job{
				Agent:       "autopilot",
				IssueNumber: 529,
				IssueTitle:  sql.NullString{String: "Cleanup old tour", Valid: true},
				Status:      db.StatusReviewed,
				PRNumber:    sql.NullInt64{Int64: 532, Valid: true},
				ReviewRisk:  sql.NullString{String: "needs-testing", Valid: true},
				Branch:      sql.NullString{String: "agent/issue-529", Valid: true},
				CostUSD:     2.01,
			},
			contains: []string{
				"#529", "autopilot", "PR: #532",
				"needs-testing", "$2.01", "agent/issue-529",
			},
		},
		{
			name: "spike with result JSON",
			job: &db.Job{
				Agent:       "spike",
				IssueNumber: 518,
				IssueTitle:  sql.NullString{String: "Research caching", Valid: true},
				Status:      "done",
				ResultJSON:  sql.NullString{String: `{"recommendation": "use Redis"}`, Valid: true},
			},
			contains: []string{
				"spike", "#518", "use Redis",
			},
		},
		{
			name: "minimal job still works",
			job: &db.Job{
				Agent:  "autopilot",
				Name:   "weekly-cleanup",
				Status: "done",
			},
			contains: []string{
				"autopilot", "done",
				"git log",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := buildResumePrompt(tt.job)
			for _, s := range tt.contains {
				if !strings.Contains(prompt, s) {
					t.Errorf("prompt missing %q\nprompt:\n%s", s, prompt)
				}
			}
		})
	}
}
