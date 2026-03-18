package discovery

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dustinlange/agent-minder/internal/agentutil"
)

// ScanAgentLogs scans .log files in the given directory for permission
// failures from prior Claude Code agent runs. If projectName is non-empty,
// only log files matching "<projectName>-issue-*.log" are scanned;
// otherwise all *.log files are scanned (not recommended for shared log dirs).
// Returns a deduplicated, sorted list of denied tool patterns
// (e.g., "Bash(go *)", "Write").
// Returns nil if the directory does not exist or contains no matching logs.
func ScanAgentLogs(logDir string, projectName string) []string {
	if !dirExists(logDir) {
		return nil
	}

	pattern := "*.log"
	if projectName != "" {
		pattern = projectName + "-issue-*.log"
	}

	matches, err := filepath.Glob(filepath.Join(logDir, pattern))
	if err != nil || len(matches) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	var patterns []string

	for _, logPath := range matches {
		result, err := agentutil.ParseAgentLog(logPath)
		if err != nil || result == nil {
			continue
		}
		if len(result.PermissionDenials) == 0 {
			continue
		}
		for _, raw := range result.PermissionDenials {
			p := denialRawToPattern(raw)
			if p != "" && !seen[p] {
				seen[p] = true
				patterns = append(patterns, p)
			}
		}
	}

	sort.Strings(patterns)
	return patterns
}

// denialRawToPattern converts a json.RawMessage permission denial into
// a human-readable tool pattern string. Handles both object
// ({"tool_name":"Bash",...}) and plain string ("Write") formats.
func denialRawToPattern(raw json.RawMessage) string {
	// Try as object with tool_name field.
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		name, _ := obj["tool_name"].(string)
		if name == "" {
			return ""
		}
		if name == "Bash" {
			if cmd := extractBashCommand(obj); cmd != "" {
				parts := strings.Fields(cmd)
				if len(parts) > 0 {
					return fmt.Sprintf("Bash(%s *)", parts[0])
				}
			}
		}
		return name
	}

	// Try as plain string.
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	return ""
}

// extractBashCommand tries to find the denied command from a permission
// denial object. Checks "command" at top level and nested "tool_input".
func extractBashCommand(obj map[string]any) string {
	if cmd, ok := obj["command"].(string); ok && cmd != "" {
		return cmd
	}
	if input, ok := obj["tool_input"].(map[string]any); ok {
		if cmd, ok := input["command"].(string); ok && cmd != "" {
			return cmd
		}
	}
	return ""
}
