package db

import (
	"database/sql"
	"time"
)

// Default limits for agent execution.
const (
	DefaultMaxTurns     = 50
	DefaultMaxBudgetUSD = 5.0
)

// ActivationPolicy classifies why a deployment's automations (jobs.yaml
// triggers and cron schedules) are or are not installed. Persisted on the
// deployment record so Coordinator construction — including on daemon
// re-exec and restart — can consult intent without re-deriving it from the
// original CLI invocation or from whether jobs happen to already exist.
type ActivationPolicy string

const (
	// ActivationExplicit deployments run only the explicitly supplied work
	// (issues and/or a single proactive-agent job) and never install
	// jobs.yaml triggers or cron schedules.
	ActivationExplicit ActivationPolicy = "explicit"
	// ActivationAutomated deployments install jobs.yaml triggers and cron
	// schedules and remain alive; this is also used when the operator asked
	// only for a watch filter with no explicit issues, preserving the
	// historical no-issue behavior.
	ActivationAutomated ActivationPolicy = "automated"
	// ActivationHybrid deployments run explicitly supplied work AND install
	// jobs.yaml automations, because the operator explicitly requested both
	// (e.g. issue numbers plus --watch).
	ActivationHybrid ActivationPolicy = "hybrid"
)

// Deployment represents a single deploy run configuration.
type Deployment struct {
	ID               string           `db:"id"`
	RepoDir          string           `db:"repo_dir"`
	Owner            string           `db:"owner"`
	Repo             string           `db:"repo"`
	Mode             string           `db:"mode"`
	WatchFilter      sql.NullString   `db:"watch_filter"`
	MaxAgents        int              `db:"max_agents"`
	MaxTurns         int              `db:"max_turns"`
	MaxBudgetUSD     float64          `db:"max_budget_usd"`
	Runtime          string           `db:"runtime"`
	AnalyzerModel    string           `db:"analyzer_model"`
	SkipLabel        string           `db:"skip_label"`
	AutoMerge        bool             `db:"auto_merge"`
	ReviewEnabled    bool             `db:"review_enabled"`
	ReviewMaxTurns   sql.NullInt64    `db:"review_max_turns"`
	ReviewMaxBudget  sql.NullFloat64  `db:"review_max_budget"`
	TotalBudgetUSD   float64          `db:"total_budget_usd"`
	CarriedCostUSD   float64          `db:"carried_cost_usd"`
	BaseBranch       string           `db:"base_branch"`
	ActivationPolicy ActivationPolicy `db:"activation_policy"`
	StartedAt        time.Time        `db:"started_at"`
}

// Job represents a unit of work assigned to an agent.
type Job struct {
	ID           int64  `db:"id"`
	DeploymentID string `db:"deployment_id"`

	// What to run.
	Agent   string         `db:"agent"`   // e.g., "autopilot", "reviewer", "dependency-updater"
	Name    string         `db:"name"`    // e.g., "issue-42", "weekly-deps-2026-04-01"
	Runtime sql.NullString `db:"runtime"` // optional per-job runtime override
	Model   sql.NullString `db:"model"`   // optional per-job model override

	// Context (nullable for proactive agents).
	IssueNumber int            `db:"issue_number"`
	IssueTitle  sql.NullString `db:"issue_title"`
	IssueBody   sql.NullString `db:"issue_body"`
	Owner       string         `db:"owner"`
	Repo        string         `db:"repo"`

	// Lifecycle.
	Status       string         `db:"status"`
	CurrentStage sql.NullString `db:"current_stage"`
	StagesJSON   sql.NullString `db:"stages_json"`
	ResultJSON   sql.NullString `db:"result_json"`

	// Execution.
	WorktreePath sql.NullString `db:"worktree_path"`
	Branch       sql.NullString `db:"branch"`
	PRNumber     sql.NullInt64  `db:"pr_number"`
	CostUSD      float64        `db:"cost_usd"`
	AgentLog     sql.NullString `db:"agent_log"`

	// Failure.
	FailureReason sql.NullString `db:"failure_reason"`
	FailureDetail sql.NullString `db:"failure_detail"`

	// Review.
	ReviewRisk      sql.NullString `db:"review_risk"`
	ReviewCommentID sql.NullInt64  `db:"review_comment_id"`

	// Dependencies.
	Dependencies sql.NullString `db:"dependencies"`

	// Budget overrides (per-job, nullable — falls back to deployment defaults).
	MaxTurns    sql.NullInt64   `db:"max_turns"`
	MaxBudgetOv sql.NullFloat64 `db:"max_budget_usd"`

	// Provenance (which automation activated this job, nullable).
	SourceType sql.NullString `db:"source_type"` // e.g., "trigger"
	SourceName sql.NullString `db:"source_name"` // automation name
	SourceRef  sql.NullString `db:"source_ref"`  // optional matched condition (e.g., trigger filter)

	// Timestamps.
	QueuedAt    sql.NullTime `db:"queued_at"`
	StartedAt   sql.NullTime `db:"started_at"`
	CompletedAt sql.NullTime `db:"completed_at"`
}

