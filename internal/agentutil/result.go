package agentutil

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// AgentResult holds parsed fields from the Claude Code stream-json result event.
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

// ParseAgentLog reads a Claude Code stream-json log and extracts the result event.
// Returns nil (no error) if logPath is empty or contains no result event.
func ParseAgentLog(logPath string) (*AgentResult, error) {
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
		var evt resultEvent
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			continue
		}
		if evt.Type == "result" {
			r := evt.AgentResult
			return &r, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan log: %w", err)
	}

	return nil, nil
}
