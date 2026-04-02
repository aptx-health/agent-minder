package scheduler

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfig(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		yaml := []byte(`
jobs:
  weekly-deps:
    schedule: "0 9 * * 1"
    agent: dependency-updater
    description: "Check for outdated dependencies"
    budget: 3.0

  nightly-security:
    schedule: "0 6 * * *"
    agent: security-scanner

  bug-triage:
    trigger: "label:bug"
    agent: autopilot
    description: "Pick up and fix labeled bugs"
`)
		cfg, err := ParseConfig(yaml)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(cfg.Jobs) != 3 {
			t.Fatalf("got %d jobs, want 3", len(cfg.Jobs))
		}

		deps := cfg.Jobs["weekly-deps"]
		if deps.Agent != "dependency-updater" {
			t.Errorf("agent = %q", deps.Agent)
		}
		if deps.Budget != 3.0 {
			t.Errorf("budget = %f", deps.Budget)
		}
		if !deps.IsScheduled() {
			t.Error("expected scheduled")
		}
		if deps.IsTrigger() {
			t.Error("should not be trigger")
		}

		cron, err := deps.ParsedSchedule()
		if err != nil {
			t.Fatalf("ParsedSchedule: %v", err)
		}
		if cron == nil {
			t.Fatal("expected non-nil cron")
		}
		if cron.String() != "0 9 * * 1" {
			t.Errorf("cron = %q", cron.String())
		}

		bug := cfg.Jobs["bug-triage"]
		if !bug.IsTrigger() {
			t.Error("expected trigger")
		}
		if bug.IsScheduled() {
			t.Error("should not be scheduled")
		}
	})

	t.Run("no jobs", func(t *testing.T) {
		_, err := ParseConfig([]byte("jobs:\n"))
		if err == nil {
			t.Error("expected error for empty jobs")
		}
	})

	t.Run("missing agent", func(t *testing.T) {
		_, err := ParseConfig([]byte(`
jobs:
  test:
    schedule: "* * * * *"
`))
		if err == nil {
			t.Error("expected error for missing agent")
		}
	})

	t.Run("no schedule or trigger", func(t *testing.T) {
		_, err := ParseConfig([]byte(`
jobs:
  test:
    agent: autopilot
`))
		if err == nil {
			t.Error("expected error for no schedule/trigger")
		}
	})

	t.Run("both schedule and trigger", func(t *testing.T) {
		_, err := ParseConfig([]byte(`
jobs:
  test:
    agent: autopilot
    schedule: "* * * * *"
    trigger: "label:bug"
`))
		if err == nil {
			t.Error("expected error for both schedule and trigger")
		}
	})

	t.Run("bad cron", func(t *testing.T) {
		_, err := ParseConfig([]byte(`
jobs:
  test:
    agent: autopilot
    schedule: "bad cron"
`))
		if err == nil {
			t.Error("expected error for bad cron")
		}
	})

	t.Run("bad trigger format", func(t *testing.T) {
		_, err := ParseConfig([]byte(`
jobs:
  test:
    agent: autopilot
    trigger: "invalid"
`))
		if err == nil {
			t.Error("expected error for bad trigger")
		}
	})

	t.Run("bad trigger type", func(t *testing.T) {
		_, err := ParseConfig([]byte(`
jobs:
  test:
    agent: autopilot
    trigger: "unknown:value"
`))
		if err == nil {
			t.Error("expected error for unknown trigger type")
		}
	})

	t.Run("with max_turns", func(t *testing.T) {
		cfg, err := ParseConfig([]byte(`
jobs:
  test:
    agent: autopilot
    schedule: "0 * * * *"
    max_turns: 25
    budget: 2.5
`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		j := cfg.Jobs["test"]
		if j.MaxTurns != 25 {
			t.Errorf("max_turns = %d, want 25", j.MaxTurns)
		}
		if j.Budget != 2.5 {
			t.Errorf("budget = %f, want 2.5", j.Budget)
		}
	})
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, ".agent-minder")
	_ = os.MkdirAll(agentDir, 0755)

	content := `
jobs:
  test-job:
    schedule: "0 9 * * *"
    agent: autopilot
    description: "Test job"
`
	path := filepath.Join(agentDir, "jobs.yaml")
	_ = os.WriteFile(path, []byte(content), 0644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Jobs) != 1 {
		t.Errorf("got %d jobs, want 1", len(cfg.Jobs))
	}

	// Non-existent file.
	_, err = LoadConfig("/nonexistent/jobs.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestConfigPath(t *testing.T) {
	got := ConfigPath("/home/user/repo")
	want := "/home/user/repo/.agent-minder/jobs.yaml"
	if got != want {
		t.Errorf("ConfigPath = %q, want %q", got, want)
	}
}
