package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// Store wraps a sqlx.DB for type-safe query methods.
type Store struct {
	db *sqlx.DB
}

// NewStore creates a new Store.
func NewStore(db *sqlx.DB) *Store {
	return &Store{db: db}
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying sqlx.DB for advanced operations.
func (s *Store) DB() *sqlx.DB {
	return s.db
}

// --- Deployment CRUD ---

// CreateDeployment inserts a new deployment.
func (s *Store) CreateDeployment(d *Deployment) error {
	_, err := s.db.Exec(`INSERT INTO deployments
		(id, repo_dir, owner, repo, mode, watch_filter, max_agents, max_turns,
		 max_budget_usd, analyzer_model, skip_label, auto_merge, review_enabled,
		 review_max_turns, review_max_budget, total_budget_usd, carried_cost_usd, base_branch)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.RepoDir, d.Owner, d.Repo, d.Mode, d.WatchFilter,
		d.MaxAgents, d.MaxTurns, d.MaxBudgetUSD, d.AnalyzerModel,
		d.SkipLabel, d.AutoMerge, d.ReviewEnabled,
		d.ReviewMaxTurns, d.ReviewMaxBudget,
		d.TotalBudgetUSD, d.CarriedCostUSD, d.BaseBranch)
	return err
}

// GetDeployment retrieves a deployment by ID.
func (s *Store) GetDeployment(id string) (*Deployment, error) {
	var d Deployment
	err := s.db.Get(&d, "SELECT * FROM deployments WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ListDeployments returns all deployments, most recent first.
func (s *Store) ListDeployments() ([]*Deployment, error) {
	var ds []*Deployment
	err := s.db.Select(&ds, "SELECT * FROM deployments ORDER BY started_at DESC")
	return ds, err
}

// UpdateDeploymentCarriedCost updates the carried cost for a deployment.
func (s *Store) UpdateDeploymentCarriedCost(id string, cost float64) error {
	_, err := s.db.Exec("UPDATE deployments SET carried_cost_usd = ? WHERE id = ?", cost, id)
	return err
}

// --- Job CRUD ---

// CreateJob inserts a new job.
func (s *Store) CreateJob(j *Job) error {
	res, err := s.db.Exec(`INSERT INTO jobs
		(deployment_id, agent, name, issue_number, issue_title, issue_body,
		 owner, repo, status, dependencies, stages_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.DeploymentID, j.Agent, j.Name, j.IssueNumber, j.IssueTitle, j.IssueBody,
		j.Owner, j.Repo, j.Status, j.Dependencies, j.StagesJSON)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	j.ID = id
	return nil
}

// BulkCreateJobs inserts multiple jobs, ignoring duplicates.
func (s *Store) BulkCreateJobs(jobs []*Job) error {
	for _, j := range jobs {
		_, err := s.db.Exec(`INSERT OR IGNORE INTO jobs
			(deployment_id, agent, name, issue_number, issue_title, issue_body,
			 owner, repo, status, dependencies, stages_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.DeploymentID, j.Agent, j.Name, j.IssueNumber, j.IssueTitle, j.IssueBody,
			j.Owner, j.Repo, j.Status, j.Dependencies, j.StagesJSON)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetJobs returns all jobs for a deployment.
func (s *Store) GetJobs(deploymentID string) ([]*Job, error) {
	var jobs []*Job
	err := s.db.Select(&jobs, "SELECT * FROM jobs WHERE deployment_id = ? ORDER BY id", deploymentID)
	return jobs, err
}

// GetJob returns a single job by ID.
func (s *Store) GetJob(id int64) (*Job, error) {
	var j Job
	err := s.db.Get(&j, "SELECT * FROM jobs WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// UpdateJobStatus updates the status of a job.
func (s *Store) UpdateJobStatus(id int64, status string) error {
	_, err := s.db.Exec("UPDATE jobs SET status = ? WHERE id = ?", status, id)
	return err
}

// UpdateJobRunning marks a job as running with a start time.
func (s *Store) UpdateJobRunning(id int64) error {
	_, err := s.db.Exec("UPDATE jobs SET status = 'running', started_at = ? WHERE id = ?",
		time.Now().UTC(), id)
	return err
}

// UpdateJobStage updates the current stage and stages JSON for a job.
func (s *Store) UpdateJobStage(id int64, stage string, stagesJSON string) error {
	_, err := s.db.Exec("UPDATE jobs SET current_stage = ?, stages_json = ? WHERE id = ?",
		stage, stagesJSON, id)
	return err
}

// UpdateJobResult sets the result JSON for a completed job.
func (s *Store) UpdateJobResult(id int64, resultJSON string) error {
	_, err := s.db.Exec("UPDATE jobs SET result_json = ? WHERE id = ?", resultJSON, id)
	return err
}

// UpdateJobWorktree sets the worktree path and branch for a job.
func (s *Store) UpdateJobWorktree(id int64, path, branch string) error {
	_, err := s.db.Exec("UPDATE jobs SET worktree_path = ?, branch = ? WHERE id = ?",
		path, branch, id)
	return err
}

// UpdateJobPR sets the PR number for a job.
func (s *Store) UpdateJobPR(id int64, prNumber int) error {
	_, err := s.db.Exec("UPDATE jobs SET pr_number = ? WHERE id = ?", prNumber, id)
	return err
}

// UpdateJobCost updates the cost for a job.
func (s *Store) UpdateJobCost(id int64, cost float64) error {
	_, err := s.db.Exec("UPDATE jobs SET cost_usd = ? WHERE id = ?", cost, id)
	return err
}

// UpdateJobFailure sets failure info and marks the job as bailed.
func (s *Store) UpdateJobFailure(id int64, reason, detail string) error {
	_, err := s.db.Exec(`UPDATE jobs SET status = 'bailed', failure_reason = ?,
		failure_detail = ?, completed_at = ? WHERE id = ?`,
		reason, detail, time.Now().UTC(), id)
	return err
}

// UpdateJobDeps updates the dependencies JSON for a job.
func (s *Store) UpdateJobDeps(id int64, deps []int) error {
	data, _ := json.Marshal(deps)
	_, err := s.db.Exec("UPDATE jobs SET dependencies = ? WHERE id = ?", string(data), id)
	return err
}

// UpdateJobReview sets review-related fields.
func (s *Store) UpdateJobReview(id int64, risk string, commentID int64) error {
	_, err := s.db.Exec("UPDATE jobs SET review_risk = ?, review_comment_id = ? WHERE id = ?",
		risk, commentID, id)
	return err
}

// UpdateJobOverrides sets per-job turn/budget overrides.
func (s *Store) UpdateJobOverrides(id int64, turns *int, budget *float64) error {
	_, err := s.db.Exec("UPDATE jobs SET max_turns = ?, max_budget_usd = ? WHERE id = ?",
		turns, budget, id)
	return err
}

// CompleteJob marks a job as done with a completion time.
func (s *Store) CompleteJob(id int64, status string) error {
	_, err := s.db.Exec("UPDATE jobs SET status = ?, completed_at = ? WHERE id = ?",
		status, time.Now().UTC(), id)
	return err
}

// ResetJob resets a job to queued, clearing runtime state.
func (s *Store) ResetJob(id int64) error {
	_, err := s.db.Exec(`UPDATE jobs SET status = 'queued', worktree_path = NULL,
		branch = NULL, pr_number = NULL, cost_usd = 0, failure_reason = NULL,
		failure_detail = NULL, review_risk = NULL, review_comment_id = NULL,
		current_stage = NULL, result_json = NULL,
		agent_log = NULL, started_at = NULL, completed_at = NULL WHERE id = ?`, id)
	return err
}

// ClearJobWorktree clears the worktree path for a job.
func (s *Store) ClearJobWorktree(id int64) error {
	_, err := s.db.Exec("UPDATE jobs SET worktree_path = NULL WHERE id = ?", id)
	return err
}

// TransitionStaleRunningJobs moves running jobs back to queued (for crash recovery).
func (s *Store) TransitionStaleRunningJobs(deploymentID string) (int64, error) {
	res, err := s.db.Exec(`UPDATE jobs SET status = 'queued', started_at = NULL
		WHERE deployment_id = ? AND status = 'running'`, deploymentID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// QueuedUnblockedJobs returns queued jobs whose dependencies are all satisfied.
func (s *Store) QueuedUnblockedJobs(deploymentID string) ([]*Job, error) {
	jobs, err := s.GetJobs(deploymentID)
	if err != nil {
		return nil, err
	}

	// Build status map: issue_number → status.
	statusMap := make(map[int]string)
	for _, j := range jobs {
		if j.IssueNumber > 0 {
			statusMap[j.IssueNumber] = j.Status
		}
	}

	var result []*Job
	for _, j := range jobs {
		if j.Status != StatusQueued && j.Status != StatusBlocked {
			continue
		}

		// Parse dependencies.
		if !j.Dependencies.Valid || j.Dependencies.String == "" || j.Dependencies.String == "null" {
			result = append(result, j)
			continue
		}

		var deps []int
		if err := json.Unmarshal([]byte(j.Dependencies.String), &deps); err != nil {
			// Malformed deps — treat as unblocked.
			result = append(result, j)
			continue
		}

		blocked := false
		for _, dep := range deps {
			depStatus, exists := statusMap[dep]
			if !exists {
				// External dep — not tracked, assume unblocked.
				continue
			}
			if depStatus != StatusDone && depStatus != StatusReview &&
				depStatus != StatusReviewing && depStatus != StatusReviewed {
				blocked = true
				break
			}
		}
		if !blocked {
			result = append(result, j)
		}
	}

	return result, nil
}

// TotalSpend returns the sum of cost_usd for all jobs in a deployment plus carried cost.
func (s *Store) TotalSpend(deploymentID string) (float64, error) {
	var jobCost sql.NullFloat64
	err := s.db.Get(&jobCost, "SELECT SUM(cost_usd) FROM jobs WHERE deployment_id = ?", deploymentID)
	if err != nil {
		return 0, err
	}

	var carried float64
	_ = s.db.Get(&carried, "SELECT carried_cost_usd FROM deployments WHERE id = ?", deploymentID)

	cost := carried
	if jobCost.Valid {
		cost += jobCost.Float64
	}
	return cost, nil
}

// --- Dep Graph ---

// SaveDepGraph saves or replaces the dependency graph for a deployment.
func (s *Store) SaveDepGraph(deploymentID, graphJSON, optionName string) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO dep_graphs
		(deployment_id, graph_json, option_name, created_at)
		VALUES (?, ?, ?, ?)`,
		deploymentID, graphJSON, optionName, time.Now().UTC())
	return err
}

// SaveDepGraphFull saves a dep graph with reasoning and confidence.
func (s *Store) SaveDepGraphFull(deploymentID, graphJSON, optionName, reasoning, confidence string) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO dep_graphs
		(deployment_id, graph_json, option_name, reasoning, confidence, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		deploymentID, graphJSON, optionName, reasoning, confidence, time.Now().UTC())
	return err
}

// GetDepGraph retrieves the dependency graph for a deployment.
func (s *Store) GetDepGraph(deploymentID string) (*DepGraph, error) {
	var g DepGraph
	err := s.db.Get(&g, "SELECT * FROM dep_graphs WHERE deployment_id = ?", deploymentID)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// DeleteDepGraph deletes the dependency graph for a deployment.
func (s *Store) DeleteDepGraph(deploymentID string) error {
	_, err := s.db.Exec("DELETE FROM dep_graphs WHERE deployment_id = ?", deploymentID)
	return err
}

// --- Lessons ---

// CreateLesson inserts a new lesson.
func (s *Store) CreateLesson(l *Lesson) error {
	res, err := s.db.Exec(`INSERT INTO lessons
		(repo_scope, content, source, active, pinned)
		VALUES (?, ?, ?, ?, ?)`,
		l.RepoScope, l.Content, l.Source, l.Active, l.Pinned)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	l.ID = id
	return nil
}

// GetActiveLessons returns all active lessons, optionally filtered by repo scope.
func (s *Store) GetActiveLessons(repoScope string) ([]*Lesson, error) {
	var lessons []*Lesson
	if repoScope == "" {
		err := s.db.Select(&lessons,
			"SELECT * FROM lessons WHERE active = 1 AND superseded_by IS NULL ORDER BY pinned DESC, times_injected ASC")
		return lessons, err
	}
	err := s.db.Select(&lessons,
		`SELECT * FROM lessons WHERE active = 1 AND superseded_by IS NULL
		 AND (repo_scope IS NULL OR repo_scope = ?)
		 ORDER BY pinned DESC, times_injected ASC`, repoScope)
	return lessons, err
}

// GetAllLessons returns all lessons (including inactive), optionally filtered.
func (s *Store) GetAllLessons(repoScope string, includeInactive bool) ([]*Lesson, error) {
	var lessons []*Lesson
	var query strings.Builder
	var args []interface{}

	query.WriteString("SELECT * FROM lessons WHERE 1=1")
	if !includeInactive {
		query.WriteString(" AND active = 1")
	}
	if repoScope != "" {
		query.WriteString(" AND (repo_scope IS NULL OR repo_scope = ?)")
		args = append(args, repoScope)
	}
	query.WriteString(" ORDER BY created_at DESC")

	err := s.db.Select(&lessons, query.String(), args...)
	return lessons, err
}

// GetLesson retrieves a single lesson by ID.
func (s *Store) GetLesson(id int64) (*Lesson, error) {
	var l Lesson
	err := s.db.Get(&l, "SELECT * FROM lessons WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// UpdateLessonContent updates the content and updated_at timestamp.
func (s *Store) UpdateLessonContent(id int64, content string) error {
	_, err := s.db.Exec("UPDATE lessons SET content = ?, updated_at = ? WHERE id = ?",
		content, time.Now().UTC(), id)
	return err
}

// UpdateLessonActive sets a lesson's active state.
func (s *Store) UpdateLessonActive(id int64, active bool) error {
	_, err := s.db.Exec("UPDATE lessons SET active = ?, updated_at = ? WHERE id = ?",
		active, time.Now().UTC(), id)
	return err
}

// UpdateLessonPinned sets a lesson's pinned state.
func (s *Store) UpdateLessonPinned(id int64, pinned bool) error {
	_, err := s.db.Exec("UPDATE lessons SET pinned = ?, updated_at = ? WHERE id = ?",
		pinned, time.Now().UTC(), id)
	return err
}

// SupersedeLesson marks a lesson as superseded by another.
func (s *Store) SupersedeLesson(oldID, newID int64) error {
	_, err := s.db.Exec("UPDATE lessons SET superseded_by = ?, active = 0, updated_at = ? WHERE id = ?",
		newID, time.Now().UTC(), oldID)
	return err
}

// DeleteLesson permanently removes a lesson.
func (s *Store) DeleteLesson(id int64) error {
	_, err := s.db.Exec("DELETE FROM lessons WHERE id = ?", id)
	return err
}

// IncrementLessonInjected updates injection stats for a set of lessons.
func (s *Store) IncrementLessonInjected(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids)+1)
	args[0] = time.Now().UTC()
	for i, id := range ids {
		placeholders[i] = "?"
		args[i+1] = id
	}
	query := fmt.Sprintf(
		"UPDATE lessons SET times_injected = times_injected + 1, last_injected_at = ? WHERE id IN (%s)",
		strings.Join(placeholders, ","))
	_, err := s.db.Exec(query, args...)
	return err
}

// RecordJobLessons records which lessons were injected into a job.
func (s *Store) RecordJobLessons(jobID int64, lessonIDs []int64) error {
	for _, lid := range lessonIDs {
		_, err := s.db.Exec("INSERT OR IGNORE INTO job_lessons (job_id, lesson_id) VALUES (?, ?)",
			jobID, lid)
		if err != nil {
			return err
		}
	}
	return nil
}

// UpdateLessonOutcome increments helpful or unhelpful counts for lessons injected into a job.
func (s *Store) UpdateLessonOutcome(jobID int64, helpful bool) error {
	col := "times_helpful"
	if !helpful {
		col = "times_unhelpful"
	}
	query := fmt.Sprintf(`UPDATE lessons SET %s = %s + 1 WHERE id IN
		(SELECT lesson_id FROM job_lessons WHERE job_id = ?)`, col, col)
	_, err := s.db.Exec(query, jobID)
	return err
}

// StaleLessons returns active non-pinned lessons not injected in the given duration.
func (s *Store) StaleLessons(staleDuration time.Duration) ([]*Lesson, error) {
	var lessons []*Lesson
	cutoff := time.Now().UTC().Add(-staleDuration)
	err := s.db.Select(&lessons,
		`SELECT * FROM lessons WHERE active = 1 AND pinned = 0
		 AND (last_injected_at IS NULL OR last_injected_at < ?)
		 ORDER BY last_injected_at ASC`, cutoff)
	return lessons, err
}

// IneffectiveLessons returns active lessons with more unhelpful than helpful outcomes.
func (s *Store) IneffectiveLessons(minInjections int) ([]*Lesson, error) {
	var lessons []*Lesson
	err := s.db.Select(&lessons,
		`SELECT * FROM lessons WHERE active = 1 AND pinned = 0
		 AND times_injected >= ? AND times_unhelpful > times_helpful`, minInjections)
	return lessons, err
}

// --- Repo Onboarding ---

// SaveOnboarding upserts repo onboarding data.
func (s *Store) SaveOnboarding(o *RepoOnboarding) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO repo_onboarding
		(repo_dir, owner, repo, yaml_content, validation_status, validation_failures, scanned_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		o.RepoDir, o.Owner, o.Repo, o.YAMLContent,
		o.ValidationStatus, o.ValidationFailures, time.Now().UTC())
	return err
}

// GetOnboarding retrieves onboarding data for a repo.
func (s *Store) GetOnboarding(repoDir string) (*RepoOnboarding, error) {
	var o RepoOnboarding
	err := s.db.Get(&o, "SELECT * FROM repo_onboarding WHERE repo_dir = ?", repoDir)
	if err != nil {
		return nil, err
	}
	return &o, nil
}
