package opencode

import "strings"

// looksLikeUsageLimit reports whether s carries a rate-limit / quota / billing
// signal, used to raise OnUsageLimit and classify outcomes.
func looksLikeUsageLimit(s string) bool {
	s = strings.ToLower(s)
	for _, marker := range []string{"rate limit", "rate_limit", "quota", "billing", "usage limit", "usage_limit", "too many requests"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// truncate shortens s to at most maxLen runes, appending an ellipsis when cut.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
