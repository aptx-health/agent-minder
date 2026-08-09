package runtime

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// Known runtime names. ClaudeCode is the default and is the only runtime with
// a concrete implementation today; additional entries (e.g., "codex") will be
// added as their adapters land.
const (
	NameClaudeCode = "claude-code"
	NameCodex      = "codex"
	NameOpenCode   = "opencode"
)

// DefaultName is the runtime selected when the operator does not pass
// --runtime. It must always reference a known runtime.
const DefaultName = NameClaudeCode

// Factory constructs a fresh AgentRuntime instance. Runtimes register their
// factories from package init() to avoid an import cycle between this package
// and the concrete adapters.
type Factory func() AgentRuntime

var (
	registryMu    sync.RWMutex
	factories     = map[string]Factory{}
	shutdownHooks []func()
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

// RegisterShutdown registers a process-level teardown hook for a runtime that
// owns shared resources beyond a single Run — e.g. opencode's shared
// `opencode serve` process. Hooks run when ShutdownAll is called on clean
// shutdown. Stateless runtimes (claude-code, codex) are per-process and need
// not register one. Intended to be called from a runtime adapter's init(); a
// nil fn is ignored.
//
// This is only safe under topology invariant I-4 (process-global equals
// deployment-global, see AgentRuntime doc comment and
// docs/research/fable-expedition/02-worker-process-topology-adr.md §6): a
// process-level hook is correct because one process hosts exactly one
// Coordinator driving exactly one deployment. A resource that must be shared
// *across* deployments on a host (I-5) must not be registered here — it
// belongs outside the runtime as an externally configured service.
func RegisterShutdown(fn func()) {
	if fn == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	shutdownHooks = append(shutdownHooks, fn)
}

// ShutdownAll invokes every registered runtime teardown hook. It is safe to
// call when no runtime allocated shared resources — hooks guard their own nil
// case — and safe to call more than once. Shutdown-path callers (the supervisor
// on Stop, the daemon on teardown) invoke it once in-flight work has drained.
func ShutdownAll() {
	registryMu.RLock()
	hooks := make([]func(), len(shutdownHooks))
	copy(hooks, shutdownHooks)
	registryMu.RUnlock()
	for _, fn := range hooks {
		fn()
	}
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

// NormalizeModelName trims an optional runtime-native model name. Empty means
// "use the selected runtime's default model".
func NormalizeModelName(name string) string {
	return strings.TrimSpace(name)
}

// ValidateModelName rejects malformed model names while intentionally avoiding
// a runtime-specific model catalog. Runtimes and providers add aliases over time,
// so a well-formed unknown name is passed through and, if unsupported, the CLI
// error is surfaced with job context.
func ValidateModelName(name string) error {
	name = NormalizeModelName(name)
	if name == "" {
		return nil
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		switch r {
		case '-', '_', '.', ':', '/':
			continue
		default:
			return fmt.Errorf("invalid model %q: use a runtime-native model name or alias without spaces or shell characters", name)
		}
	}
	return nil
}
