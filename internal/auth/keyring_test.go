package auth

import "testing"

func TestEnvTokenPrefersGitHubToken(t *testing.T) {
	t.Setenv(EnvGitHubToken, "github-token")
	t.Setenv(EnvGitHubTokenAlt, "gh-token")

	tok, name := EnvToken()
	if tok != "github-token" || name != EnvGitHubToken {
		t.Fatalf("EnvToken() = (%q, %q), want (%q, %q)", tok, name, "github-token", EnvGitHubToken)
	}
}

func TestEnvTokenFallsBackToGHToken(t *testing.T) {
	t.Setenv(EnvGitHubToken, "")
	t.Setenv(EnvGitHubTokenAlt, "gh-token")

	tok, name := EnvToken()
	if tok != "gh-token" || name != EnvGitHubTokenAlt {
		t.Fatalf("EnvToken() = (%q, %q), want (%q, %q)", tok, name, "gh-token", EnvGitHubTokenAlt)
	}
}
