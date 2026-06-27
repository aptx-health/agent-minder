package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestStreamLogFormatsCodexEvents(t *testing.T) {
	log := strings.Join([]string{
		"Reading additional input from stdin...",
		`{"type":"thread.started","thread_id":"session-123"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"I am checking the repo."}}`,
		`{"type":"item.started","item":{"type":"command_execution","command":"/bin/zsh -lc pwd","aggregated_output":"","exit_code":null,"status":"in_progress"}}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"/bin/zsh -lc pwd","aggregated_output":"/tmp/worktree\n","exit_code":0,"status":"completed"}}`,
		`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"/tmp/worktree/internal/model/article.go","kind":"add"}],"status":"completed"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":3,"reasoning_output_tokens":2}}`,
	}, "\n")

	var out bytes.Buffer
	if err := streamLogTo(&out, strings.NewReader(log), false); err != nil {
		t.Fatalf("streamLogTo() error = %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"Reading additional input from stdin...",
		"Session: session-123",
		"--- Turn started ---",
		"I am checking the repo.",
		"> /bin/zsh -lc pwd",
		"/tmp/worktree",
		"add",
		"/tmp/worktree/internal/model/article.go",
		"Tokens: input=10 cached=4 output=3 reasoning=2",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted log missing %q\noutput:\n%s", want, got)
		}
	}
}

func TestStreamLogStillFormatsClaudeEvents(t *testing.T) {
	log := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Checking now."},{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"result","result":"Done","num_turns":2,"total_cost_usd":0.25}`,
	}, "\n")

	var out bytes.Buffer
	if err := streamLogTo(&out, strings.NewReader(log), false); err != nil {
		t.Fatalf("streamLogTo() error = %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"Checking now.",
		"> Bash",
		"Done",
		"Turns: 2  Cost: $0.25",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted log missing %q\noutput:\n%s", want, got)
		}
	}
}
