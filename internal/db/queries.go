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
	runtimeName := d.Runtime
	if runtimeName == "" {
		runtimeName = "claude-code"
	}
	policy := d.ActivationPolicy
	if policy == "" {
		// Preserves pre-migration behavior for callers that don't set an
		// explicit policy: unconditionally load jobs.yaml automations.
		policy = ActivationAutomated
	}
	_, err := s.db.Exec(`INSERT INTO deployments
		(id, repo_dir, owner, repo, mode, watch_filter, max_agents, max_turns,
		 max_budget_usd, runtime, analyzer_model, skip_label, auto_merge, review_enabled,
		 review_max_turns, review_max_budget, total_budget_usd, carried_cost_usd, base_branch,
		 activation_policy)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.RepoDir, d.Owner, d.Repo, d.Mode, d.WatchFilter,
		d.MaxAgents, d.MaxTurns, d.MaxBudgetUSD, runtimeName, d.AnalyzerModel,
		d.SkipLabel, d.AutoMerge, d.ReviewEnabled,
		d.ReviewMaxTurns, d.ReviewMaxBudget,
		d.TotalBudgetUSD, d.CarriedCostUSD, d.BaseBranch, policy)
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

// jobInsertColumns lists the columns shared by CreateJob and BulkCreateJobs,
// so a new job field only has to be added to one INSERT statement shape.
const jobInsertColumns = `deployment_id, agent, name, runtime, model, max_turns, max_budget_usd,
	issue_number, issue_title, issue_body, owner, repo, status, dependencies, stages_json,
	source_type, source_name, source_ref`

// jobInsertArgs returns the bind args matching jobInsertColumns, in order.
func jobInsertArgs(j *Job) []interface{} {
	return []interface{}{
		j.DeploymentID, j.Agent, j.Name, j.Runtime, j.Model, j.MaxTurns, j.MaxBudgetOv,
		j.IssueNumber, j.IssueTitle, j.IssueBody, j.Owner, j.Repo, j.Status, j.Dependencies, j.StagesJSON,
		j.SourceType, j.SourceName, j.SourceRef,
	}
}

// CreateJob inserts a new job.
func (s *Store) CreateJob(j *Job) error {
	return createJob(s.db, j)
}

// CreateJobTx inserts a new job inside an existing transaction, so a durable
// discovery event can commit atomically with the job row (Expedition IV R-1).
func CreateJobTx(tx *sqlx.Tx, j *Job) error {
	return createJob(tx, j)
}

func createJob(e sqlx.Execer, j *Job) error {
	res, err := e.Exec(fmt.Sprintf(`INSERT INTO jobs (%s)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, jobInsertColumns),
		jobInsertArgs(j)...)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	j.ID = id
	return nil
}

