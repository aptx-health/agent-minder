package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

// sseEvent is a light view of an opencode `GET /event` frame. We parse only the
// fields needed to drive the EventSink; the full raw line is mirrored to the log
// for forensics and never lossily re-encoded.
type sseEvent struct {
	Type       string `json:"type"`
	Properties struct {
		SessionID string          `json:"sessionID"`
		Part      *ssePart        `json:"part"`
		Info      json.RawMessage `json:"info"`
		Error     json.RawMessage `json:"error"`
	} `json:"properties"`
}

type ssePart struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	Tool   string `json:"tool"`
	CallID string `json:"callID"`
	State  struct {
		Status string          `json:"status"`
		Title  string          `json:"title"`
		Input  json.RawMessage `json:"input"`
	} `json:"state"`
}

// streamEvents opens the server event stream and drives sink until ctx is
// cancelled or the stream closes. Only events for sessionID are acted on; every
// event line is mirrored to logFile. It returns when the stream ends.
func streamEvents(ctx context.Context, baseURL, sessionID string, sink runtime.EventSink, logFile io.Writer) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/event", nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()

	tr := &translator{sink: sink, tools: map[string]bool{}}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		data, ok := bytesCutSSEData(line)
		if !ok {
			continue
		}
		mirror(logFile, data)

		var evt sseEvent
		if err := json.Unmarshal(data, &evt); err != nil {
			continue
		}
		if evt.Properties.SessionID != "" && evt.Properties.SessionID != sessionID {
			continue
		}
		tr.handle(evt)
	}
}

// bytesCutSSEData extracts the JSON payload from a `data: {...}` SSE line.
// Non-data lines (blank separators, comments) return ok=false.
func bytesCutSSEData(line []byte) ([]byte, bool) {
	s := strings.TrimRight(string(line), "\r")
	rest, ok := strings.CutPrefix(s, "data:")
	if !ok {
		return nil, false
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return nil, false
	}
	return []byte(rest), true
}

// translator maps opencode events to EventSink calls, deduping tool lifecycle by
// call id so each tool invocation yields one OnToolStart / OnToolEnd pair.
type translator struct {
	sink  runtime.EventSink
	mu    sync.Mutex
	tools map[string]bool // callID -> OnToolStart already fired
}

func (t *translator) handle(evt sseEvent) {
	if t.sink == nil {
		return
	}
	switch evt.Type {
	case "message.part.updated":
		t.handlePart(evt.Properties.Part)
	case "message.updated":
		if looksLikeUsageLimit(string(evt.Properties.Info)) {
			t.sink.OnUsageLimit()
		}
	case "session.error":
		if looksLikeUsageLimit(string(evt.Properties.Error)) {
			t.sink.OnUsageLimit()
		}
	}
}

func (t *translator) handlePart(p *ssePart) {
	if p == nil {
		return
	}
	switch p.Type {
	case "step-start":
		t.sink.OnAssistantStep()
	case "step-finish":
		t.sink.OnToolEnd()
	case "tool":
		t.handleTool(p)
	}
}

func (t *translator) handleTool(p *ssePart) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch p.State.Status {
	case "completed", "error":
		if t.tools[p.CallID] {
			delete(t.tools, p.CallID)
		}
		t.sink.OnToolEnd()
	default: // pending, running
		if p.CallID != "" && t.tools[p.CallID] {
			return
		}
		if p.CallID != "" {
			t.tools[p.CallID] = true
		}
		t.sink.OnToolStart(toolLabel(p), toolSummary(p, 80))
	}
}

func toolLabel(p *ssePart) string {
	if p.Tool != "" {
		return p.Tool
	}
	return "tool"
}

func toolSummary(p *ssePart, maxLen int) string {
	if p.State.Title != "" {
		return truncate(p.State.Title, maxLen)
	}
	if len(p.State.Input) > 0 {
		return truncate(string(p.State.Input), maxLen)
	}
	return ""
}

func mirror(w io.Writer, data []byte) {
	if w == nil {
		return
	}
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n"))
}
