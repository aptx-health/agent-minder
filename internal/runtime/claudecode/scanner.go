package claudecode

import (
	"bufio"
	"encoding/json"
	"io"
	"os"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

// claudeResultEvent is the Claude Code stream-json result event shape.
//
// This mirrors `internal/agentutil.AgentResult` but stays internal to the
// runtime so changes to Claude's wire format don't leak into supervisor code.
type claudeResultEvent struct {
	SubType           string            `json:"subtype"`
	IsError           bool              `json:"is_error"`
	NumTurns          int               `json:"num_turns"`
	TotalCost         float64           `json:"total_cost_usd"`
	StopReason        string            `json:"stop_reason"`
	Result            string            `json:"result"`
	PermissionDenials []json.RawMessage `json:"permission_denials"`
	SessionID         string            `json:"session_id"`
	Model             string            `json:"model"`
}

// streamEvent is the envelope for any stream-json event.
type streamEvent struct {
	Type        string     `json:"type"`
	Subtype     string     `json:"subtype,omitempty"`
	Message     *streamMsg `json:"message,omitempty"`
	NumTurns    int        `json:"num_turns,omitempty"`
	TotalCost   float64    `json:"total_cost_usd,omitempty"`
	IsError     bool       `json:"is_error,omitempty"`
	Error       string     `json:"error,omitempty"`
	ErrorStatus int        `json:"error_status,omitempty"`
}

type streamMsg struct {
	Content []streamBlock `json:"content"`
}

type streamBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	Text  string          `json:"text,omitempty"`
}

// scanStream reads stream-json lines from r, mirrors each line to logFile, and
// pushes normalized events to sink.
//
// Sink semantics (matching the supervisor's existing LiveStatus updates in
// `internal/supervisor/scanner.go:scanStream`):
//
//   - assistant message with at least one tool_use block → OnAssistantStep then
//     OnToolStart(name, inputSummary) for the (last) tool_use.
//   - assistant message with no tool_use blocks → OnAssistantStep then OnToolEnd.
//   - result event → OnToolEnd (clears any in-flight tool display).
//   - system api_retry with rate_limit / billing_error → OnUsageLimit.
//
// Returns when r reaches EOF.
func scanStream(r io.Reader, logFile io.Writer, sink runtime.EventSink) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()

		if logFile != nil {
			_, _ = logFile.Write(line)
			_, _ = logFile.Write([]byte("\n"))
		}

		if sink == nil {
			continue
		}

		var evt streamEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}

		switch evt.Type {
		case "assistant":
			if evt.Message == nil {
				continue
			}
			sink.OnAssistantStep()

			var lastTool *streamBlock
			for i := range evt.Message.Content {
				if evt.Message.Content[i].Type == "tool_use" {
					lastTool = &evt.Message.Content[i]
				}
			}
			if lastTool != nil {
				sink.OnToolStart(lastTool.Name, extractToolInput(lastTool.Input, 80))
			} else {
				sink.OnToolEnd()
			}

		case "result":
			sink.OnToolEnd()

		case "system":
			if evt.Subtype == "api_retry" &&
				(evt.Error == "rate_limit" || evt.Error == "billing_error") {
				sink.OnUsageLimit()
			}
		}
	}
}

// extractToolInput pulls a short, displayable summary from raw tool_use input
// JSON. Prefers common human-meaningful keys; falls back to the raw JSON.
func extractToolInput(raw json.RawMessage, maxLen int) string {
	if len(raw) == 0 {
		return ""
	}

	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return truncate(string(raw), maxLen)
	}

	for _, key := range []string{"command", "file_path", "pattern", "prompt", "query", "description"} {
		if val, ok := obj[key].(string); ok && val != "" {
			return truncate(val, maxLen)
		}
	}
	return truncate(string(raw), maxLen)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// readResultEvent walks a stream-json log line by line and returns the parsed
// result event plus the raw JSON line (for Result.Native).
//
// Returns (nil, nil, nil) if logPath is empty or no result event is found.
func readResultEvent(logPath string) (*claudeResultEvent, json.RawMessage, error) {
	if logPath == "" {
		return nil, nil, nil
	}
	f, err := os.Open(logPath)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		if env.Type != "result" {
			continue
		}
		var evt claudeResultEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}
		raw := make(json.RawMessage, len(line))
		copy(raw, line)
		return &evt, raw, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	return nil, nil, nil
}
