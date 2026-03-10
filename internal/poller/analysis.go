package poller

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dustinlange/agent-minder/internal/db"
)

// AnalysisResponse is the structured JSON output from the tier 2 (analyzer) LLM.
type AnalysisResponse struct {
	Analysis   string            `json:"analysis"`
	Concerns   []AnalysisConcern `json:"concerns,omitempty"`
	BusMessage *BusMessage       `json:"bus_message,omitempty"`
}

// AnalysisConcern is a concern identified by the analyzer.
type AnalysisConcern struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// BusMessage is an optional message the analyzer wants to publish to the bus.
type BusMessage struct {
	Topic   string `json:"topic"`
	Message string `json:"message"`
}

// parseAnalysis attempts to parse a tier 2 LLM response as structured JSON.
// It handles raw JSON, JSON wrapped in markdown code fences, and gracefully
// falls back to treating the entire response as plain analysis text.
func parseAnalysis(raw string) *AnalysisResponse {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &AnalysisResponse{Analysis: ""}
	}

	// Try to extract JSON from markdown code fences.
	cleaned := raw
	if idx := strings.Index(raw, "```json"); idx >= 0 {
		start := idx + len("```json")
		if end := strings.Index(raw[start:], "```"); end >= 0 {
			cleaned = strings.TrimSpace(raw[start : start+end])
		}
	} else if idx := strings.Index(raw, "```"); idx >= 0 {
		start := idx + len("```")
		if end := strings.Index(raw[start:], "```"); end >= 0 {
			cleaned = strings.TrimSpace(raw[start : start+end])
		}
	}

	var resp AnalysisResponse
	if err := json.Unmarshal([]byte(cleaned), &resp); err == nil && resp.Analysis != "" {
		return &resp
	}

	// Try raw string as JSON directly (no fences).
	if cleaned != raw {
		if err := json.Unmarshal([]byte(raw), &resp); err == nil && resp.Analysis != "" {
			return &resp
		}
	}

	// Fallback: treat entire response as analysis text.
	return &AnalysisResponse{Analysis: raw}
}

// parseJSON is a helper that unmarshals JSON into the given target.
func parseJSON(raw string, target interface{}) error {
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return fmt.Errorf("parse JSON: %w", err)
	}
	return nil
}

// isDuplicateConcern checks whether a new concern message is semantically
// similar to any existing active concern. Uses keyword overlap — if 50%+
// of significant words (4+ chars) in the new message match an existing one,
// it's considered a duplicate.
func isDuplicateConcern(newMsg string, active []db.Concern) bool {
	newWords := significantWords(newMsg)
	if len(newWords) == 0 {
		return false
	}
	for _, existing := range active {
		existingWords := significantWords(existing.Message)
		overlap := 0
		for w := range newWords {
			if existingWords[w] {
				overlap++
			}
		}
		if float64(overlap)/float64(len(newWords)) >= 0.5 {
			return true
		}
	}
	return false
}

// significantWords extracts lowercase words of 4+ characters from text.
func significantWords(text string) map[string]bool {
	words := make(map[string]bool)
	for _, w := range strings.Fields(strings.ToLower(text)) {
		// Strip common punctuation.
		w = strings.Trim(w, ".,;:!?\"'()-[]{}/*")
		if len(w) >= 4 {
			words[w] = true
		}
	}
	return words
}
