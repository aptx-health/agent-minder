package poller

import "fmt"

// Context budget constants control how much data is sent to LLM calls.
// Keeping these tight reduces input token costs (the dominant API expense).
const (
	// MaxCommitsPerRepo caps how many commits are listed per repo in the
	// tier 1 git summarizer prompt. Beyond this, a "+N more" indicator is shown.
	MaxCommitsPerRepo = 15

	// MaxBusMessages caps how many bus messages are included in the tier 1
	// bus summarizer prompt. Messages are already sorted newest-first, so
	// older messages are dropped.
	MaxBusMessages = 20

	// MaxTrackedItemsForTier2 caps how many tracked items are included
	// verbatim in the tier 2 prompt. Beyond this, a count is shown.
	MaxTrackedItemsForTier2 = 20

	// MaxCompletedItemsForTier2 caps how many completed items are shown.
	MaxCompletedItemsForTier2 = 10

	// MaxConcernsForTier2 caps concerns included in tier 2 context.
	MaxConcernsForTier2 = 15
)

// truncateSlice returns at most max elements from s, plus a message
// indicating how many were omitted if truncation occurred.
// Returns the truncated slice and an optional overflow message.
func truncateSlice[T any](s []T, max int) ([]T, string) {
	if len(s) <= max {
		return s, ""
	}
	overflow := len(s) - max
	return s[:max], fmt.Sprintf("... and %d more (truncated for cost)", overflow)
}

// estimateTokens returns a rough token count estimate using the ~4 chars/token heuristic.
// This is intentionally approximate — used for logging, not billing.
func estimateTokens(s string) int {
	return (len(s) + 3) / 4 // ceiling division
}
