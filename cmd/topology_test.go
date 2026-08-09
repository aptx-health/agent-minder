package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestCoordinatorNewCallersAreTopologyOwners guards topology invariant I-4
// (see docs/research/fable-expedition/02-worker-process-topology-adr.md §6):
// process-global equals deployment-global only holds because exactly one
// process (runForeground or runDaemon) constructs exactly one Coordinator for
// a deployment. If a future refactor adds another coordinator.New call site
// (e.g. spawning a second Coordinator in the same process, or from a
// different command), the invariant runtime adapters rely on — such as
// opencode's shared-server-per-process teardown — silently breaks. This test
// fails loudly instead.
func TestCoordinatorNewCallersAreTopologyOwners(t *testing.T) {
	const allowedFile = "deploy.go"
	allowedFuncs := map[string]bool{
		"runForeground": true,
		"runDaemon":     true,
	}

	callRe := regexp.MustCompile(`\bcoordinator\.New\(`)
	funcRe := regexp.MustCompile(`^func\s+(\w+)\s*\(`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd dir: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		content := string(data)
		if !callRe.MatchString(content) {
			continue
		}

		if name != allowedFile {
			t.Errorf("coordinator.New called from %s, expected only %s (functions %v) per topology invariant I-4",
				name, allowedFile, sortedKeys(allowedFuncs))
			continue
		}

		// Walk line by line, tracking the enclosing top-level function, and
		// flag any coordinator.New call outside the allowed functions.
		currentFunc := ""
		for _, line := range strings.Split(content, "\n") {
			if m := funcRe.FindStringSubmatch(line); m != nil {
				currentFunc = m[1]
			}
			if callRe.MatchString(line) && !allowedFuncs[currentFunc] {
				t.Errorf("coordinator.New called from %s() in %s, expected only %v per topology invariant I-4",
					currentFunc, name, sortedKeys(allowedFuncs))
			}
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
