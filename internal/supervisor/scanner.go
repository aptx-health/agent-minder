package supervisor

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// LiveStatus holds real-time agent status parsed from stream-json output.
type LiveStatus struct {
	CurrentTool string // e.g. "Bash", "Read", "Edit"
	ToolInput   string // truncated summary of tool input
	StepCount   int    // incremented on each assistant message
}

// Stream-json event types (unexported, used only by scanner).

type streamEvent struct {
	Type        string     `json:"type"`
	Subtype     string     `json:"subtype,omitempty"`
	Message     *streamMsg `json:"message,omitempty"`
	NumTurns    int        `json:"num_turns,omitempty"`
	TotalCost   float64    `json:"total_cost_usd,omitempty"`
	Duration    int        `json:"duration_ms,omitempty"`
	IsError     bool       `json:"is_error,omitempty"`
	Error       string     `json:"error,omitempty"`        // system/api_retry error category
	ErrorStatus int        `json:"error_status,omitempty"` // HTTP status (429 for rate limit)
}

type streamMsg struct {
	Model   string        `json:"model"`
	Content []streamBlock `json:"content"`
}

type streamBlock struct {
	Type  string          `json:"type"` // "tool_use" or "text"
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	Text  string          `json:"text,omitempty"`
}

// scanStream reads stream-json lines from r, writes each line to logFile,
// and updates the live status for the given job on the supervisor.
// It exits when r reaches EOF (agent process exits).
func scanStream(r io.Reader, logFile *os.File, jobID int64, s *Supervisor) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024) // 1MB max line

	for scanner.Scan() {
		line := scanner.Bytes()

		// Write raw line to log (lossless).
		_, _ = logFile.Write(line)
		_, _ = logFile.Write([]byte("\n"))

		// Parse for live status updates.
		var evt streamEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}

		s.mu.Lock()
		rs, ok := s.running[jobID]
		if ok {
			switch evt.Type {
			case "assistant":
				if evt.Message != nil {
					rs.liveStatus.StepCount++

					for _, block := range evt.Message.Content {
						if block.Type == "tool_use" {
							rs.liveStatus.CurrentTool = block.Name
							rs.liveStatus.ToolInput = extractToolInput(block.Input, 80)
						}
					}

					hasToolUse := false
					for _, block := range evt.Message.Content {
						if block.Type == "tool_use" {
							hasToolUse = true
							break
						}
					}
					if !hasToolUse {
						rs.liveStatus.CurrentTool = ""
						rs.liveStatus.ToolInput = ""
					}
				}

			case "result":
				rs.liveStatus.CurrentTool = ""
				rs.liveStatus.ToolInput = ""

			case "system":
				// Detect usage limit from api_retry events.
				if evt.Subtype == "api_retry" &&
					(evt.Error == "rate_limit" || evt.Error == "billing_error") {
					rs.hitUsageLimit = true
				}
			}
		}
		s.mu.Unlock()
	}
}

// extractToolInput extracts a displayable summary from raw JSON tool input.
func extractToolInput(raw json.RawMessage, maxLen int) string {
	if len(raw) == 0 {
		return ""
	}

	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		s := string(raw)
		if len(s) > maxLen {
			return s[:maxLen-3] + "..."
		}
		return s
	}

	for _, key := range []string{"command", "file_path", "pattern", "prompt", "query", "description"} {
		if val, ok := obj[key].(string); ok && val != "" {
			if len(val) > maxLen {
				return val[:maxLen-3] + "..."
			}
			return val
		}
	}

	s := string(raw)
	if len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}
