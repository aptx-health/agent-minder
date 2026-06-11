package runtime

import (
	"strings"
	"testing"
)

func TestDefaultNameIsKnown(t *testing.T) {
	if err := Validate(DefaultName); err != nil {
		t.Fatalf("DefaultName %q must validate: %v", DefaultName, err)
	}
	if DefaultName != NameClaudeCode {
		t.Fatalf("DefaultName changed unexpectedly: got %q, want %q", DefaultName, NameClaudeCode)
	}
}

func TestValidateKnown(t *testing.T) {
	for _, n := range KnownNames() {
		if err := Validate(n); err != nil {
			t.Errorf("Validate(%q) returned error: %v", n, err)
		}
	}
}

func TestValidateRejectsUnknown(t *testing.T) {
	cases := []string{"codex", "Claude-Code", "CLAUDE-CODE", "claude_code", "gpt", "  claude-code"}
	for _, c := range cases {
		err := Validate(c)
		if err == nil {
			t.Errorf("Validate(%q) accepted unknown runtime", c)
			continue
		}
		if !strings.Contains(err.Error(), "unknown runtime") {
			t.Errorf("Validate(%q) error %q missing %q", c, err, "unknown runtime")
		}
	}
}

func TestValidateRejectsEmpty(t *testing.T) {
	err := Validate("")
	if err == nil {
		t.Fatal("Validate(\"\") accepted empty string")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("Validate(\"\") error %q missing %q", err, "empty")
	}
}

func TestKnownNamesIncludesClaudeCode(t *testing.T) {
	names := KnownNames()
	if len(names) == 0 {
		t.Fatal("KnownNames returned empty slice")
	}
	found := false
	for _, n := range names {
		if n == NameClaudeCode {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("KnownNames does not include %q: %v", NameClaudeCode, names)
	}
}