// EffectiveMaxTurns returns the per-job override or the deployment default.
func (j *Job) EffectiveMaxTurns(deploy *Deployment) int {
	if j.MaxTurns.Valid {
		return int(j.MaxTurns.Int64)
	}
	return deploy.MaxTurns
}

// EffectiveMaxBudget returns the per-job override or the deployment default.
func (j *Job) EffectiveMaxBudget(deploy *Deployment) float64 {
	if j.MaxBudgetOv.Valid {
		return j.MaxBudgetOv.Float64
	}
	return deploy.MaxBudgetUSD
}

// HasOverrides returns true if the job has per-job overrides.
func (j *Job) HasOverrides() bool {
	return j.MaxTurns.Valid || j.MaxBudgetOv.Valid
}

// EffectiveRuntime returns the per-job runtime override or the deployment
// default.
func (j *Job) EffectiveRuntime(deploy *Deployment) string {
	if j.Runtime.Valid && j.Runtime.String != "" {
		return j.Runtime.String
	}
	if deploy != nil && deploy.Runtime != "" {
		return deploy.Runtime
	}
	return "claude-code"
}

// EffectiveModel returns the per-job model override, if one was configured.
// Empty means the selected runtime should use its own default model.
func (j *Job) EffectiveModel() string {
	if j.Model.Valid {
		return j.Model.String
	}
	return ""
}

// Job status constants.
const (
	StatusQueued    = "queued"
	StatusBlocked   = "blocked"
	StatusRunning   = "running"
	StatusReview    = "review"
	StatusReviewing = "reviewing"
	StatusReviewed  = "reviewed"
	StatusDone      = "done"
	StatusFailed    = "failed"
	StatusBailed    = "bailed"
	StatusManual    = "manual"
	StatusStopped   = "stopped"
	StatusWaiting   = "waiting" // waiting for usage limit reset
)

// DepGraph stores the dependency graph for a deployment.
type DepGraph struct {
	DeploymentID string         `db:"deployment_id"`
	GraphJSON    string         `db:"graph_json"`
	OptionName   sql.NullString `db:"option_name"`
	Reasoning    sql.NullString `db:"reasoning"`
	Confidence   sql.NullString `db:"confidence"`
	CreatedAt    time.Time      `db:"created_at"`
}

// Lesson stores a persistent piece of feedback or knowledge.
type Lesson struct {
	ID              int64          `db:"id"`
	RepoScope       sql.NullString `db:"repo_scope"`
	Content         string         `db:"content"`
	Source          string         `db:"source"`
	Active          bool           `db:"active"`
	Pinned          bool           `db:"pinned"`
	TimesInjected   int            `db:"times_injected"`
	TimesHelpful    int            `db:"times_helpful"`
	TimesUnhelpful  int            `db:"times_unhelpful"`
	SupersededBy    sql.NullInt64  `db:"superseded_by"`
	LastInjectedAt  sql.NullTime   `db:"last_injected_at"`
	LastHelpfulAt   sql.NullTime   `db:"last_helpful_at"`
	LastUnhelpfulAt sql.NullTime   `db:"last_unhelpful_at"`
	CreatedAt       time.Time      `db:"created_at"`
	UpdatedAt       time.Time      `db:"updated_at"`
}

