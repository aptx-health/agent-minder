package scheduler

import (
	"database/sql"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
)

func TestScheduleScopedToDeployment(t *testing.T) {
	t.Skip("M1-08: scope schedules to deployment and preserve last-run during reconciliation")

	store := testStore(t)
	first := createAutomationSpecDeployment(t, store, "schedule-scope-a")
	second := createAutomationSpecDeployment(t, store, "schedule-scope-b")
	cfg := parseAutomationSpecConfig(t, `
jobs:
  shared-nightly:
    schedule: "0 2 * * *"
    agent: maintainer
`)

	firstScheduler := New(store, first.ID, first.Owner, first.Repo, cfg)
	if err := firstScheduler.SyncSchedules(); err != nil {
		t.Fatalf("sync first deployment: %v", err)
	}

	lastRun := time.Date(2026, time.July, 31, 2, 0, 0, 0, time.UTC)
	if _, err := store.DB().Exec(`
		UPDATE job_schedules
		SET last_run_at = ?
		WHERE deployment_id = ? AND name = ?`, lastRun, first.ID, "shared-nightly"); err != nil {
		t.Fatalf("record first deployment last-run: %v", err)
	}
	if err := firstScheduler.SyncSchedules(); err != nil {
		t.Fatalf("re-sync first deployment: %v", err)
	}

	secondScheduler := New(store, second.ID, second.Owner, second.Repo, cfg)
	if err := secondScheduler.SyncSchedules(); err != nil {
		t.Fatalf("sync second deployment: %v", err)
	}

	type persistedSchedule struct {
		DeploymentID string       `db:"deployment_id"`
		Name         string       `db:"name"`
		LastRunAt    sql.NullTime `db:"last_run_at"`
	}
	var rows []persistedSchedule
	if err := store.DB().Select(&rows, `
		SELECT deployment_id, name, last_run_at
		FROM job_schedules
		WHERE name = ?
		ORDER BY deployment_id`, "shared-nightly"); err != nil {
		t.Fatalf("query scoped schedules: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("persisted %d shared-nightly schedules, want 2: %#v", len(rows), rows)
	}
	if rows[0].DeploymentID != first.ID || rows[1].DeploymentID != second.ID {
		t.Fatalf("schedule deployment IDs = [%q, %q], want [%q, %q]", rows[0].DeploymentID, rows[1].DeploymentID, first.ID, second.ID)
	}
	if !rows[0].LastRunAt.Valid || !rows[0].LastRunAt.Time.Equal(lastRun) {
		t.Fatalf("first deployment last-run = %#v, want %s", rows[0].LastRunAt, lastRun)
	}
}

func TestRemovedAutomation_Disabled(t *testing.T) {
	store := testStore(t)
	deploy := createAutomationSpecDeployment(t, store, "removed-automation")
	initial := parseAutomationSpecConfig(t, `
jobs:
  keep-nightly:
    schedule: "0 1 * * *"
    agent: maintainer
  remove-nightly:
    schedule: "0 2 * * *"
    agent: maintainer
`)
	if err := New(store, deploy.ID, deploy.Owner, deploy.Repo, initial).SyncSchedules(); err != nil {
		t.Fatalf("sync initial automations: %v", err)
	}

	reconciled := parseAutomationSpecConfig(t, `
jobs:
  keep-nightly:
    schedule: "0 1 * * *"
    agent: maintainer
`)
	if err := New(store, deploy.ID, deploy.Owner, deploy.Repo, reconciled).SyncSchedules(); err != nil {
		t.Fatalf("reconcile removed automation: %v", err)
	}

	var enabled int
	if err := store.DB().QueryRow(`
		SELECT enabled
		FROM job_schedules
		WHERE deployment_id = ? AND name = ?`, deploy.ID, "remove-nightly").Scan(&enabled); err != nil {
		t.Fatalf("query removed automation: %v", err)
	}
	if enabled != 0 {
		t.Fatalf("removed automation enabled = %d, want 0", enabled)
	}
}

func createAutomationSpecDeployment(t *testing.T, store *db.Store, id string) *db.Deployment {
	t.Helper()
	deploy := &db.Deployment{
		ID:             id,
		RepoDir:        t.TempDir(),
		Owner:          "acme",
		Repo:           "widgets",
		Mode:           "issues",
		MaxAgents:      1,
		MaxTurns:       50,
		MaxBudgetUSD:   5,
		TotalBudgetUSD: 25,
		BaseBranch:     "main",
	}
	if err := store.CreateDeployment(deploy); err != nil {
		t.Fatalf("create deployment %q: %v", id, err)
	}
	return deploy
}

func parseAutomationSpecConfig(t *testing.T, yaml string) *Config {
	t.Helper()
	cfg, err := ParseConfig([]byte(yaml))
	if err != nil {
		t.Fatalf("parse automation config: %v", err)
	}
	return cfg
}
