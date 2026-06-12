package runtime

import (
	"fmt"
	"sort"
	"sync"
)

// Known runtime names. ClaudeCode is the default and is the only runtime with
// a concrete implementation today; additional entries (e.g., "codex") will be
// added as their adapters land.
const (
	NameClaudeCode = "claude-code"
)

// DefaultName is the runtime selected when the operator does not pass
// --runtime. It must always reference a known runtime.
const DefaultName = NameClaudeCode

// Factory constructs a fresh AgentRuntime instance. Runtimes register their
// factories from package init() to avoid an import cycle between this package
// and the concrete adapters.
type Factory func() AgentRuntime

var (
	registryMu sync.RWMutex
	factories  = map[string]Factory{}
)

// Register associates name with factory. Intended to be called from a runtime
// adapter's package init(). Panics if name is empty or already registered, to
// surface wiring mistakes at process start rather than at Lookup time.
func Register(name string, factory Factory) {
	if name == "" {
		panic("runtime: Register called with empty name")
	}
	if factory == nil {
		panic("runtime: Register called with nil factory for " + name)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := factories[name]; exists {
		panic("runtime: duplicate Register for " + name)
	}
	factories[name] = factory
}

// KnownNames returns the registered runtime identifiers in a stable sorted
// order suitable for error messages and CLI help text.
func KnownNames() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(factories))
	for n := range factories {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Validate returns nil if name refers to a registered runtime, and a clear
// error otherwise. An empty name is treated as a configuration bug, not as a
// request for the default — callers should resolve defaults before calling
// Validate.
func Validate(name string) error {
	if name == "" {
		return fmt.Errorf("runtime: name is empty (expected one of %v)", KnownNames())
	}
	registryMu.RLock()
	_, ok := factories[name]
	registryMu.RUnlock()
	if !ok {
		return fmt.Errorf("runtime: unknown runtime %q (expected one of %v)", name, KnownNames())
	}
	return nil
}

// Lookup returns a fresh AgentRuntime for the registered name. Errors are
// consistent with Validate: empty or unknown names produce the same messages.
func Lookup(name string) (AgentRuntime, error) {
	if err := Validate(name); err != nil {
		return nil, err
	}
	registryMu.RLock()
	factory := factories[name]
	registryMu.RUnlock()
	return factory(), nil
}
