package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

type capabilityCacheEntry struct {
	caps RuntimeCapabilities
	err  error
}

var (
	capabilityMu    sync.Mutex
	capabilityCache = map[string]capabilityCacheEntry{}
)

// CachedCapabilities returns a runtime's capability snapshot, probing at most
// once per runtime name in this process.
func CachedCapabilities(ctx context.Context, rt AgentRuntime) (RuntimeCapabilities, error) {
	if rt == nil {
		return RuntimeCapabilities{}, fmt.Errorf("runtime: nil runtime")
	}
	name := rt.Name()
	capabilityMu.Lock()
	if entry, ok := capabilityCache[name]; ok {
		capabilityMu.Unlock()
		return entry.caps, entry.err
	}
	capabilityMu.Unlock()

	caps, err := rt.Capabilities(ctx)
	if caps.RuntimeName == "" {
		caps.RuntimeName = name
	}

	capabilityMu.Lock()
	capabilityCache[name] = capabilityCacheEntry{caps: caps, err: err}
	capabilityMu.Unlock()
	return caps, err
}

// ResetCapabilityCache clears process capability probes. Intended for tests
// that swap fake runtime implementations under the same name.
func ResetCapabilityCache() {
	capabilityMu.Lock()
	defer capabilityMu.Unlock()
	capabilityCache = map[string]capabilityCacheEntry{}
}

// Preflight verifies the runtime CLI is available and the selected model is
// supported before Minder creates a worktree or launches an agent process.
func Preflight(ctx context.Context, rt AgentRuntime, model string) error {
	caps, err := CachedCapabilities(ctx, rt)
	if err != nil {
		return fmt.Errorf("preflight %s: %w", rt.Name(), err)
	}
	if !caps.Available {
		detail := caps.Error
		if detail == "" {
			detail = "CLI was not found or did not report a version"
		}
		if caps.BinaryName != "" {
			return fmt.Errorf("runtime %q unavailable: %s (%s)", caps.RuntimeName, caps.BinaryName, detail)
		}
		return fmt.Errorf("runtime %q unavailable: %s", caps.RuntimeName, detail)
	}
	model = NormalizeModelName(model)
	if !caps.SupportsModel(model) {
		return fmt.Errorf("runtime %q does not support model %q (supported: %s)",
			caps.RuntimeName, model, ModelSupportSummary(caps))
	}
	return nil
}

// ProbeCLIVersion checks for binary and runs its version command with a short
// timeout. It returns an unavailable capability fragment instead of an error so
// doctor can print every runtime in one pass.
func ProbeCLIVersion(ctx context.Context, runtimeName, binary string, args ...string) RuntimeCapabilities {
	caps := RuntimeCapabilities{
		RuntimeName: runtimeName,
		BinaryName:  binary,
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		caps.Error = err.Error()
		return caps
	}

	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, path, args...).CombinedOutput()
	if err != nil {
		caps.Error = strings.TrimSpace(string(out))
		if caps.Error == "" {
			caps.Error = err.Error()
		}
		return caps
	}
	caps.Available = true
	caps.Version = firstLine(strings.TrimSpace(string(out)))
	if caps.Version == "" {
		caps.Version = "available"
	}
	return caps
}

// ModelSupportSummary renders the model catalog compactly for CLI output and
// preflight errors.
func ModelSupportSummary(caps RuntimeCapabilities) string {
	parts := make([]string, 0, 4)
	if len(caps.Models) > 0 {
		models := append([]string(nil), caps.Models...)
		sort.Strings(models)
		parts = append(parts, strings.Join(models, ", "))
	}
	if len(caps.Aliases) > 0 {
		aliases := make([]string, 0, len(caps.Aliases))
		for alias, target := range caps.Aliases {
			if target == "" {
				aliases = append(aliases, alias)
			} else {
				aliases = append(aliases, alias+"="+target)
			}
		}
		sort.Strings(aliases)
		parts = append(parts, "aliases: "+strings.Join(aliases, ", "))
	}
	if len(caps.ModelPrefixes) > 0 {
		prefixes := append([]string(nil), caps.ModelPrefixes...)
		sort.Strings(prefixes)
		parts = append(parts, "prefixes: "+strings.Join(prefixes, ", "))
	}
	if len(caps.ModelFormats) > 0 {
		formats := append([]string(nil), caps.ModelFormats...)
		sort.Strings(formats)
		parts = append(parts, "formats: "+strings.Join(formats, ", "))
	}
	if len(parts) == 0 {
		return "runtime default only"
	}
	return strings.Join(parts, "; ")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