// BulkCreateJobs inserts multiple jobs, ignoring duplicates. Shares the same
// column set as CreateJob so job fields never silently diverge between the
// two insert paths.
func (s *Store) BulkCreateJobs(jobs []*Job) error {
	for _, j := range jobs {
		_, err := s.db.Exec(fmt.Sprintf(`INSERT OR IGNORE INTO jobs (%s)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, jobInsertColumns),
			jobInsertArgs(j)...)
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

// CountActiveJobsBySource returns the number of jobs for a deployment with the
// given source_type/source_name provenance whose status is queued, running,
// blocked, or reviewing. Used for scheduler dedup instead of name-prefix
// matching.
func (s *Store) CountActiveJobsBySource(deploymentID, sourceType, sourceName string) (int, error) {
	var count int
	err := s.db.Get(&count, `SELECT COUNT(*) FROM jobs
		WHERE deployment_id = ? AND source_type = ? AND source_name = ?
		AND status IN (?, ?, ?, ?)`,
		deploymentID, sourceType, sourceName,
		StatusQueued, StatusRunning, StatusBlocked, StatusReviewing)
	return count, err
}

// GetJobsByRepo returns all jobs for a given owner/repo across all deployments,
// most recent first.
func (s *Store) GetJobsByRepo(owner, repo string) ([]*Job, error) {
	var jobs []*Job
	err := s.db.Select(&jobs,
		"SELECT * FROM jobs WHERE owner = ? AND repo = ? ORDER BY id DESC", owner, repo)
	return jobs, err
}

// activeBranchClaimStatuses are the job statuses that hold a live claim on a
// branch and its worktree. Running jobs have in-flight working state; jobs in
// review/reviewing/reviewed have an open PR backed by the branch (A-4); waiting
// and manual jobs are paused mid-lifecycle and expect their worktree intact.
// A second job deriving the same branch must not destroy any of these.
var activeBranchClaimStatuses = []string{
	StatusRunning, StatusReview, StatusReviewing, StatusReviewed, StatusWaiting, StatusManual,
}

// ActiveJobOwningBranch returns the first non-terminal job in the same
// owner/repo that owns the given branch, excluding excludeID. It returns
// (nil, nil) when the branch is unclaimed. Callers use this to block a second
// job from destroying an active job's worktree on a branch collision.
func (s *Store) ActiveJobOwningBranch(owner, repo, branch string, excludeID int64) (*Job, error) {
	if branch == "" {
		return nil, nil
	}
	placeholders := make([]string, len(activeBranchClaimStatuses))
	args := []interface{}{owner, repo, branch, excludeID}
	for i, st := range activeBranchClaimStatuses {
		placeholders[i] = "?"
		args = append(args, st)
	}
	query := fmt.Sprintf(`SELECT * FROM jobs
		WHERE owner = ? AND repo = ? AND branch = ? AND id != ?
		AND status IN (%s) ORDER BY id LIMIT 1`, strings.Join(placeholders, ","))
	var j Job
	err := s.db.Get(&j, query, args...)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
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

// The exported *Tx variants below mirror their Store methods so a state write
// can share a transaction with the durable event describing it (Expedition IV
// R-1). Both shapes delegate to one statement, so the SQL can never diverge.

// UpdateJobStatus updates the status of a job.
func (s *Store) UpdateJobStatus(id int64, status string) error {
	return updateJobStatus(s.db, id, status)
}

// UpdateJobStatusTx is UpdateJobStatus inside an existing transaction.
func UpdateJobStatusTx(tx *sqlx.Tx, id int64, status string) error {
	return updateJobStatus(tx, id, status)
}

func updateJobStatus(e sqlx.Execer, id int64, status string) error {
	_, err := e.Exec("UPDATE jobs SET status = ? WHERE id = ?", status, id)
	return err
}

// UpdateJobRunning marks a job as running with a start time.
func (s *Store) UpdateJobRunning(id int64) error {
	return updateJobRunning(s.db, id)
}

// UpdateJobRunningTx is UpdateJobRunning inside an existing transaction.
func UpdateJobRunningTx(tx *sqlx.Tx, id int64) error {
	return updateJobRunning(tx, id)
}

func updateJobRunning(e sqlx.Execer, id int64) error {
	_, err := e.Exec("UPDATE jobs SET status = 'running', started_at = ? WHERE id = ?",
		time.Now().UTC(), id)
	return err
}

// UpdateJobStage updates the current stage and stages JSON for a job.
func (s *Store) UpdateJobStage(id int64, stage string, stagesJSON string) error {
	return updateJobStage(s.db, id, stage, stagesJSON)
}

// UpdateJobStageTx is UpdateJobStage inside an existing transaction.
func UpdateJobStageTx(tx *sqlx.Tx, id int64, stage string, stagesJSON string) error {
	return updateJobStage(tx, id, stage, stagesJSON)
}

func updateJobStage(e sqlx.Execer, id int64, stage string, stagesJSON string) error {
	_, err := e.Exec("UPDATE jobs SET current_stage = ?, stages_json = ? WHERE id = ?",
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
	return updateJobFailure(s.db, id, reason, detail)
}

// UpdateJobFailureTx is UpdateJobFailure inside an existing transaction.
func UpdateJobFailureTx(tx *sqlx.Tx, id int64, reason, detail string) error {
	return updateJobFailure(tx, id, reason, detail)
}

func updateJobFailure(e sqlx.Execer, id int64, reason, detail string) error {
	_, err := e.Exec(`UPDATE jobs SET status = 'bailed', failure_reason = ?,
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
	return updateJobReview(s.db, id, risk, commentID)
}

// UpdateJobReviewTx is UpdateJobReview inside an existing transaction.
func UpdateJobReviewTx(tx *sqlx.Tx, id int64, risk string, commentID int64) error {
	return updateJobReview(tx, id, risk, commentID)
}

func updateJobReview(e sqlx.Execer, id int64, risk string, commentID int64) error {
	_, err := e.Exec("UPDATE jobs SET review_risk = ?, review_comment_id = ? WHERE id = ?",
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
	return completeJob(s.db, id, status)
}

// CompleteJobTx is CompleteJob inside an existing transaction.
func CompleteJobTx(tx *sqlx.Tx, id int64, status string) error {
	return completeJob(tx, id, status)
}

func completeJob(e sqlx.Execer, id int64, status string) error {
	_, err := e.Exec("UPDATE jobs SET status = ?, completed_at = ? WHERE id = ?",
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

// --- Agent Runs ---

// StartAgentRun inserts a new agent_runs row for a starting (job, stage,
// attempt) and populates r.ID. started_at and last_activity_at are set to now.
func (s *Store) StartAgentRun(r *AgentRun) error {
	now := time.Now().UTC()
	if r.Status == "" {
		r.Status = RunStatusRunning
	}
	res, err := s.db.Exec(`INSERT INTO agent_runs
		(job_id, stage, attempt, agent, runtime, model, session_id, status,
		 max_turns, max_budget_usd, log_path, started_at, last_activity_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.JobID, r.Stage, r.Attempt, r.Agent, r.Runtime, r.Model, r.SessionID,
		r.Status, r.MaxTurns, r.MaxBudgetUSD, r.LogPath, now, now)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	r.ID = id
	r.StartedAt = sql.NullTime{Time: now, Valid: true}
	r.LastActivityAt = sql.NullTime{Time: now, Valid: true}
	return nil
}

// TouchAgentRun records live progress for an in-flight run: it advances the
// step count and refreshes last_activity_at. Called as the runtime streams
// assistant steps, so the read API can surface progress before the run ends.
func (s *Store) TouchAgentRun(id int64, stepCount int) error {
	_, err := s.db.Exec(
		"UPDATE agent_runs SET step_count = ?, last_activity_at = ? WHERE id = ?",
		stepCount, time.Now().UTC(), id)
	return err
}

// CompleteAgentRun finalizes a run row with its terminal status, stop reason,
// session, exact final turns, cost, final text, and step count.
func (s *Store) CompleteAgentRun(id int64, f AgentRunResult) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE agent_runs SET
		status = ?, stop_reason = ?, failure_detail = ?, session_id = ?,
		final_text = ?, final_turns = ?, cost_usd = ?, step_count = ?,
		last_activity_at = ?, completed_at = ? WHERE id = ?`,
		f.Status, toNullString(f.StopReason), toNullString(f.FailureDetail),
		toNullString(f.SessionID), toNullString(f.FinalText),
		f.FinalTurns, f.CostUSD, f.StepCount, now, now, id)
	return err
}

// AgentRunResult carries the terminal fields written when a run completes.
type AgentRunResult struct {
	Status        string
	StopReason    string
	FailureDetail string
	SessionID     string
	FinalText     string
	FinalTurns    int
	CostUSD       float64
	StepCount     int
}

// GetAgentRuns returns all runs for a job, oldest first.
func (s *Store) GetAgentRuns(jobID int64) ([]*AgentRun, error) {
	var runs []*AgentRun
	err := s.db.Select(&runs, "SELECT * FROM agent_runs WHERE job_id = ? ORDER BY id", jobID)
	return runs, err
}

// GetAgentRun returns a single run by ID.
func (s *Store) GetAgentRun(id int64) (*AgentRun, error) {
	var r AgentRun
	err := s.db.Get(&r, "SELECT * FROM agent_runs WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// toNullString maps an empty string to NULL, otherwise a valid string.
func toNullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
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
// Sorted by pinned first, then by recency-weighted effectiveness.
// We do decay-weighted scoring in Go (SelectLessons) since SQLite lacks EXP().
// Here we just sort: pinned first, then by effectiveness ratio, then by recency of feedback.
func (s *Store) GetActiveLessons(repoScope string) ([]*Lesson, error) {
	orderClause := `ORDER BY pinned DESC,
		CASE WHEN times_helpful + times_unhelpful = 0 THEN 0.5
		     ELSE CAST(times_helpful AS REAL) / (times_helpful + times_unhelpful)
		END DESC,
		COALESCE(last_helpful_at, last_injected_at, created_at) DESC`

	var lessons []*Lesson
	if repoScope == "" {
		err := s.db.Select(&lessons,
			"SELECT * FROM lessons WHERE active = 1 AND superseded_by IS NULL "+orderClause)
		return lessons, err
	}
	// Only return lessons scoped to this specific repo. Global (unscoped) lessons
	// are excluded to prevent cross-repo leakage (e.g., Go lint lessons injected
	// into JS projects).
	err := s.db.Select(&lessons,
		`SELECT * FROM lessons WHERE active = 1 AND superseded_by IS NULL
		 AND repo_scope = ? `+orderClause, repoScope)
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

// DeleteLesson permanently removes a lesson and its job_lessons references.
func (s *Store) DeleteLesson(id int64) error {
	// Delete FK references first, then the lesson itself.
	_, _ = s.db.Exec("DELETE FROM job_lessons WHERE lesson_id = ?", id)
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

// UpdateLessonOutcome increments helpful or unhelpful counts for all lessons injected into a job.
// This is the binary (job-level) fallback — used when per-lesson reviewer feedback isn't available.
func (s *Store) UpdateLessonOutcome(jobID int64, helpful bool) error {
	now := time.Now().UTC()
	col := "times_helpful"
	tsCol := "last_helpful_at"
	if !helpful {
		col = "times_unhelpful"
		tsCol = "last_unhelpful_at"
	}
	query := fmt.Sprintf(`UPDATE lessons SET %s = %s + 1, %s = ? WHERE id IN
		(SELECT lesson_id FROM job_lessons WHERE job_id = ?)`, col, col, tsCol)
	_, err := s.db.Exec(query, now, jobID)
	return err
}

// UpdateLessonFeedback records per-lesson feedback from the reviewer.
// This is more precise than UpdateLessonOutcome — it scores individual lessons.
func (s *Store) UpdateLessonFeedback(lessonID int64, helpful bool) error {
	now := time.Now().UTC()
	if helpful {
		_, err := s.db.Exec(
			`UPDATE lessons SET times_helpful = times_helpful + 1, last_helpful_at = ?, updated_at = ? WHERE id = ?`,
			now, now, lessonID)
		return err
	}
	_, err := s.db.Exec(
		`UPDATE lessons SET times_unhelpful = times_unhelpful + 1, last_unhelpful_at = ?, updated_at = ? WHERE id = ?`,
		now, now, lessonID)
	return err
}

// GetJobLessons returns the lessons that were injected into a specific job.
func (s *Store) GetJobLessons(jobID int64) ([]*Lesson, error) {
	var lessons []*Lesson
	err := s.db.Select(&lessons,
		`SELECT l.* FROM lessons l
		 JOIN job_lessons jl ON l.id = jl.lesson_id
		 WHERE jl.job_id = ?`, jobID)
	return lessons, err
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

// --- Job Schedules ---

// UpsertSchedule inserts or updates a job schedule, keyed by
// (deployment_id, name). On conflict it refreshes the definition columns but
// preserves last_run_at and created_at, so re-syncing a config never discards
// last-run history.
func (s *Store) UpsertSchedule(js *JobSchedule) error {
	_, err := s.db.Exec(`INSERT INTO job_schedules
		(name, deployment_id, cron_expr, trigger_expr, agent, runtime, model, description, budget, max_turns, enabled, next_run_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(deployment_id, name) DO UPDATE SET
			cron_expr = excluded.cron_expr,
			trigger_expr = excluded.trigger_expr,
			agent = excluded.agent,
			runtime = excluded.runtime,
			model = excluded.model,
			description = excluded.description,
			budget = excluded.budget,
			max_turns = excluded.max_turns,
			enabled = excluded.enabled,
			next_run_at = excluded.next_run_at`,
		js.Name, js.DeploymentID, js.CronExpr, js.TriggerExpr,
		js.Agent, js.Runtime, js.Model, js.Description, js.Budget, js.MaxTurns, js.Enabled, js.NextRunAt)
	return err
}

// GetSchedules returns all schedules for a deployment.
func (s *Store) GetSchedules(deploymentID string) ([]*JobSchedule, error) {
	var schedules []*JobSchedule
	err := s.db.Select(&schedules,
		"SELECT * FROM job_schedules WHERE deployment_id = ? ORDER BY name", deploymentID)
	return schedules, err
}

// GetEnabledSchedules returns enabled schedules for a deployment.
func (s *Store) GetEnabledSchedules(deploymentID string) ([]*JobSchedule, error) {
	var schedules []*JobSchedule
	err := s.db.Select(&schedules,
		"SELECT * FROM job_schedules WHERE deployment_id = ? AND enabled = 1 ORDER BY name", deploymentID)
	return schedules, err
}

// GetSchedule retrieves a single schedule scoped to a deployment.
func (s *Store) GetSchedule(deploymentID, name string) (*JobSchedule, error) {
	var js JobSchedule
	err := s.db.Get(&js,
		"SELECT * FROM job_schedules WHERE deployment_id = ? AND name = ?", deploymentID, name)
	if err != nil {
		return nil, err
	}
	return &js, nil
}

// UpdateScheduleRun records that a schedule just fired, scoped to a deployment.
func (s *Store) UpdateScheduleRun(deploymentID, name string, lastRun, nextRun time.Time) error {
	_, err := s.db.Exec(
		"UPDATE job_schedules SET last_run_at = ?, next_run_at = ? WHERE deployment_id = ? AND name = ?",
		lastRun, nextRun, deploymentID, name)
	return err
}

// DeleteSchedule removes a schedule scoped to a deployment.
func (s *Store) DeleteSchedule(deploymentID, name string) error {
	_, err := s.db.Exec(
		"DELETE FROM job_schedules WHERE deployment_id = ? AND name = ?", deploymentID, name)
	return err
}

// SetScheduleEnabled toggles the enabled flag for a schedule within a deployment.
func (s *Store) SetScheduleEnabled(deploymentID, name string, enabled bool) error {
	_, err := s.db.Exec(
		"UPDATE job_schedules SET enabled = ? WHERE deployment_id = ? AND name = ?",
		enabled, deploymentID, name)
	return err
}

// RenameRepo updates all references from oldOwner/oldRepo to newOwner/newRepo
// across deployments, jobs, and onboarding tables. Returns the total number of
// rows updated.
func (s *Store) RenameRepo(oldOwner, oldRepo, newOwner, newRepo string) (int64, error) {
	var total int64
	tables := []string{"deployments", "jobs", "repo_onboarding"}
	for _, table := range tables {
		res, err := s.db.Exec(
			fmt.Sprintf("UPDATE %s SET owner = ?, repo = ? WHERE owner = ? AND repo = ?", table),
			newOwner, newRepo, oldOwner, oldRepo)
		if err != nil {
			return total, fmt.Errorf("rename %s: %w", table, err)
		}
		n, _ := res.RowsAffected()
		total += n
	}

	// Also update lesson repo_scope.
	oldScope := oldOwner + "/" + oldRepo
	newScope := newOwner + "/" + newRepo
	res, err := s.db.Exec(
		"UPDATE lessons SET repo_scope = ? WHERE repo_scope = ?", newScope, oldScope)
	if err != nil {
		return total, fmt.Errorf("rename lessons: %w", err)
	}
	n, _ := res.RowsAffected()
	total += n

	return total, nil
}

// HasRepo returns true if any deployments or jobs exist for the given owner/repo.
func (s *Store) HasRepo(owner, repo string) (bool, error) {
	var count int
	err := s.db.Get(&count,
		"SELECT COUNT(*) FROM deployments WHERE owner = ? AND repo = ?", owner, repo)
	return count > 0, err
}

// FindRepoByDir returns the owner/repo stored for a given repo_dir, if any.
func (s *Store) FindRepoByDir(repoDir string) (string, string, error) {
	var d struct {
		Owner string `db:"owner"`
		Repo  string `db:"repo"`
	}
	err := s.db.Get(&d,
		"SELECT owner, repo FROM deployments WHERE repo_dir = ? ORDER BY started_at DESC LIMIT 1", repoDir)
	if err != nil {
		return "", "", err
	}
	return d.Owner, d.Repo, nil
}
