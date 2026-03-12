package poller

import (
	"testing"

	"github.com/dustinlange/agent-minder/internal/db"
)

func TestWorktreesChanged(t *testing.T) {
	tests := []struct {
		name     string
		existing []db.Worktree
		incoming []db.Worktree
		want     bool
	}{
		{
			name:     "both empty",
			existing: nil,
			incoming: nil,
			want:     false,
		},
		{
			name:     "empty vs non-empty",
			existing: nil,
			incoming: []db.Worktree{{Path: "/a", Branch: "main"}},
			want:     true,
		},
		{
			name:     "identical single entry",
			existing: []db.Worktree{{Path: "/a", Branch: "main"}},
			incoming: []db.Worktree{{Path: "/a", Branch: "main"}},
			want:     false,
		},
		{
			name:     "same paths different branches",
			existing: []db.Worktree{{Path: "/a", Branch: "main"}},
			incoming: []db.Worktree{{Path: "/a", Branch: "develop"}},
			want:     true,
		},
		{
			name: "reordered entries",
			existing: []db.Worktree{
				{Path: "/a", Branch: "main"},
				{Path: "/b", Branch: "feature"},
			},
			incoming: []db.Worktree{
				{Path: "/b", Branch: "feature"},
				{Path: "/a", Branch: "main"},
			},
			want: false,
		},
		{
			name: "added worktree",
			existing: []db.Worktree{
				{Path: "/a", Branch: "main"},
			},
			incoming: []db.Worktree{
				{Path: "/a", Branch: "main"},
				{Path: "/b", Branch: "feature"},
			},
			want: true,
		},
		{
			name: "removed worktree",
			existing: []db.Worktree{
				{Path: "/a", Branch: "main"},
				{Path: "/b", Branch: "feature"},
			},
			incoming: []db.Worktree{
				{Path: "/a", Branch: "main"},
			},
			want: true,
		},
		{
			name: "IDs differ but path+branch same",
			existing: []db.Worktree{
				{ID: 1, RepoID: 10, Path: "/a", Branch: "main"},
			},
			incoming: []db.Worktree{
				{ID: 99, RepoID: 20, Path: "/a", Branch: "main"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := worktreesChanged(tt.existing, tt.incoming)
			if got != tt.want {
				t.Errorf("worktreesChanged() = %v, want %v", got, tt.want)
			}
		})
	}
}
