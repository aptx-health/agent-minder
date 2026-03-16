package autopilot

import (
	"encoding/json"
	"io"
	"os"
	"testing"
)

// newTestSupervisor creates a minimal supervisor with N slots for testing.
func newTestSupervisor(n int) *Supervisor {
	return &Supervisor{
		slots: make([]*slotState, n),
	}
}

func TestScanStream_AssistantToolUse(t *testing.T) {
	sup := newTestSupervisor(1)
	sup.slots[0] = &slotState{}

	events := `{"type":"system","subtype":"init","session_id":"abc"}
{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}
{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/foo/bar.go"}}]}}
{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Done!"}]}}
`

	pr, pw := io.Pipe()
	logFile, err := os.CreateTemp(t.TempDir(), "scan-test-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		scanStream(pr, logFile, 0, sup)
	}()

	_, _ = pw.Write([]byte(events))
	_ = pw.Close()
	<-done

	sup.mu.Lock()
	defer sup.mu.Unlock()

	if sup.slots[0].liveStatus.StepCount != 3 {
		t.Errorf("StepCount = %d, want 3", sup.slots[0].liveStatus.StepCount)
	}
	// After text-only message, tool should be cleared.
	if sup.slots[0].liveStatus.CurrentTool != "" {
		t.Errorf("CurrentTool = %q, want empty (cleared by text-only message)", sup.slots[0].liveStatus.CurrentTool)
	}

	// Verify log file has all lines.
	if _, err := logFile.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(logFile)
	lines := 0
	for _, b := range content {
		if b == '\n' {
			lines++
		}
	}
	if lines != 4 {
		t.Errorf("log file has %d lines, want 4", lines)
	}
}

func TestScanStream_ResultEvent(t *testing.T) {
	sup := newTestSupervisor(1)
	sup.slots[0] = &slotState{}

	events := `{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"x.go"}}]}}
{"type":"result","subtype":"success","num_turns":5,"total_cost_usd":0.42,"duration_ms":30000,"stop_reason":"end_turn"}
`

	pr, pw := io.Pipe()
	logFile, err := os.CreateTemp(t.TempDir(), "scan-test-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		scanStream(pr, logFile, 0, sup)
	}()

	_, _ = pw.Write([]byte(events))
	_ = pw.Close()
	<-done

	sup.mu.Lock()
	defer sup.mu.Unlock()

	// Tool should be cleared after result.
	if sup.slots[0].liveStatus.CurrentTool != "" {
		t.Errorf("CurrentTool = %q, want empty after result", sup.slots[0].liveStatus.CurrentTool)
	}
}

func TestScanStream_MalformedLines(t *testing.T) {
	sup := newTestSupervisor(1)
	sup.slots[0] = &slotState{}

	events := `not json at all
{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}
also not json {{{
`

	pr, pw := io.Pipe()
	logFile, err := os.CreateTemp(t.TempDir(), "scan-test-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		scanStream(pr, logFile, 0, sup)
	}()

	_, _ = pw.Write([]byte(events))
	_ = pw.Close()
	<-done

	sup.mu.Lock()
	defer sup.mu.Unlock()

	// Only the valid assistant event should be counted.
	if sup.slots[0].liveStatus.StepCount != 1 {
		t.Errorf("StepCount = %d, want 1", sup.slots[0].liveStatus.StepCount)
	}
	if sup.slots[0].liveStatus.CurrentTool != "Bash" {
		t.Errorf("CurrentTool = %q, want Bash", sup.slots[0].liveStatus.CurrentTool)
	}

	// All 3 lines should be in the log file.
	if _, err := logFile.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(logFile)
	lines := 0
	for _, b := range content {
		if b == '\n' {
			lines++
		}
	}
	if lines != 3 {
		t.Errorf("log file has %d lines, want 3", lines)
	}
}

func TestScanStream_NilSlot(t *testing.T) {
	// Ensure scanner doesn't panic if slot becomes nil mid-scan.
	sup := newTestSupervisor(1)
	// Slot is nil — scanner should just write to log without updating status.

	events := `{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}
`

	pr, pw := io.Pipe()
	logFile, err := os.CreateTemp(t.TempDir(), "scan-test-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		scanStream(pr, logFile, 0, sup)
	}()

	_, _ = pw.Write([]byte(events))
	_ = pw.Close()
	<-done
	// No panic = success.
}

func TestExtractToolInput(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "bash command",
			input:  `{"command":"go test ./internal/..."}`,
			maxLen: 80,
			want:   "go test ./internal/...",
		},
		{
			name:   "file path",
			input:  `{"file_path":"/Users/dustin/repos/agent-minder/internal/db/schema.go"}`,
			maxLen: 80,
			want:   "/Users/dustin/repos/agent-minder/internal/db/schema.go",
		},
		{
			name:   "pattern",
			input:  `{"pattern":"**/*.go"}`,
			maxLen: 80,
			want:   "**/*.go",
		},
		{
			name:   "long command truncated",
			input:  `{"command":"go test -v -count=1 -run TestVeryLongTestNameThatExceedsTheMaximumLength ./internal/autopilot/..."}`,
			maxLen: 40,
			want:   "go test -v -count=1 -run TestVeryLong...",
		},
		{
			name:   "empty input",
			input:  `{}`,
			maxLen: 80,
			want:   "{}",
		},
		{
			name:   "null input",
			input:  "",
			maxLen: 80,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToolInput(json.RawMessage(tt.input), tt.maxLen)
			if got != tt.want {
				t.Errorf("extractToolInput() = %q, want %q", got, tt.want)
			}
		})
	}
}
