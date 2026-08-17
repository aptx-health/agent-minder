package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
)

// Scheduler evaluates cron schedules and inserts job rows when due.
type Scheduler struct {
	store    *db.Store
	deployID string
	owner    string
	repo     string
	config   *Config
	interval time.Duration
}

// New creates a new Scheduler.
func New(store *db.Store, deployID, owner, repo string, config *Config) *Scheduler {
	return &Scheduler{
		store:    store,
		deployID: deployID,
		owner:    owner,
		repo:     repo,
		config:   config,
		interval: 60 * time.Second,
	}
}

// SyncSchedules writes the jobs.yaml config into the job_schedules table,
// computing next_run_at for each cron schedule.
func (s *Scheduler) SyncSchedules() error {
	now := time.Now().UTC()

	// Look up already-fired one-shots so a config reload (e.g. across a
	// daemon restart) doesn't re-resolve `in:` to a fresh future time and
	// refire them.
	existing, err := s.store.GetSchedules(s.deployID)
	if err != nil {
		return fmt.Errorf("load existing schedules: %w", err)
	}
	firedOneShots := make(map[string]bool, len(existing))
	for _, sched := range existing {
		if sched.IsOneShot() && sched.Fired() {
			firedOneShots[sched.Name] = true
		}
	}

	for name, def := range s.config.Jobs {
		switch {
		case def.IsScheduled():
			cron, err := def.ParsedSchedule()
			if err != nil {
				return fmt.Errorf("schedule %q: %w", name, err)
			}
			nextRun := cron.NextAfter(now)

			js := &db.JobSchedule{
				Name:          name,
				DeploymentID:  s.deployID,
				CronExpr:      sql.NullString{String: def.Schedule, Valid: true},
				Kind:          def.EffectiveKind(),
				Agent:         executionAgent(def),
				Runtime:       sql.NullString{String: def.Runtime, Valid: def.Runtime != ""},
				Model:         sql.NullString{String: def.Model, Valid: def.Model != ""},
				ScriptCommand: sql.NullString{String: def.Command, Valid: def.Command != ""},
				ScriptTimeout: sql.NullString{String: def.Timeout, Valid: def.Timeout != ""},
				ScriptWorkDir: sql.NullString{String: def.ScriptWorkDir(), Valid: def.ScriptWorkDir() != ""},
				Description:   sql.NullString{String: def.Description, Valid: def.Description != ""},
				Enabled:       true,
				NextRunAt:     sql.NullTime{Time: nextRun, Valid: !nextRun.IsZero()},
			}
			if len(def.Env) > 0 {
				envJSON, err := json.Marshal(def.Env)
				if err != nil {
					return fmt.Errorf("encode env for schedule %q: %w", name, err)
				}
				js.ScriptEnv = sql.NullString{String: string(envJSON), Valid: true}
			}
			if def.Budget > 0 {
				js.Budget = sql.NullFloat64{Float64: def.Budget, Valid: true}
			}
			if def.MaxTurns > 0 {
				js.MaxTurns = sql.NullInt64{Int64: int64(def.MaxTurns), Valid: true}
			}
			if err := s.store.UpsertSchedule(js); err != nil {
				return fmt.Errorf("save schedule %q: %w", name, err)
			}

		case def.IsOneShot():
			if firedOneShots[name] {
				continue // already fired; do not resurrect or refire it
			}

			js := &db.JobSchedule{
				Name:          name,
				DeploymentID:  s.deployID,
				AtTime:        sql.NullTime{Time: def.ResolvedAt(), Valid: true},
				Kind:          def.EffectiveKind(),
				Agent:         executionAgent(def),
				Runtime:       sql.NullString{String: def.Runtime, Valid: def.Runtime != ""},
				Model:         sql.NullString{String: def.Model, Valid: def.Model != ""},
				ScriptCommand: sql.NullString{String: def.Command, Valid: def.Command != ""},
				ScriptTimeout: sql.NullString{String: def.Timeout, Valid: def.Timeout != ""},
				ScriptWorkDir: sql.NullString{String: def.ScriptWorkDir(), Valid: def.ScriptWorkDir() != ""},
				Description:   sql.NullString{String: def.Description, Valid: def.Description != ""},
				Enabled:       true,
			}
			if len(def.Env) > 0 {
				envJSON, err := json.Marshal(def.Env)
				if err != nil {
					return fmt.Errorf("encode env for schedule %q: %w", name, err)
				}
				js.ScriptEnv = sql.NullString{String: string(envJSON), Valid: true}
			}
			if def.Budget > 0 {
				js.Budget = sql.NullFloat64{Float64: def.Budget, Valid: true}
			}
			if def.MaxTurns > 0 {
				js.MaxTurns = sql.NullInt64{Int64: int64(def.MaxTurns), Valid: true}
			}
			if err := s.store.UpsertSchedule(js); err != nil {
				return fmt.Errorf("save schedule %q: %w", name, err)
			}

		default:
			continue // triggers are handled by watch mode, not the scheduler
		}
	}

	// Reconcile removals: disable any persisted schedule for this deployment
	// that is no longer cron- or one-shot-scheduled in jobs.yaml, so it stops
	// firing. This covers outright removal and in-place conversion between
	// schedule:/at:/in:/trigger: (a name still present in config but no
	// longer scheduled).
	existing, err = s.store.GetSchedules(s.deployID)
	if err != nil {
		return fmt.Errorf("load existing schedules: %w", err)
	}
	for _, sched := range existing {
		if def, ok := s.config.Jobs[sched.Name]; ok && (def.IsScheduled() || def.IsOneShot()) {
			continue // still scheduled in config
		}
		if !sched.Enabled {
			continue // already disabled
		}
		if err := s.store.SetScheduleEnabled(s.deployID, sched.Name, false); err != nil {
			return fmt.Errorf("disable removed schedule %q: %w", sched.Name, err)
		}
	}

	return nil
}