// EffectivenessRatio returns the ratio of helpful to total outcomes.
// Returns 0.5 if no outcomes recorded (neutral).
func (l *Lesson) EffectivenessRatio() float64 {
	total := l.TimesHelpful + l.TimesUnhelpful
	if total == 0 {
		return 0.5
	}
	return float64(l.TimesHelpful) / float64(total)
}

// JobSchedule tracks a scheduled or triggered job definition.
type JobSchedule struct {
	Name         string          `db:"name"`
	DeploymentID string          `db:"deployment_id"`
	CronExpr     sql.NullString  `db:"cron_expr"`
	TriggerExpr  sql.NullString  `db:"trigger_expr"`
	Agent        string          `db:"agent"`
	Runtime      sql.NullString  `db:"runtime"`
	Model        sql.NullString  `db:"model"`
	Description  sql.NullString  `db:"description"`
	Budget       sql.NullFloat64 `db:"budget"`
	MaxTurns     sql.NullInt64   `db:"max_turns"`
	Enabled      bool            `db:"enabled"`
	LastRunAt    sql.NullTime    `db:"last_run_at"`
	NextRunAt    sql.NullTime    `db:"next_run_at"`
	CreatedAt    time.Time       `db:"created_at"`
}

// AgentRun is a durable record of a single agent execution within a job: one
// row per (job, stage, attempt). It captures per-run truth — agent, runtime,
// model, session, live step count vs. exact final turns, cost, effective
// limits, final text, and log identity — that the aggregate jobs row and raw
// logs cannot express when a job spans several stages, retries, or resumes.
type AgentRun struct {
	ID    int64  `db:"id"`
	JobID int64  `db:"job_id"`
	Stage string `db:"stage"`

	// Attempt counts executions of the same stage (retries, resumes) starting at 1.
	Attempt int `db:"attempt"`

	// What ran.
	Agent          string         `db:"agent"`
	Runtime        sql.NullString `db:"runtime"`
	Model          sql.NullString `db:"model"`
	RuntimeVersion sql.NullString `db:"runtime_version"`
	SessionID      sql.NullString `db:"session_id"`

	// Outcome.
	Status        string         `db:"status"` // running/success/failed/bailed/manual/usage_limit
	StopReason    sql.NullString `db:"stop_reason"`
	FailureDetail sql.NullString `db:"failure_detail"`

	// Progress vs. final truth.
	StepCount  int     `db:"step_count"`  // live assistant steps observed for this run
	FinalTurns int     `db:"final_turns"` // exact turn count from the runtime result
	CostUSD    float64 `db:"cost_usd"`

	// Effective limits applied to this run.
	MaxTurns     sql.NullInt64   `db:"max_turns"`
	MaxBudgetUSD sql.NullFloat64 `db:"max_budget_usd"`

	// Output + log identity.
	FinalText sql.NullString `db:"final_text"`
	LogPath   sql.NullString `db:"log_path"`

	// Timestamps.
	StartedAt      sql.NullTime `db:"started_at"`
	LastActivityAt sql.NullTime `db:"last_activity_at"`
	CompletedAt    sql.NullTime `db:"completed_at"`
}

// Agent run status constants.
const (
	RunStatusRunning    = "running"
	RunStatusSuccess    = "success"
	RunStatusFailed     = "failed"
	RunStatusBailed     = "bailed"
	RunStatusManual     = "manual"
	RunStatusUsageLimit = "usage_limit"
)

// RepoOnboarding stores cached onboarding YAML for a repository.
type RepoOnboarding struct {
	RepoDir            string         `db:"repo_dir"`
	Owner              string         `db:"owner"`
	Repo               string         `db:"repo"`
	YAMLContent        string         `db:"yaml_content"`
	ValidationStatus   string         `db:"validation_status"`
	ValidationFailures sql.NullString `db:"validation_failures"`
	ScannedAt          time.Time      `db:"scanned_at"`
}
