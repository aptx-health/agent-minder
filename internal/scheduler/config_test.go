package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/aptx-health/agent-minder/internal/runtime/claudecode"
	_ "github.com/aptx-health/agent-minder/internal/runtime/codex"
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

	t.Run("with runtime", func(t *testing.T) {
		cfg, err := ParseConfig([]byte(`
jobs:
  test:
    agent: autopilot
    schedule: "0 * * * *"
    runtime: codex
    model: " gpt-5 "
`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := cfg.Jobs["test"].Runtime; got != "codex" {
			t.Errorf("runtime = %q, want codex", got)
		}
		if got := cfg.Jobs["test"].Model; got != "gpt-5" {
			t.Errorf("model = %q, want gpt-5", got)
		}
	})

	t.Run("bad runtime", func(t *testing.T) {
		_, err := ParseConfig([]byte(`
jobs:
  test:
    agent: autopilot
    schedule: "0 * * * *"
    runtime: nope
`))
		if err == nil {
			t.Error("expected error for unknown runtime")
		}
	})

	t.Run("bad model syntax", func(t *testing.T) {
		_, err := ParseConfig([]byte(`
jobs:
  test:
    agent: autopilot
    schedule: "0 * * * *"
    model: "opus latest"
`))
		if err == nil {
			t.Error("expected error for malformed model")
		}
	})
}

func TestParseConfigOneShot(t *testing.T) {
	t.Run("in duration variants", func(t *testing.T) {
		for _, in := range []string{"30s", "5m", "2h", "1d", "1h30m"} {
			t.Run(in, func(t *testing.T) {
				cfg, err := ParseConfig([]byte(fmt.Sprintf(`
jobs:
  test:
    agent: autopilot
    in: %q
`, in)))
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				j := cfg.Jobs["test"]
				if !j.IsOneShot() {
					t.Fatal("expected one-shot")
				}
				if j.IsScheduled() || j.IsTrigger() {
					t.Error("should not be scheduled or trigger")
				}
				if !j.ResolvedAt().After(time.Now().UTC()) {
					t.Errorf("resolvedAt %v is not in the future", j.ResolvedAt())
				}
			})
		}
	})

	t.Run("at future timestamp", func(t *testing.T) {
		future := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
		cfg, err := ParseConfig([]byte(fmt.Sprintf(`
jobs:
  test:
    agent: autopilot
    at: %q
`, future)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		j := cfg.Jobs["test"]
		if !j.IsOneShot() {
			t.Fatal("expected one-shot")
		}
		if !j.ResolvedAt().Equal(mustParseRFC3339(t, future)) {
			t.Errorf("resolvedAt = %v, want %v", j.ResolvedAt(), future)
		}
	})

	t.Run("at in the past is a validation error", func(t *testing.T) {
		past := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
		_, err := ParseConfig([]byte(fmt.Sprintf(`
jobs:
  test:
    agent: autopilot
    at: %q
`, past)))
		if err == nil {
			t.Fatal("expected error for at in the past")
		}
	})

	t.Run("at bad format", func(t *testing.T) {
		_, err := ParseConfig([]byte(`
jobs:
  test:
    agent: autopilot
    at: "not-a-timestamp"
`))
		if err == nil {
			t.Fatal("expected error for malformed at timestamp")
		}
	})

	t.Run("in bad duration", func(t *testing.T) {
		_, err := ParseConfig([]byte(`
jobs:
  test:
    agent: autopilot
    in: "banana"
`))
		if err == nil {
			t.Fatal("expected error for malformed in duration")
		}
	})

	t.Run("in non-positive duration", func(t *testing.T) {
		_, err := ParseConfig([]byte(`
jobs:
  test:
    agent: autopilot
    in: "0s"
`))
		if err == nil {
			t.Fatal("expected error for non-positive in duration")
		}
	})

	t.Run("at and in together", func(t *testing.T) {
		future := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
		_, err := ParseConfig([]byte(fmt.Sprintf(`
jobs:
  test:
    agent: autopilot
    at: %q
    in: "5m"
`, future)))
		if err == nil {
			t.Fatal("expected error for both at and in")
		}
	})

	t.Run("in and schedule together", func(t *testing.T) {
		_, err := ParseConfig([]byte(`
jobs:
  test:
    agent: autopilot
    schedule: "0 * * * *"
    in: "5m"
`))
		if err == nil {
			t.Fatal("expected error for both schedule and in")
		}
	})

	t.Run("in and trigger together", func(t *testing.T) {
		_, err := ParseConfig([]byte(`
jobs:
  test:
    agent: autopilot
    trigger: "label:bug"
    in: "5m"
`))
		if err == nil {
			t.Fatal("expected error for both trigger and in")
		}
	})
}

func mustParseRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm.UTC()
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

func TestParseConfigSinks(t *testing.T) {
	base := `
jobs:
  test:
    agent: autopilot
    schedule: "0 * * * *"
`

	t.Run("valid webhook sink", func(t *testing.T) {
		cfg, err := ParseConfig([]byte(base + `
sinks:
  - events: ["completed", "job.done"]
    webhook: https://hooks.example.com/notify
`))
		if err != nil {
			t.Fatalf("ParseConfig: %v", err)
		}
		if len(cfg.Sinks) != 1 {
			t.Fatalf("got %d sinks, want 1", len(cfg.Sinks))
		}
		if cfg.Sinks[0].Webhook != "https://hooks.example.com/notify" {
			t.Errorf("webhook = %q", cfg.Sinks[0].Webhook)
		}
	})

	t.Run("valid exec sink", func(t *testing.T) {
		cfg, err := ParseConfig([]byte(base + `
sinks:
  - events: ["bailed"]
    exec: ./scripts/notify.sh
`))
		if err != nil {
			t.Fatalf("ParseConfig: %v", err)
		}
		if cfg.Sinks[0].Exec != "./scripts/notify.sh" {
			t.Errorf("exec = %q", cfg.Sinks[0].Exec)
		}
	})

	t.Run("both webhook and exec rejected", func(t *testing.T) {
		_, err := ParseConfig([]byte(base + `
sinks:
  - events: ["completed"]
    webhook: https://hooks.example.com/notify
    exec: ./scripts/notify.sh
`))
		if err == nil {
			t.Error("expected error for sink with both webhook and exec")
		}
	})

	t.Run("neither webhook nor exec rejected", func(t *testing.T) {
		_, err := ParseConfig([]byte(base + `
sinks:
  - events: ["completed"]
`))
		if err == nil {
			t.Error("expected error for sink with neither webhook nor exec")
		}
	})

	t.Run("empty events rejected", func(t *testing.T) {
		_, err := ParseConfig([]byte(base + `
sinks:
  - webhook: https://hooks.example.com/notify
`))
		if err == nil {
			t.Error("expected error for sink with no events")
		}
	})
}

func TestConfigPath(t *testing.T) {
	got := ConfigPath("/home/user/repo")
	want := "/home/user/repo/.agent-minder/jobs.yaml"
	if got != want {
		t.Errorf("ConfigPath = %q, want %q", got, want)
	}
}
