package runtime

import "fmt"

// Known runtime names. ClaudeCode is the default and is the only runtime with
// a concrete implementation today; additional entries (e.g., "codex") will be
// added as their adapters land.
const (
	NameClaudeCode = "claude-code"
)

// DefaultName is the runtime selected when the operator does not pass
// --runtime. It must always reference a known runtime.
const DefaultName = NameClaudeCode

// knownRuntimes is the canonical set of accepted --runtime values. Unknown
// values are rejected early by Validate so an operator never gets surprised
// by a typo deep inside a daemon process.
var knownRuntimes = map[string]struct{}{
	NameClaudeCode: {},
}

// KnownNames returns the accepted runtime identifiers in a stable order
// suitable for error messages and CLI help text.
func KnownNames() []string {
	// Keep stable / sorted-by-insertion order; today there is only one.
	return []string{NameClaudeCode}
}

// Validate returns nil if name refers to a known runtime, and a clear error
// otherwise. An empty name is treated as a configuration bug, not as a
// request for the default — callers should resolve defaults before calling
// Validate.
func Validate(name string) error {
	if name == "" {
		return fmt.Errorf("runtime: name is empty (expected one of %v)", KnownNames())
	}
	if _, ok := knownRuntimes[name]; !ok {
		return fmt.Errorf("runtime: unknown runtime %q (expected one of %v)", name, KnownNames())
	}
	return nil
}
