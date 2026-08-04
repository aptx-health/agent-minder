package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aptx-health/agent-minder/internal/coordinator"
	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/scheduler"
)

const automationTestConfig = `jobs:
  nightly-maintenance:
    schedule: "0 2 * * *"
    agent: maintainer
    runtime: codex
  ready-issues:
    trigger: "label:agent-ready,backend"
    agent: autopilot
    runtime: opencode
`

func setupAutomationTest(t *testing.T) (*db.Deployment, *db.Store, *scheduler.Config) {
	t.Helper()

	repoDir := t.TempDir()
	configPath := scheduler.ConfigPath(repoDir)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(automationTestConfig), 0o600); err != nil {
		t.Fatalf("write jobs.yaml: %v", err)
	}

	conn, err := db.Open(filepath.Join(t.TempDir(), "minder.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	store := db.NewStore(conn)
	deploy := &db.Deployment{
		ID:             "characterization",
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

	cfg, err := scheduler.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load jobs.yaml: %v", err)
	}
	return deploy, store, cfg
}

func TestForeground_ReportsSubscriptions(t *testing.T) {
	deploy, store, cfg := setupAutomationTest(t)
	routes := coordinator.TriggerRoutesFromConfig(cfg)
	sched := scheduler.New(store, deploy.ID, deploy.Owner, deploy.Repo, cfg)
	if err := sched.SyncSchedules(); err != nil {
		t.Fatalf("sync schedules: %v", err)
	}

	automations := coordinator.ComputeAutomations(deploy, routes, store, deploy.ID)
	if len(automations) != 2 {
		t.Fatalf("startup summary has %d automations, want 2: %#v", len(automations), automations)
	}

	var foundTrigger, foundCron bool
	for _, automation := range automations {
		switch automation.Kind {
		case coordinator.AutomationTrigger:
			foundTrigger = true
			if got, want := automation.Labels, []string{"agent-ready", "backend"}; !equalStrings(got, want) {
				t.Errorf("trigger labels = %v, want %v", got, want)
			}
			if automation.Agent != "autopilot" || automation.Runtime != "opencode" {
				t.Errorf("trigger target = %s via %s, want autopilot via opencode", automation.Agent, automation.Runtime)
			}
		case coordinator.AutomationCron:
			foundCron = true
			if automation.Name != "nightly-maintenance" || automation.Expression != "0 2 * * *" {
				t.Errorf("cron = %s (%s), want nightly-maintenance (0 2 * * *)", automation.Name, automation.Expression)
			}
			if automation.Agent != "maintainer" || automation.Runtime != "codex" {
				t.Errorf("cron target = %s via %s, want maintainer via codex", automation.Agent, automation.Runtime)
			}
			if !automation.HasNextRun || automation.NextRunAt.IsZero() {
				t.Error("cron summary is missing its next run")
			}
		}
	}
	if !foundTrigger || !foundCron {
		t.Fatalf("startup summary missing trigger or cron: %#v", automations)
	}
}

func TestForeground_CronPersists(t *testing.T) {
	deploy, store, cfg := setupAutomationTest(t)
	sched := scheduler.New(store, deploy.ID, deploy.Owner, deploy.Repo, cfg)
	if err := sched.SyncSchedules(); err != nil {
		t.Fatalf("sync schedules: %v", err)
	}

	schedules, err := store.GetSchedules(deploy.ID)
	if err != nil {
		t.Fatalf("get schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("persisted %d schedules, want 1", len(schedules))
	}
	schedule := schedules[0]
	if schedule.Name != "nightly-maintenance" {
		t.Errorf("schedule name = %q, want nightly-maintenance", schedule.Name)
	}
	if !schedule.CronExpr.Valid || schedule.CronExpr.String != "0 2 * * *" {
		t.Errorf("cron expression = %#v, want 0 2 * * *", schedule.CronExpr)
	}
	if schedule.Agent != "maintainer" {
		t.Errorf("agent = %q, want maintainer", schedule.Agent)
	}
	if !schedule.Runtime.Valid || schedule.Runtime.String != "codex" {
		t.Errorf("runtime = %#v, want codex", schedule.Runtime)
	}
	if !schedule.Enabled || !schedule.NextRunAt.Valid {
		t.Errorf("schedule should be enabled with a next run: %#v", schedule)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
