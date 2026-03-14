package poller

import (
	"testing"
)

func TestParseGitHubRemote_HTTPS(t *testing.T) {
	owner, repo := parseGitHubRemote("https://github.com/aptx-health/agent-minder.git")
	if owner != "aptx-health" || repo != "agent-minder" {
		t.Errorf("got owner=%q repo=%q", owner, repo)
	}
}

func TestParseGitHubRemote_HTTPSNoGit(t *testing.T) {
	owner, repo := parseGitHubRemote("https://github.com/octocat/hello-world")
	if owner != "octocat" || repo != "hello-world" {
		t.Errorf("got owner=%q repo=%q", owner, repo)
	}
}

func TestParseGitHubRemote_SSH(t *testing.T) {
	owner, repo := parseGitHubRemote("git@github.com:aptx-health/agent-minder.git")
	if owner != "aptx-health" || repo != "agent-minder" {
		t.Errorf("got owner=%q repo=%q", owner, repo)
	}
}

func TestParseGitHubRemote_Empty(t *testing.T) {
	owner, repo := parseGitHubRemote("")
	if owner != "" || repo != "" {
		t.Errorf("expected empty, got owner=%q repo=%q", owner, repo)
	}
}

func TestParseGitHubRemote_NonGitHub(t *testing.T) {
	owner, repo := parseGitHubRemote("https://gitlab.com/foo/bar.git")
	if owner != "" || repo != "" {
		t.Errorf("expected empty for non-GitHub, got owner=%q repo=%q", owner, repo)
	}
}
