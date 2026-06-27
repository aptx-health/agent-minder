package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

type codexRunState struct {
	SessionID string
	Model     string
	NumTurns  int
	FinalText string
	InputTok  int
	CachedTok int
	OutputTok int
	IsError   bool
	ErrorText string
	StopText  string
	Native    json.RawMessage
	Unknown   []string
}

type codexEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id,omitempty"`
	Model    string          `json:"model,omitempty"`
	Item     *codexItem      `json:"item,omitempty"`
	Usage    *codexUsage     `json:"usage,omitempty"`
	Error    json.RawMessage `json:"error,omitempty"`
	Message  string          `json:"message,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Seq      any             `json:"seq,omitempty"`
}

type codexItem struct {
	Type      string          `json:"type"`
	Name      string          `json:"name,omitempty"`
	Text      string          `json:"text,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Command   string          `json:"command,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Error     json.RawMessage `json:"error,omitempty"`
}

type codexUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

// scanStream reads Codex JSONL lines from r, mirrors each line to logFile, and
// pushes normalized events to sink. It also accumulates the lightweight state
// needed for later Result synthesis.
func scanStream(r io.Reader, logFile io.Writer, sink runtime.EventSink, state *codexRunState, limits runtime.Limits, cancel context.CancelFunc) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		mirrorLine(logFile, line)

		var evt codexEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}
		if state != nil {
			state.apply(evt, line)
		}
		emitEvent(sink, evt)

		if evt.Type == "turn.completed" && state != nil && limits.MaxTurns > 0 && state.NumTurns >= limits.MaxTurns && cancel != nil {
			cancel()
		}
	}
}

func mirrorLine(logFile io.Writer, line []byte) {
	if logFile == nil {
		return
	}
	_, _ = logFile.Write(line)
	_, _ = logFile.Write([]byte("\n"))
}

func (s *codexRunState) apply(evt codexEvent, raw []byte) {
	switch evt.Type {
	case "thread.started":
		if evt.ThreadID != "" {
			s.SessionID = evt.ThreadID
		}
		if evt.Model != "" {
			s.Model = evt.Model
		}
	case "session_meta", "turn_context":
		if evt.ThreadID != "" {
			s.SessionID = evt.ThreadID
		}
		if evt.Model != "" {
			s.Model = evt.Model
		}
	case "turn.completed":
		s.NumTurns++
		s.StopText = "turn.completed"
		s.Native = append(s.Native[:0], raw...)
		if evt.Usage != nil {
			s.InputTok += evt.Usage.InputTokens
			s.CachedTok += evt.Usage.CachedInputTokens
			s.OutputTok += evt.Usage.OutputTokens
		}
	case "turn.failed":
		s.IsError = true
		s.ErrorText = eventErrorText(evt)
		s.StopText = "turn.failed: " + s.ErrorText
		s.Native = append(s.Native[:0], raw...)
	case "item.completed":
		if evt.Item == nil {
			return
		}
		switch evt.Item.Type {
		case "agent_message":
			if evt.Item.Text != "" {
				s.FinalText = evt.Item.Text
			}
		case "error":
			s.IsError = true
			s.ErrorText = itemErrorText(evt.Item)
		}
	case "error":
		s.IsError = true
		s.ErrorText = eventErrorText(evt)
		s.StopText = "error: " + s.ErrorText
		s.Native = append(s.Native[:0], raw...)
	case "event_msg":
		if usage := usageFromPayload(evt.Payload); usage != nil {
			s.InputTok += usage.InputTokens
			s.CachedTok += usage.CachedInputTokens
			s.OutputTok += usage.OutputTokens
		}
	default:
		if evt.Type != "" {
			s.Unknown = append(s.Unknown, fmt.Sprintf("%s seq=%v", evt.Type, evt.Seq))
		}
	}
}

func emitEvent(sink runtime.EventSink, evt codexEvent) {
	if sink == nil {
		return
	}

	switch evt.Type {
	case "turn.completed":
		sink.OnAssistantStep()
	case "turn.failed", "error":
		if looksLikeUsageLimit(eventErrorText(evt)) {
			sink.OnUsageLimit()
		}
	case "item.started":
		if evt.Item == nil {
			return
		}
		switch evt.Item.Type {
		case "command_execution", "file_change", "mcp_tool_call":
			sink.OnToolStart(toolName(evt.Item), toolInputSummary(evt.Item, 80))
		}
	case "item.completed":
		if evt.Item == nil {
			return
		}
		switch evt.Item.Type {
		case "agent_message":
			sink.OnToolEnd()
		case "error":
			if looksLikeUsageLimit(itemErrorText(evt.Item)) {
				sink.OnUsageLimit()
			}
		}
	case "response_item":
		if evt.Item == nil {
			return
		}
		switch evt.Item.Type {
		case "function_call", "custom_tool_call":
			sink.OnToolStart(toolName(evt.Item), toolInputSummary(evt.Item, 80))
		case "function_call_output", "custom_tool_call_output":
			sink.OnToolEnd()
		}
	}
}

func usageFromPayload(raw json.RawMessage) *codexUsage {
	if len(raw) == 0 {
		return nil
	}
	var payload struct {
		Type  string      `json:"type"`
		Usage *codexUsage `json:"usage"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	if payload.Type != "token_count" {
		return nil
	}
	return payload.Usage
}

func toolName(item *codexItem) string {
	if item.Name != "" {
		return item.Name
	}
	return item.Type
}

func toolInputSummary(item *codexItem, maxLen int) string {
	for _, raw := range []json.RawMessage{item.Input, item.Arguments} {
		if summary := rawInputSummary(raw, maxLen); summary != "" {
			return summary
		}
	}
	if item.Command != "" {
		return truncate(item.Command, maxLen)
	}
	if item.CallID != "" {
		return truncate(item.CallID, maxLen)
	}
	return ""
}

func rawInputSummary(raw json.RawMessage, maxLen int) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		for _, key := range []string{"command", "file_path", "pattern", "prompt", "query", "description"} {
			if val, ok := obj[key].(string); ok && val != "" {
				return truncate(val, maxLen)
			}
		}
	}
	return truncate(string(raw), maxLen)
}

func eventErrorText(evt codexEvent) string {
	if evt.Message != "" {
		return evt.Message
	}
	if len(evt.Error) == 0 {
		return ""
	}
	var obj struct {
		Message string `json:"message"`
		Code    string `json:"code"`
		Type    string `json:"type"`
	}
	if json.Unmarshal(evt.Error, &obj) == nil {
		switch {
		case obj.Message != "":
			return obj.Message
		case obj.Code != "":
			return obj.Code
		case obj.Type != "":
			return obj.Type
		}
	}
	var s string
	if json.Unmarshal(evt.Error, &s) == nil {
		return s
	}
	return string(evt.Error)
}

func itemErrorText(item *codexItem) string {
	if item.Text != "" {
		return item.Text
	}
	if len(item.Error) == 0 {
		return ""
	}
	return string(item.Error)
}

func looksLikeUsageLimit(s string) bool {
	s = strings.ToLower(s)
	for _, marker := range []string{"rate limit", "rate_limit", "quota", "billing", "usage limit", "usage_limit", "too many requests"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
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
