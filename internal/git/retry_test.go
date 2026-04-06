package git

import (
	"errors"
	"testing"
)

func TestIsNetworkError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"generic error", errors.New("something broke"), false},
		{"ssh timeout", errors.New("git fetch origin: ssh: connect to host github.com port 22: Operation timed out"), true},
		{"connection refused", errors.New("ssh: connect to host github.com port 22: Connection refused"), true},
		{"could not read", errors.New("fatal: Could not read from remote repository."), true},
		{"unable to access", errors.New("fatal: unable to access 'https://github.com/foo/bar.git/'"), true},
		{"network unreachable", errors.New("Network is unreachable"), true},
		{"broken pipe", errors.New("fatal: the remote end hung up unexpectedly; Broken pipe"), true},
		{"permission denied", errors.New("Permission denied (publickey)"), false},
		{"not a git repo", errors.New("fatal: not a git repository"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsNetworkError(tt.err)
			if got != tt.want {
				t.Errorf("IsNetworkError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestFetchWithRetry_FailsAndReturnsError(t *testing.T) {
	dir := setupTestRepo(t)
	err := FetchWithRetry(dir, 1, nil)
	if err == nil {
		t.Error("expected error for repo with no remote")
	}
}

func TestFetchWithRetry_CallbackCalled(t *testing.T) {
	dir := setupTestRepo(t)
	var calls []int
	// maxAttempts=1 means no retries, callback should not be called.
	_ = FetchWithRetry(dir, 1, func(attempt, max int, err error) {
		calls = append(calls, attempt)
	})
	if len(calls) != 0 {
		t.Errorf("expected no callback calls with maxAttempts=1, got %d", len(calls))
	}
}
