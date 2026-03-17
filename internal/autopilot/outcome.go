package autopilot

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// AgentResult holds parsed fields from the stream-json result event.
type AgentResult struct {
	SubType           string            `json:"subtype"`
	IsError           bool              `json:"is_error"`
	NumTurns          int               `json:"num_turns"`
	TotalCost         float64           `json:"total_cost_usd"`
	StopReason        json.RawMessage   `json:"stop_reason"`
	Result            string            `json:"result"`
	PermissionDenials []json.RawMessage `json:"permission_denials"`
	SessionID         string            `json:"session_id"`
}

// resultEvent is the full stream-json event; we only care about type=="result".
type resultEvent struct {
	Type string `json:"type"`
	AgentResult
}

// parseAgentLog reads the agent log file and extracts the result event.
// Returns nil if the log is missing, empty, or contains no result event.
func parseAgentLog(logPath string) (*AgentResult, error) {
	if logPath == "" {
		return nil, nil
	}

	f, err := os.Open(logPath)
	if err != nil {
		return nil, fmt.Errorf("open agent log: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024) // 1MB max line

	for scanner.Scan() {
		line := scanner.Bytes()

		var evt resultEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}

		if evt.Type == "result" {
			result := evt.AgentResult
			return &result, nil
		}
	}

	return nil, nil // no result event found
}

// classifyOutcome determines failure reason from the result event + config.
// Returns (status, failureReason, failureDetail).
// Empty status means no failure detected — caller should continue with PR check.
func classifyOutcome(result *AgentResult, maxTurns int, maxBudget float64) (string, string, string) {
	if result == nil {
		return "", "", ""
	}

	// 1. Permission denials (non-empty array).
	if len(result.PermissionDenials) > 0 {
		detail, _ := json.Marshal(result.PermissionDenials)
		return "failed", "permissions", string(detail)
	}

	// 2. Turn limit exhaustion.
	if maxTurns > 0 && result.NumTurns >= maxTurns {
		return "failed", "max_turns", fmt.Sprintf("used %d of %d turns", result.NumTurns, maxTurns)
	}

	// 3. Budget exhaustion (>= 95% of limit).
	if maxBudget > 0 && result.TotalCost >= maxBudget*0.95 {
		return "failed", "max_budget", fmt.Sprintf("spent $%.2f of $%.2f budget", result.TotalCost, maxBudget)
	}

	// 4. Explicit error flag.
	if result.IsError {
		detail := result.Result
		if len(detail) > 500 {
			detail = detail[:500] + "..."
		}
		return "failed", "error", detail
	}

	// No failure detected.
	return "", "", ""
}
