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

// Deployment represents a single deploy run configuration.
type Deployment struct {
	ID              string          `db:"id"`
	RepoDir         string          `db:"repo_dir"`
	Owner           string          `db:"owner"`
	Repo            string          `db:"repo"`
	Mode            string          `db:"mode"`
	WatchFilter     sql.NullString  `db:"watch_filter"`
	MaxAgents       int             `db:"max_agents"`
	MaxTurns        int             `db:"max_turns"`
	MaxBudgetUSD    float64         `db:"max_budget_usd"`
	AnalyzerModel   string          `db:"analyzer_model"`
	SkipLabel       string          `db:"skip_label"`
	AutoMerge       bool            `db:"auto_merge"`
	ReviewEnabled   bool            `db:"review_enabled"`
	ReviewMaxTurns  sql.NullInt64   `db:"review_max_turns"`
	ReviewMaxBudget sql.NullFloat64 `db:"review_max_budget"`
	TotalBudgetUSD  float64         `db:"total_budget_usd"`
	CarriedCostUSD  float64         `db:"carried_cost_usd"`
	BaseBranch      string          `db:"base_branch"`
	StartedAt       time.Time       `db:"started_at"`
}

// Task represents a single issue being worked on by an agent.
type Task struct {
	ID                int64           `db:"id"`
	DeploymentID      string          `db:"deployment_id"`
	IssueNumber       int             `db:"issue_number"`
	IssueTitle        sql.NullString  `db:"issue_title"`
	IssueBody         sql.NullString  `db:"issue_body"`
	Owner             string          `db:"owner"`
	Repo              string          `db:"repo"`
	Status            string          `db:"status"`
	Dependencies      sql.NullString  `db:"dependencies"`
	WorktreePath      sql.NullString  `db:"worktree_path"`
	Branch            sql.NullString  `db:"branch"`
	PRNumber          sql.NullInt64   `db:"pr_number"`
	CostUSD           float64         `db:"cost_usd"`
	FailureReason     sql.NullString  `db:"failure_reason"`
	FailureDetail     sql.NullString  `db:"failure_detail"`
	ReviewRisk        sql.NullString  `db:"review_risk"`
	ReviewCommentID   sql.NullInt64   `db:"review_comment_id"`
	MaxTurnsOverride  sql.NullInt64   `db:"max_turns_override"`
	MaxBudgetOverride sql.NullFloat64 `db:"max_budget_override"`
	AgentLog          sql.NullString  `db:"agent_log"`
	StartedAt         sql.NullTime    `db:"started_at"`
	CompletedAt       sql.NullTime    `db:"completed_at"`
}

// EffectiveMaxTurns returns the per-task override or the deployment default.
func (t *Task) EffectiveMaxTurns(deploy *Deployment) int {
	if t.MaxTurnsOverride.Valid {
		return int(t.MaxTurnsOverride.Int64)
	}
	return deploy.MaxTurns
}

// EffectiveMaxBudget returns the per-task override or the deployment default.
func (t *Task) EffectiveMaxBudget(deploy *Deployment) float64 {
	if t.MaxBudgetOverride.Valid {
		return t.MaxBudgetOverride.Float64
	}
	return deploy.MaxBudgetUSD
}

// HasOverrides returns true if the task has per-task overrides.
func (t *Task) HasOverrides() bool {
	return t.MaxTurnsOverride.Valid || t.MaxBudgetOverride.Valid
}

// Task status constants.
const (
	StatusQueued    = "queued"
	StatusBlocked   = "blocked"
	StatusRunning   = "running"
	StatusReview    = "review"
	StatusReviewing = "reviewing"
	StatusReviewed  = "reviewed"
	StatusDone      = "done"
	StatusBailed    = "bailed"
	StatusManual    = "manual"
	StatusStopped   = "stopped"
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
	ID             int64          `db:"id"`
	RepoScope      sql.NullString `db:"repo_scope"`
	Content        string         `db:"content"`
	Source         string         `db:"source"`
	Active         bool           `db:"active"`
	Pinned         bool           `db:"pinned"`
	TimesInjected  int            `db:"times_injected"`
	TimesHelpful   int            `db:"times_helpful"`
	TimesUnhelpful int            `db:"times_unhelpful"`
	SupersededBy   sql.NullInt64  `db:"superseded_by"`
	LastInjectedAt sql.NullTime   `db:"last_injected_at"`
	CreatedAt      time.Time      `db:"created_at"`
	UpdatedAt      time.Time      `db:"updated_at"`
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