// Run starts the scheduler loop. It checks every interval for due schedules
// and inserts job rows. Blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[scheduler] panic recovered: %v", r)
		}
	}()

	// Initial tick.
	s.tick()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

// tick evaluates all enabled schedules and fires due ones.
func (s *Scheduler) tick() {
	schedules, err := s.store.GetEnabledSchedules(s.deployID)
	if err != nil {
		log.Printf("[scheduler] GetEnabledSchedules error: %v", err)
		return
	}

	now := time.Now().UTC()

	for _, sched := range schedules {
		switch {
		case sched.CronExpr.Valid && sched.CronExpr.String != "":
			if !sched.NextRunAt.Valid || sched.NextRunAt.Time.After(now) {
				continue
			}
			if s.jobAlreadyActive(sched.Name) {
				log.Printf("[scheduler] skip %s (already active)", sched.Name)
				continue
			}
			log.Printf("[scheduler] firing %s (agent: %s)", sched.Name, sched.Agent)
			s.fireSchedule(sched)

		case sched.IsOneShot():
			if sched.AtTime.Time.After(now) {
				continue
			}
			if s.jobAlreadyActive(sched.Name) {
				log.Printf("[scheduler] skip %s (already active)", sched.Name)
				continue
			}
			log.Printf("[scheduler] firing one-shot %s (agent: %s)", sched.Name, sched.Agent)
			s.fireOneShot(sched)
		}
	}
}

// fireSchedule creates a job row for a due schedule.
func (s *Scheduler) fireSchedule(sched *db.JobSchedule) {
	now := time.Now().UTC()

	// Generate unique job name: schedule-name-YYYYMMDD-HHMM
	jobName := fmt.Sprintf("%s-%s", sched.Name, now.Format("20060102-1504"))

	title := sched.Name
	if sched.Description.Valid && sched.Description.String != "" {
		title = sched.Description.String
	}

	job := &db.Job{
		DeploymentID:  s.deployID,
		Kind:          scheduleKind(sched),
		Agent:         sched.Agent,
		Name:          jobName,
		Runtime:       sched.Runtime,
		Model:         sched.Model,
		ScriptCommand: sched.ScriptCommand,
		ScriptTimeout: sched.ScriptTimeout,
		ScriptEnv:     sched.ScriptEnv,
		ScriptWorkDir: sched.ScriptWorkDir,
		IssueTitle:    sql.NullString{String: title, Valid: true},
		Owner:         s.owner,
		Repo:          s.repo,
		Status:        db.StatusQueued,
		SourceType:    sql.NullString{String: "cron", Valid: true},
		SourceName:    sql.NullString{String: sched.Name, Valid: true},
		SourceRef:     sql.NullString{String: now.Format(time.RFC3339), Valid: true},
	}

	if sched.Budget.Valid {
		job.MaxBudgetOv = sched.Budget
	}
	if sched.MaxTurns.Valid {
		job.MaxTurns = sched.MaxTurns
	}

	if err := s.store.CreateJob(job); err != nil {
		log.Printf("[scheduler] CreateJob error for %s: %v", sched.Name, err)
		return
	}

	// Update schedule: last_run_at and compute next_run_at.
	cron, err := ParseCron(sched.CronExpr.String)
	if err != nil {
		log.Printf("[scheduler] ParseCron error for %s: %v", sched.Name, err)
		return
	}
	nextRun := cron.NextAfter(now)
	_ = s.store.UpdateScheduleRun(s.deployID, sched.Name, now, nextRun)
}

