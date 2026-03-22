package poller

import (
	"encoding/json"
	"fmt"
	"strings"
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
// It strips markdown code fences before parsing.
func parseJSON(raw string, target interface{}) error {
	cleaned := strings.TrimSpace(raw)

	// Strip markdown code fences.
	if idx := strings.Index(cleaned, "```json"); idx >= 0 {
		start := idx + len("```json")
		if end := strings.Index(cleaned[start:], "```"); end >= 0 {
			cleaned = strings.TrimSpace(cleaned[start : start+end])
		}
	} else if idx := strings.Index(cleaned, "```"); idx >= 0 {
		start := idx + len("```")
		if end := strings.Index(cleaned[start:], "```"); end >= 0 {
			cleaned = strings.TrimSpace(cleaned[start : start+end])
		}
	}

	if err := json.Unmarshal([]byte(cleaned), target); err != nil {
		return fmt.Errorf("parse JSON: %w", err)
	}
	return nil
}

// validSeverity normalizes a severity string to one of the three valid levels.
func validSeverity(s string) string {
	switch strings.ToLower(s) {
	case "warning", "warn":
		return "warning"
	case "danger", "critical":
		return "danger"
	default:
		return "info"
	}
}
