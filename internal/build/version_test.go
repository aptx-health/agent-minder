package build

import (
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	if Version == "" {
		t.Error("Version should not be empty")
	}
}

func TestVersionString(t *testing.T) {
	got := VersionString()
	if !strings.HasPrefix(got, "agent-minder v") {
		t.Errorf("VersionString() = %q, want prefix %q", got, "agent-minder v")
	}
	if !strings.Contains(got, Version) {
		t.Errorf("VersionString() = %q, should contain Version %q", got, Version)
	}
}
