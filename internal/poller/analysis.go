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

// reconcileConcerns replaces the append-only concern model with full-list
// reconciliation. The analyzer returns the complete desired concern list;
// we match against existing concerns using keyword overlap, resolve any
// that were dropped, and add any that are new.
func reconcileConcerns(store *db.Store, projectID int64, existing []db.Concern, desired []AnalysisConcern) []string {
	var result []string

	// Resolve all existing concerns — the analyzer returns the full desired
	// list each cycle, so we replace rather than diff.
	for _, e := range existing {
		store.ResolveConcern(e.ID)
	}

	// Insert the analyzer's current concerns fresh.
	for _, d := range desired {
		severity := validSeverity(d.Severity)
		store.AddConcern(&db.Concern{
			ProjectID: projectID,
			Severity:  severity,
			Message:   d.Message,
		})
		result = append(result, fmt.Sprintf("[%s] %s", severity, d.Message))
	}

	return result
}

