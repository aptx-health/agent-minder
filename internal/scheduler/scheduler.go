package scheduler

import (
	"context"
	"database/sql"
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

	for name, def := range s.config.Jobs {
		if !def.IsScheduled() {
			continue // triggers are handled by watch mode, not the scheduler
		}

		cron, err := def.ParsedSchedule()
		if err != nil {
			return fmt.Errorf("schedule %q: %w", name, err)
		}

		nextRun := cron.NextAfter(now)

		js := &db.JobSchedule{
			Name:         name,
			DeploymentID: s.deployID,
			CronExpr:     sql.NullString{String: def.Schedule, Valid: true},
			Agent:        def.Agent,
			Description:  sql.NullString{String: def.Description, Valid: def.Description != ""},
			Enabled:      true,
			NextRunAt:    sql.NullTime{Time: nextRun, Valid: !nextRun.IsZero()},
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
		if !sched.CronExpr.Valid || sched.CronExpr.String == "" {
			continue
		}

		if !sched.NextRunAt.Valid || sched.NextRunAt.Time.After(now) {
			continue
		}

		if s.jobAlreadyActive(sched.Name) {
			log.Printf("[scheduler] skip %s (already active)", sched.Name)
			continue
		}

		log.Printf("[scheduler] firing %s (agent: %s)", sched.Name, sched.Agent)
		s.fireSchedule(sched)
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
		DeploymentID: s.deployID,
		Agent:        sched.Agent,
		Name:         jobName,
		IssueTitle:   sql.NullString{String: title, Valid: true},
		Owner:        s.owner,
		Repo:         s.repo,
		Status:       db.StatusQueued,
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
	_ = s.store.UpdateScheduleRun(sched.Name, now, nextRun)
}

// jobAlreadyActive checks if a job matching this schedule name is queued or running.
func (s *Scheduler) jobAlreadyActive(scheduleName string) bool {
	jobs, err := s.store.GetJobs(s.deployID)
	if err != nil {
		return false
	}
	for _, j := range jobs {
		// Match by prefix: schedule jobs are named "schedule-name-YYYYMMDD-HHMM"
		if len(j.Name) > len(scheduleName) && j.Name[:len(scheduleName)] == scheduleName &&
			j.Name[len(scheduleName)] == '-' {
			if j.Status == db.StatusQueued || j.Status == db.StatusRunning ||
				j.Status == db.StatusBlocked || j.Status == db.StatusReviewing {
				return true
			}
		}
	}
	return false
}

// RunOnce fires a specific schedule immediately, regardless of its cron timing.
// Returns the created job ID, or an error.
func (s *Scheduler) RunOnce(name string) (int64, error) {
	sched, err := s.store.GetSchedule(name)
	if err != nil {
		return 0, fmt.Errorf("schedule %q not found", name)
	}

	now := time.Now().UTC()
	jobName := fmt.Sprintf("%s-%s", name, now.Format("20060102-1504"))

	job := &db.Job{
		DeploymentID: s.deployID,
		Agent:        sched.Agent,
		Name:         jobName,
		Owner:        s.owner,
		Repo:         s.repo,
		Status:       db.StatusQueued,
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

	// Update last run.
	if sched.CronExpr.Valid {
		cron, _ := ParseCron(sched.CronExpr.String)
		if cron != nil {
			nextRun := cron.NextAfter(now)
			_ = s.store.UpdateScheduleRun(name, now, nextRun)
		}
	}

	return job.ID, nil
}
