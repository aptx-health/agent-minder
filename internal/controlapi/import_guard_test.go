package controlapi

import (
	"os"
	"strings"
	"testing"
)

func TestControlAPIDoesNotImportExecutionInternals(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(data), "/internal/supervisor") {
			t.Errorf("%s imports internal/supervisor; controlapi must use coordinator.StateProvider", name)
		}
	}
}
