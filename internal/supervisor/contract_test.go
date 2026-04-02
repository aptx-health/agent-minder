package supervisor

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExtractFrontmatter(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "basic frontmatter",
			input: "---\nname: autopilot\n---\nBody text",
			want:  "name: autopilot",
		},
		{
			name:  "multiline frontmatter",
			input: "---\nname: autopilot\ndescription: test agent\nmode: reactive\n---\nBody",
			want:  "name: autopilot\ndescription: test agent\nmode: reactive",
		},
		{
			name:    "no frontmatter",
			input:   "Just some text",
			wantErr: true,
		},
		{
			name:    "no closing marker",
			input:   "---\nname: test",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractFrontmatter(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseContractFromBytes(t *testing.T) {
	t.Run("minimal frontmatter defaults", func(t *testing.T) {
		input := []byte("---\nname: autopilot\n---\nBody")
		c, err := ParseContractFromBytes(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.Name != "autopilot" {
			t.Errorf("name = %q, want autopilot", c.Name)
		}
		if c.Mode != "reactive" {
			t.Errorf("mode = %q, want reactive", c.Mode)
		}
		if c.Output != "pr" {
			t.Errorf("output = %q, want pr", c.Output)
		}
		if c.BranchPrefix != "agent/issue" {
			t.Errorf("branch_prefix = %q, want agent/issue", c.BranchPrefix)
		}
		if c.Timeout != "2h" {
			t.Errorf("timeout = %q, want 2h", c.Timeout)
		}
	})

	t.Run("full contract", func(t *testing.T) {
		input := []byte(`---
name: dependency-updater
description: Scans for outdated dependencies
mode: proactive
output: pr
branch_prefix: agent/deps
timeout: 1h
dedup:
  - branch_exists
  - open_pr_with_label:deps
stages:
  - name: scan
    timeout: 30m
    on_failure: bail
  - name: review
    agent: reviewer
    timeout: 15m
    on_failure: skip
    retries: 1
---
You are a dependency update agent.`)

		c, err := ParseContractFromBytes(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.Name != "dependency-updater" {
			t.Errorf("name = %q", c.Name)
		}
		if c.Mode != "proactive" {
			t.Errorf("mode = %q, want proactive", c.Mode)
		}
		if c.IsReactive() {
			t.Error("proactive agent should not be reactive")
		}
		if c.Output != "pr" {
			t.Errorf("output = %q", c.Output)
		}
		if c.BranchPrefix != "agent/deps" {
			t.Errorf("branch_prefix = %q", c.BranchPrefix)
		}
		if len(c.Dedup) != 2 {
			t.Errorf("dedup len = %d, want 2", len(c.Dedup))
		}
		if c.Dedup[0] != "branch_exists" {
			t.Errorf("dedup[0] = %q", c.Dedup[0])
		}
		if c.Dedup[1] != "open_pr_with_label:deps" {
			t.Errorf("dedup[1] = %q", c.Dedup[1])
		}
		if len(c.Stages) != 2 {
			t.Fatalf("stages len = %d, want 2", len(c.Stages))
		}
		if c.Stages[0].Name != "scan" {
			t.Errorf("stage[0].name = %q", c.Stages[0].Name)
		}
		if c.Stages[0].Agent != "dependency-updater" {
			t.Errorf("stage[0].agent = %q, want dependency-updater (default to parent)", c.Stages[0].Agent)
		}
		if c.Stages[1].Agent != "reviewer" {
			t.Errorf("stage[1].agent = %q, want reviewer", c.Stages[1].Agent)
		}
		if c.Stages[1].OnFailure != "skip" {
			t.Errorf("stage[1].on_failure = %q, want skip", c.Stages[1].OnFailure)
		}
		if c.Stages[1].Retries != 1 {
			t.Errorf("stage[1].retries = %d, want 1", c.Stages[1].Retries)
		}
	})

	t.Run("built-in autopilot def", func(t *testing.T) {
		c, err := ParseContractFromBytes([]byte(defaultAgentDef))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.Name != "autopilot" {
			t.Errorf("name = %q", c.Name)
		}
		// Built-in doesn't have contract fields — should get defaults.
		if c.Mode != "reactive" {
			t.Errorf("mode = %q, want reactive", c.Mode)
		}
	})

	t.Run("built-in reviewer def", func(t *testing.T) {
		c, err := ParseContractFromBytes([]byte(defaultReviewerDef))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.Name != "reviewer" {
			t.Errorf("name = %q", c.Name)
		}
	})
}

func TestTimeoutDuration(t *testing.T) {
	c := &AgentContract{Timeout: "1h30m"}
	if d := c.TimeoutDuration(); d != 90*time.Minute {
		t.Errorf("timeout = %v, want 1h30m", d)
	}

	c2 := &AgentContract{Timeout: ""}
	if d := c2.TimeoutDuration(); d != 2*time.Hour {
		t.Errorf("empty timeout = %v, want 2h", d)
	}

	c3 := &AgentContract{Timeout: "invalid"}
	if d := c3.TimeoutDuration(); d != 2*time.Hour {
		t.Errorf("invalid timeout = %v, want 2h default", d)
	}

	s := &StageContract{Timeout: "10m"}
	if d := s.TimeoutDuration(); d != 10*time.Minute {
		t.Errorf("stage timeout = %v, want 10m", d)
	}
}

func TestDefaultContract(t *testing.T) {
	c := DefaultContract("my-agent")
	if c.Name != "my-agent" {
		t.Errorf("name = %q", c.Name)
	}
	if c.Mode != "reactive" {
		t.Errorf("mode = %q", c.Mode)
	}
	if !c.IsReactive() {
		t.Error("should be reactive")
	}
	if len(c.Stages) != 1 {
		t.Errorf("stages = %d, want 1", len(c.Stages))
	}
}

func TestParseContractFromFile(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, ".claude", "agents")
	_ = os.MkdirAll(agentDir, 0755)

	content := `---
name: test-agent
mode: proactive
output: issue
dedup:
  - recent_run:24
timeout: 1h
---
You are a test agent.`

	path := filepath.Join(agentDir, "test-agent.md")
	_ = os.WriteFile(path, []byte(content), 0644)

	c, err := ParseContract(path)
	if err != nil {
		t.Fatalf("ParseContract: %v", err)
	}
	if c.Name != "test-agent" {
		t.Errorf("name = %q", c.Name)
	}
	if c.Mode != "proactive" {
		t.Errorf("mode = %q", c.Mode)
	}
	if c.Output != "issue" {
		t.Errorf("output = %q", c.Output)
	}
	if len(c.Dedup) != 1 || c.Dedup[0] != "recent_run:24" {
		t.Errorf("dedup = %v", c.Dedup)
	}
}

func TestResolveContract(t *testing.T) {
	// With a non-existent repo dir, should fall back to built-in.
	c, err := ResolveContract("/nonexistent", "autopilot")
	if err != nil {
		t.Fatalf("ResolveContract: %v", err)
	}
	if c.Name != "autopilot" {
		t.Errorf("name = %q", c.Name)
	}

	// Unknown agent gets default contract.
	c2, err := ResolveContract("/nonexistent", "unknown-agent")
	if err != nil {
		t.Fatalf("ResolveContract unknown: %v", err)
	}
	if c2.Name != "unknown-agent" {
		t.Errorf("name = %q", c2.Name)
	}
	if c2.Mode != "reactive" {
		t.Errorf("mode = %q", c2.Mode)
	}

	// Repo-level override.
	dir := t.TempDir()
	agentDir := filepath.Join(dir, ".claude", "agents")
	_ = os.MkdirAll(agentDir, 0755)
	_ = os.WriteFile(filepath.Join(agentDir, "autopilot.md"), []byte(`---
name: autopilot
mode: proactive
output: report
timeout: 30m
---
Custom autopilot.`), 0644)

	c3, err := ResolveContract(dir, "autopilot")
	if err != nil {
		t.Fatalf("ResolveContract repo: %v", err)
	}
	if c3.Mode != "proactive" {
		t.Errorf("mode = %q, want proactive (repo override)", c3.Mode)
	}
	if c3.Output != "report" {
		t.Errorf("output = %q, want report", c3.Output)
	}
}
