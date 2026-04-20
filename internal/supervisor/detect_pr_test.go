package supervisor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectPRFromLog(t *testing.T) {
	tests := []struct {
		name   string
		owner  string
		repo   string
		log    string
		wantPR int
	}{
		{
			name:  "exact match",
			owner: "acme", repo: "widgets",
			log:    `PR created: https://github.com/acme/widgets/pull/42`,
			wantPR: 42,
		},
		{
			name:  "renamed repo fallback",
			owner: "acme", repo: "old-name",
			log:    `PR created: https://github.com/acme/new-name/pull/538`,
			wantPR: 538,
		},
		{
			name:  "multiple PRs takes last",
			owner: "acme", repo: "widgets",
			log:    `https://github.com/acme/widgets/pull/10 then https://github.com/acme/widgets/pull/20`,
			wantPR: 20,
		},
		{
			name:  "no PR URL",
			owner: "acme", repo: "widgets",
			log:    `no pull request here`,
			wantPR: 0,
		},
		{
			name:  "PR in stream-json",
			owner: "acme", repo: "old-repo",
			log:    `{"type":"user","content":"https://github.com/acme/renamed-repo/pull/99"}`,
			wantPR: 99,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "agent.log")
			_ = os.WriteFile(logPath, []byte(tt.log), 0o644)

			got := detectPRFromLog(logPath, tt.owner, tt.repo)
			if got != tt.wantPR {
				t.Errorf("detectPRFromLog() = %d, want %d", got, tt.wantPR)
			}
		})
	}
}
