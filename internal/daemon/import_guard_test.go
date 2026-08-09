package daemon

import (
	"os"
	"strings"
	"testing"
)

// TestHandlersNeverImportSupervisor is a grep-style guard for DM-5: handler
// code reaches the deployment only through coordinator.StateProvider, so this
// package must never import internal/supervisor. The same rule applies to the
// future internal/controlapi package.
func TestHandlersNeverImportSupervisor(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(source), `"github.com/aptx-health/agent-minder/internal/supervisor"`) {
			t.Errorf("%s imports internal/supervisor; handlers must consume coordinator.StateProvider instead (DM-5)", name)
		}
	}
}
