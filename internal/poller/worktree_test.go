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

func TestDiffWorktrees(t *testing.T) {
	tests := []struct {
		name        string
		existing    []db.Worktree
		incoming    []db.Worktree
		wantAdded   []string
		wantRemoved []string
	}{
		{
			name:        "no changes",
			existing:    []db.Worktree{{Path: "/a", Branch: "main"}},
			incoming:    []db.Worktree{{Path: "/a", Branch: "main"}},
			wantAdded:   nil,
			wantRemoved: nil,
		},
		{
			name:        "new worktree added",
			existing:    []db.Worktree{{Path: "/a", Branch: "main"}},
			incoming:    []db.Worktree{{Path: "/a", Branch: "main"}, {Path: "/b", Branch: "feature/auth"}},
			wantAdded:   []string{"feature/auth"},
			wantRemoved: nil,
		},
		{
			name:        "worktree removed",
			existing:    []db.Worktree{{Path: "/a", Branch: "main"}, {Path: "/b", Branch: "feature/auth"}},
			incoming:    []db.Worktree{{Path: "/a", Branch: "main"}},
			wantAdded:   nil,
			wantRemoved: []string{"feature/auth"},
		},
		{
			name:        "branch switched on same path",
			existing:    []db.Worktree{{Path: "/a", Branch: "main"}},
			incoming:    []db.Worktree{{Path: "/a", Branch: "develop"}},
			wantAdded:   []string{"develop"},
			wantRemoved: []string{"main"},
		},
		{
			name:        "from empty",
			existing:    nil,
			incoming:    []db.Worktree{{Path: "/a", Branch: "main"}},
			wantAdded:   []string{"main"},
			wantRemoved: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, removed := diffWorktrees(tt.existing, tt.incoming)
			if !slicesEqual(added, tt.wantAdded) {
				t.Errorf("added = %v, want %v", added, tt.wantAdded)
			}
			if !slicesEqual(removed, tt.wantRemoved) {
				t.Errorf("removed = %v, want %v", removed, tt.wantRemoved)
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
