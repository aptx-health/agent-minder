package coordinator

import (
	"database/sql"
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
	return setupWithPolicy(t, jobsConfig, db.ActivationAutomated)
}

func setupWithPolicy(t *testing.T, jobsConfig string, policy db.ActivationPolicy) (*db.Store, *db.Deployment) {
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
		ID:               "coord-test",
		RepoDir:          repoDir,
		Owner:            "acme",
		Repo:             "widgets",
		Mode:             "issues",
		MaxAgents:        1,
		MaxTurns:         50,
		MaxBudgetUSD:     5,
		Runtime:          "claude-code",
		TotalBudgetUSD:   25,
		BaseBranch:       "main",
		ActivationPolicy: policy,
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

func TestSnapshot_PreservesOverridesAndReportsEffectiveSettings(t *testing.T) {
	const config = `jobs:
  inherited:
    trigger: "label:ready"
    agent: autopilot
`
	store, deploy := setup(t, config)
	coord, err := New(Options{Store: store, Deploy: deploy})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	automations := coord.Snapshot()
	if len(automations) != 1 {
		t.Fatalf("snapshot = %#v", automations)
	}
	automation := automations[0]
	if automation.Runtime != "" {
		t.Fatalf("configured runtime = %q, want empty override (legacy output compatibility)", automation.Runtime)
	}
	if automation.EffectiveRuntime != deploy.Runtime || automation.EffectiveMaxTurns != deploy.MaxTurns ||
		automation.EffectiveMaxBudget != deploy.MaxBudgetUSD {
		t.Fatalf("effective settings = %#v, deployment = %#v", automation, deploy)
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

// TestNew_ExplicitPolicySkipsJobsYAML asserts an explicit-activation
// deployment never installs jobs.yaml triggers or cron schedules, even when
// a valid jobs.yaml is present — the core guarantee of issue #653.
func TestNew_ExplicitPolicySkipsJobsYAML(t *testing.T) {
	store, deploy := setupWithPolicy(t, jobsYAML, db.ActivationExplicit)

	coord, err := New(Options{Store: store, Deploy: deploy})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if coord.Scheduler() != nil {
		t.Fatal("explicit deployment must not load the jobs.yaml scheduler")
	}
	if len(coord.Routes()) != 0 {
		t.Fatalf("explicit deployment installed routes: %#v", coord.Routes())
	}
	if len(coord.Snapshot()) != 0 {
		t.Fatalf("explicit deployment snapshot = %#v, want empty (truthful Subscriptions: summary)", coord.Snapshot())
	}
	schedules, err := store.GetSchedules(deploy.ID)
	if err != nil {
		t.Fatalf("get schedules: %v", err)
	}
	if len(schedules) != 0 {
		t.Fatalf("explicit deployment persisted schedules: %#v", schedules)
	}
	if coord.ActivationPolicy() != db.ActivationExplicit {
		t.Errorf("ActivationPolicy() = %q, want explicit", coord.ActivationPolicy())
	}
}

// TestNew_HybridPolicyLoadsJobsYAML asserts a hybrid deployment (explicit
// issues plus an explicit watch filter) still installs jobs.yaml automations.
func TestNew_HybridPolicyLoadsJobsYAML(t *testing.T) {
	store, deploy := setupWithPolicy(t, jobsYAML, db.ActivationHybrid)

	coord, err := New(Options{Store: store, Deploy: deploy})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if coord.Scheduler() == nil {
		t.Fatal("hybrid deployment should still load the jobs.yaml scheduler")
	}
	if len(coord.Routes()) != 1 {
		t.Fatalf("hybrid deployment installed %d routes, want 1", len(coord.Routes()))
	}
}

// TestNew_InvalidJobsYAMLErrorsWhenAutomationRequested verifies a broken
// jobs.yaml is surfaced as a fatal error for automated/hybrid deployments
// instead of being silently ignored.
func TestNew_InvalidJobsYAMLErrorsWhenAutomationRequested(t *testing.T) {
	store, deploy := setupWithPolicy(t, "jobs: {}", db.ActivationAutomated)

	if _, err := New(Options{Store: store, Deploy: deploy}); err == nil {
		t.Fatal("expected error for invalid jobs.yaml when automation is requested")
	}
}

// TestNew_ExplicitPolicyIgnoresInvalidJobsYAML verifies an explicit
// deployment never even attempts to load jobs.yaml, so a broken config next
// to explicitly requested work does not block it.
func TestNew_ExplicitPolicyIgnoresInvalidJobsYAML(t *testing.T) {
	store, deploy := setupWithPolicy(t, "jobs: {}", db.ActivationExplicit)

	coord, err := New(Options{Store: store, Deploy: deploy})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if coord.Scheduler() != nil {
		t.Fatal("explicit deployment must not load jobs.yaml at all")
	}
}

// TestSnapshot_ReportsLastActivationFromProvenance verifies that once a
// trigger fires and creates a job (source_type=trigger, source_name=<route
// name>), the next snapshot derives the automation's last-activation time and
// job id from that job row — no durable automations table involved (DM-3).
func TestSnapshot_ReportsLastActivationFromProvenance(t *testing.T) {
	store, deploy := setup(t, jobsYAML)

	coord, err := New(Options{Store: store, Deploy: deploy})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Before any job exists, the trigger automation reports no activation.
	before := coord.Snapshot()
	for _, a := range before {
		if a.Kind == AutomationTrigger && a.HasLastActivation {
			t.Fatalf("trigger automation reports activation before any job exists: %#v", a)
		}
	}

	// Simulate the trigger firing: a job created with matching provenance.
	job := &db.Job{
		DeploymentID: deploy.ID,
		Agent:        "autopilot",
		Name:         "autopilot-issue-99",
		Owner:        deploy.Owner,
		Repo:         deploy.Repo,
		IssueNumber:  99,
		Status:       db.StatusQueued,
		SourceType:   sql.NullString{String: "trigger", Valid: true},
		SourceName:   sql.NullString{String: "ready-issues", Valid: true},
	}
	if err := store.CreateJob(job); err != nil {
		t.Fatalf("create job: %v", err)
	}

	after := coord.Snapshot()
	var found bool
	for _, a := range after {
		if a.Kind != AutomationTrigger {
			continue
		}
		found = true
		if !a.HasLastActivation {
			t.Fatalf("trigger automation missing last activation after job creation: %#v", a)
		}
		if a.LastActivationJobID != job.ID {
			t.Errorf("LastActivationJobID = %d, want %d", a.LastActivationJobID, job.ID)
		}
		if a.LastActivationAt.IsZero() {
			t.Error("LastActivationAt is zero, want the job's queued_at time")
		}
	}
	if !found {
		t.Fatal("snapshot did not include the trigger automation")
	}
}

// TestConfigRevision_ValidAndInvalid covers both branches DM-3 asks for:
// LoadConfigRevision reports a content hash and load time for a valid
// jobs.yaml, and surfaces the parse error (with HasConfig still true) for an
// invalid one.
func TestConfigRevision_ValidAndInvalid(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		repoDir := t.TempDir()
		configPath := scheduler.ConfigPath(repoDir)
		if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(configPath, []byte(jobsYAML), 0o600); err != nil {
			t.Fatalf("write jobs.yaml: %v", err)
		}

		rev, cfg, err := LoadConfigRevision(repoDir)
		if err != nil {
			t.Fatalf("LoadConfigRevision: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected parsed config, got nil")
		}
		if !rev.HasConfig {
			t.Error("HasConfig = false, want true")
		}
		if rev.SHA256 == "" {
			t.Error("SHA256 is empty")
		}
		if rev.LoadedAt.IsZero() {
			t.Error("LoadedAt is zero")
		}
		if rev.ValidateErr != nil {
			t.Errorf("ValidateErr = %v, want nil", rev.ValidateErr)
		}
		if rev.Path != configPath {
			t.Errorf("Path = %q, want %q", rev.Path, configPath)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		repoDir := t.TempDir()
		configPath := scheduler.ConfigPath(repoDir)
		if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(configPath, []byte("jobs: {}"), 0o600); err != nil {
			t.Fatalf("write jobs.yaml: %v", err)
		}

		rev, cfg, err := LoadConfigRevision(repoDir)
		if err == nil {
			t.Fatal("expected error for empty jobs config")
		}
		if cfg != nil {
			t.Errorf("cfg = %#v, want nil on validation error", cfg)
		}
		if !rev.HasConfig {
			t.Error("HasConfig = false, want true (file exists, content is invalid)")
		}
		if rev.ValidateErr == nil {
			t.Error("ValidateErr is nil, want the parse error")
		}
	})

	t.Run("absent", func(t *testing.T) {
		repoDir := t.TempDir()

		rev, cfg, err := LoadConfigRevision(repoDir)
		if err != nil {
			t.Fatalf("LoadConfigRevision: %v", err)
		}
		if cfg != nil {
			t.Errorf("cfg = %#v, want nil when jobs.yaml is absent", cfg)
		}
		if rev.HasConfig {
			t.Error("HasConfig = true, want false when jobs.yaml is absent")
		}
	})
}

// TestNew_ExposesConfigRevision verifies the Coordinator itself surfaces the
// config revision it loaded during New, matching what a deployed automation
// worker actually ran with.
func TestNew_ExposesConfigRevision(t *testing.T) {
	store, deploy := setup(t, jobsYAML)

	coord, err := New(Options{Store: store, Deploy: deploy})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rev := coord.ConfigRevision()
	if !rev.HasConfig || rev.SHA256 == "" {
		t.Fatalf("ConfigRevision() = %#v, want HasConfig and a SHA256", rev)
	}
}