// fireOneShot creates a job row for a due one-shot (at:/in:) schedule, then
// marks it fired so it never fires again — including across a daemon
// restart that reloads jobs.yaml and re-resolves an `in:` duration.
func (s *Scheduler) fireOneShot(sched *db.JobSchedule) {
	now := time.Now().UTC()

	jobName := fmt.Sprintf("%s-%s", sched.Name, now.Format("20060102-1504"))

	title := sched.Name
	if sched.Description.Valid && sched.Description.String != "" {
		title = sched.Description.String
	}

	job := &db.Job{
		DeploymentID:  s.deployID,
		Kind:          scheduleKind(sched),
		Agent:         sched.Agent,
		Name:          jobName,
		Runtime:       sched.Runtime,
		Model:         sched.Model,
		ScriptCommand: sched.ScriptCommand,
		ScriptTimeout: sched.ScriptTimeout,
		ScriptEnv:     sched.ScriptEnv,
		ScriptWorkDir: sched.ScriptWorkDir,
		IssueTitle:    sql.NullString{String: title, Valid: true},
		Owner:         s.owner,
		Repo:          s.repo,
		Status:        db.StatusQueued,
		SourceType:    sql.NullString{String: "at", Valid: true},
		SourceName:    sql.NullString{String: sched.Name, Valid: true},
		SourceRef:     sql.NullString{String: now.Format(time.RFC3339), Valid: true},
	}

	if sched.Budget.Valid {
		job.MaxBudgetOv = sched.Budget
	}
	if sched.MaxTurns.Valid {
		job.MaxTurns = sched.MaxTurns
	}

	if err := s.store.CreateJob(job); err != nil {
		log.Printf("[scheduler] CreateJob error for %s: %v", sched.Name, err)
		return
	}

	if err := s.store.MarkScheduleFired(s.deployID, sched.Name, now); err != nil {
		log.Printf("[scheduler] MarkScheduleFired error for %s: %v", sched.Name, err)
	}
}

// jobAlreadyActive checks if a job fired by this schedule (by provenance, not
// name) is queued, running, blocked, or reviewing.
func (s *Scheduler) jobAlreadyActive(scheduleName string) bool {
	return s.jobAlreadyActiveForSource("cron", scheduleName) || s.jobAlreadyActiveForSource("at", scheduleName)
}

func (s *Scheduler) jobAlreadyActiveForSource(sourceType, scheduleName string) bool {
	count, err := s.store.CountActiveJobsBySource(s.deployID, sourceType, scheduleName)
	if err != nil {
		log.Printf("[scheduler] CountActiveJobsBySource error for %s: %v", scheduleName, err)
		return false
	}
	return count > 0
}

// RunOnce fires a specific schedule immediately, regardless of its cron timing.
// Returns the created job ID, or an error.
func (s *Scheduler) RunOnce(name string) (int64, error) {
	sched, err := s.store.GetSchedule(s.deployID, name)
	if err != nil {
		return 0, fmt.Errorf("schedule %q not found", name)
	}

	now := time.Now().UTC()
	jobName := fmt.Sprintf("%s-%s", name, now.Format("20060102-1504"))

	sourceType := "cron"
	if sched.IsOneShot() {
		sourceType = "at"
	}

	job := &db.Job{
		DeploymentID:  s.deployID,
		Kind:          scheduleKind(sched),
		Agent:         sched.Agent,
		Name:          jobName,
		Runtime:       sched.Runtime,
		Model:         sched.Model,
		ScriptCommand: sched.ScriptCommand,
		ScriptTimeout: sched.ScriptTimeout,
		ScriptEnv:     sched.ScriptEnv,
		ScriptWorkDir: sched.ScriptWorkDir,
		Owner:         s.owner,
		Repo:          s.repo,
		Status:        db.StatusQueued,
		SourceType:    sql.NullString{String: sourceType, Valid: true},
		SourceName:    sql.NullString{String: sched.Name, Valid: true},
		SourceRef:     sql.NullString{String: now.Format(time.RFC3339), Valid: true},
	}

	if sched.Budget.Valid {
		job.MaxBudgetOv = sched.Budget
	}
	if sched.MaxTurns.Valid {
		job.MaxTurns = sched.MaxTurns
	}

	if err := s.store.CreateJob(job); err != nil {
		return 0, fmt.Errorf("create job: %w", err)
	}

	// Update last run / disable one-shots so a manual trigger doesn't leave
	// them eligible to also fire automatically once due.
	switch {
	case sched.IsOneShot():
		if err := s.store.MarkScheduleFired(s.deployID, name, now); err != nil {
			log.Printf("[scheduler] MarkScheduleFired error for %s: %v", name, err)
		}
	case sched.CronExpr.Valid:
		cron, _ := ParseCron(sched.CronExpr.String)
		if cron != nil {
			nextRun := cron.NextAfter(now)
			_ = s.store.UpdateScheduleRun(s.deployID, name, now, nextRun)
		}
	}

	return job.ID, nil
}

func scheduleKind(sched *db.JobSchedule) string {
	if sched != nil && sched.Kind != "" {
		return sched.Kind
	}
	return db.JobKindAgent
}

func executionAgent(def JobDef) string {
	if def.EffectiveKind() == JobKindScript {
		return db.JobKindScript
	}
	return def.Agent
}
