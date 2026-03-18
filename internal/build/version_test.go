package build

import (
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must not be empty")
	}
}

func TestVersionString(t *testing.T) {
	got := VersionString()
	want := "agent-minder v" + Version

	if got != want {
		t.Errorf("VersionString() = %q, want %q", got, want)
	}

	if !strings.HasPrefix(got, "agent-minder v") {
		t.Errorf("VersionString() should start with 'agent-minder v', got %q", got)
	}
}
