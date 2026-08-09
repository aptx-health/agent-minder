package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CommandVersion returns a trimmed CLI version string using a short child
// context. Version probes are advisory metadata, so callers should log and
// continue when this returns an error.
func CommandVersion(ctx context.Context, bin string, args ...string) (string, error) {
	if bin == "" {
		return "", fmt.Errorf("runtime: version command binary is empty")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	out, err := exec.CommandContext(probeCtx, bin, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s version: %w", bin, err)
	}
	return strings.TrimSpace(string(out)), nil
}
