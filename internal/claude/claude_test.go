package claude

import (
	"testing"
)

func TestIsAvailable(t *testing.T) {
	// Just verify it doesn't panic. The result depends on the environment.
	_ = IsAvailable()
}

func TestVersion(t *testing.T) {
	if !IsAvailable() {
		t.Skip("claude CLI not available")
	}

	ver, err := Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if ver == "" {
		t.Error("Version returned empty string")
	}
	t.Logf("claude version: %s", ver)
}
