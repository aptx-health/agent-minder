package coordinator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aptx-health/agent-minder/internal/db"
	// Side-effect imports register the runtime factories so jobs.yaml
	// validation recognizes the codex/opencode runtime names below, matching
	// the registry the real entry points assemble.
	_ "github.com/aptx-health/agent-minder/internal/runtime/claudecode"
	_ "github.com/aptx-health/agent-minder/internal/runtime/codex"
	_ "github.com/aptx-health/agent-minder/internal/runtime/opencode"
	"github.com/aptx-health/agent-minder/internal/scheduler"
)

const jobsYAML = `jobs:
  nightly-maintenance:
    schedule: "0 2 * * *"
    agent: maintainer
    runtime: codex
  ready-issues:
    trigger: "label:agent-ready,backend"
    agent: autopilot
    runtime: opencode
`

func setup(t *testing.T, jobsConfig string) (*db.Store, *db.Deployment) {
	t.Helper()

	repoDir := t.TempDir()
	if jobsConfig != "" {
		configPath := scheduler.ConfigPath(repoDir)
		if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
			t.Fatalf("create config directory: %v", err)
		}
		if err := os.WriteFile(configPath, []byte(jobsConfig), 0o600); err != nil {
			t.Fatalf("write jobs.yaml: %v", err)
		}
	}

	conn, err := db.Open(filepath.Join(t.TempDir(), "minder.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	store := db.NewStore(conn)

	deploy := &db.Deployment{
		ID:             "coord-test",
		RepoDir:        repoDir,
		Owner:          "acme",
		Repo:           "widgets",
		Mode:           "issues",
		MaxAgents:      1,
		MaxTurns:       50,
		MaxBudgetUSD:   5,
		Runtime:        "claude-code",
		TotalBudgetUSD: 25,
		BaseBranch:     "main",
	}
	if err := store.CreateDeployment(deploy); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	return store, deploy
}

// TestNew_AssemblesSchedulerAndRoutes verifies the coordinator loads jobs.yaml,
// persists the cron schedule, and installs the trigger route — the assembly the
// foreground and daemon entry points used to duplicate inline.
func TestNew_AssemblesSchedulerAndRoutes(t *testing.T) {
	store, deploy := setup(t, jobsYAML)

	coord, err := New(Options{Store: store, Deploy: deploy})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if w := coord.SyncWarning(); w != nil {
		t.Fatalf("unexpected sync warning: %v", w)
	}
	if coord.Scheduler() == nil {
		t.Fatal("scheduler was not loaded from jobs.yaml")
	}
	if got := len(coord.Routes()); got != 1 {
		t.Fatalf("installed %d trigger routes, want 1: %#v", got, coord.Routes())
	}

	schedules, err := store.GetSchedules(deploy.ID)
	if err != nil {
		t.Fatalf("get schedules: %v", err)
	}
	if len(schedules) != 1 || schedules[0].Name != "nightly-maintenance" {
		t.Fatalf("persisted schedules = %#v, want one nightly-maintenance", schedules)
	}
}

// TestSnapshot_ReportsTriggerAndCron mirrors the foreground startup summary: the
// snapshot must surface both the trigger and the cron automation with their
// targets.
func TestSnapshot_ReportsTriggerAndCron(t *testing.T) {
	store, deploy := setup(t, jobsYAML)

	coord, err := New(Options{Store: store, Deploy: deploy})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	automations := coord.Snapshot()
	if len(automations) != 2 {
		t.Fatalf("snapshot has %d automations, want 2: %#v", len(automations), automations)
	}

	var foundTrigger, foundCron bool
	for _, a := range automations {
		switch a.Kind {
		case AutomationTrigger:
			foundTrigger = true
			if a.Agent != "autopilot" || a.Runtime != "opencode" {
				t.Errorf("trigger target = %s via %s, want autopilot via opencode", a.Agent, a.Runtime)
			}
		case AutomationCron:
			foundCron = true
			if a.Name != "nightly-maintenance" || a.Expression != "0 2 * * *" {
				t.Errorf("cron = %s (%s), want nightly-maintenance (0 2 * * *)", a.Name, a.Expression)
			}
			if !a.HasNextRun || a.NextRunAt.IsZero() {
				t.Error("cron automation is missing its next run")
			}
		}
	}
	if !foundTrigger || !foundCron {
		t.Fatalf("snapshot missing trigger or cron: %#v", automations)
	}
}

// TestNew_NoConfig assembles cleanly when no jobs.yaml is present: no scheduler,
// no routes, empty snapshot.
func TestNew_NoConfig(t *testing.T) {
	store, deploy := setup(t, "")

	coord, err := New(Options{Store: store, Deploy: deploy})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if coord.Scheduler() != nil {
		t.Fatal("scheduler should be nil without jobs.yaml")
	}
	if len(coord.Routes()) != 0 {
		t.Fatalf("routes = %#v, want none", coord.Routes())
	}
	if len(coord.Snapshot()) != 0 {
		t.Fatalf("snapshot = %#v, want empty", coord.Snapshot())
	}
}
