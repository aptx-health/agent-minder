package cmd

import "testing"

func TestCheckMark(t *testing.T) {
	if got := checkMark(true); got != "yes" {
		t.Errorf("checkMark(true) = %q, want %q", got, "yes")
	}
	if got := checkMark(false); got != "no" {
		t.Errorf("checkMark(false) = %q, want %q", got, "no")
	}
}

func TestDefaultAgentLogDir(t *testing.T) {
	dir := defaultAgentLogDir()
	if dir == "" {
		t.Skip("could not determine home directory")
	}
	// Should end with .agent-minder/agents.
	if len(dir) < len(".agent-minder/agents") {
		t.Errorf("defaultAgentLogDir() = %q, too short", dir)
	}
}
