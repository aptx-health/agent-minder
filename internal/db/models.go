package db

import (
	"database/sql"
	"fmt"
	"time"
)

// Project represents a monitored project.
type Project struct {
	ID                    int64   `db:"id"`
	Name                  string  `db:"name"`
	GoalType              string  `db:"goal_type"`
	GoalDescription       string  `db:"goal_description"`
	RefreshIntervalSec    int     `db:"refresh_interval_sec"`
	MessageTTLSec         int     `db:"message_ttl_sec"`
	AutoEnrollWorktrees   bool    `db:"auto_enroll_worktrees"`
	MinderIdentity        string  `db:"minder_identity"`
	LLMProvider           string  `db:"llm_provider"`
	LLMModel              string  `db:"llm_model"`
	LLMSummarizerModel    string  `db:"llm_summarizer_model"`
	LLMAnalyzerModel      string  `db:"llm_analyzer_model"`
	LLMSummarizerProvider string  `db:"llm_summarizer_provider"`
	LLMAnalyzerProvider   string  `db:"llm_analyzer_provider"`
	StatusIntervalSec     int     `db:"status_interval_sec"`
	AnalysisIntervalSec   int     `db:"analysis_interval_sec"`
	IdlePauseSec          int     `db:"idle_pause_sec"`
	AnalyzerFocus         string  `db:"analyzer_focus"`
	AutopilotFilterType   string  `db:"autopilot_filter_type"`  // deprecated, unused
	AutopilotFilterValue  string  `db:"autopilot_filter_value"` // deprecated, unused
	AutopilotMaxAgents    int     `db:"autopilot_max_agents"`
	AutopilotMaxTurns     int     `db:"autopilot_max_turns"`
	AutopilotMaxBudgetUSD float64 `db:"autopilot_max_budget_usd"`
	AutopilotSkipLabel    string  `db:"autopilot_skip_label"`
	AutopilotBaseBranch   string  `db:"autopilot_base_branch"`
	CreatedAt             string  `db:"created_at"`
}

// RefreshInterval returns the refresh interval as a time.Duration.
func (p *Project) RefreshInterval() time.Duration {
	return time.Duration(p.RefreshIntervalSec) * time.Second
}

// MessageTTL returns the message TTL as a time.Duration.
func (p *Project) MessageTTL() time.Duration {
	return time.Duration(p.MessageTTLSec) * time.Second
}

// StatusInterval returns the status poll interval as a time.Duration.
// Defaults to 5 minutes if not set.
func (p *Project) StatusInterval() time.Duration {
	if p.StatusIntervalSec <= 0 {
		return 5 * time.Minute
	}
	return time.Duration(p.StatusIntervalSec) * time.Second
}

// AnalysisInterval returns the analysis poll interval as a time.Duration.
// Defaults to 30 minutes if not set.
func (p *Project) AnalysisInterval() time.Duration {
	if p.AnalysisIntervalSec <= 0 {
		return 30 * time.Minute
	}
	return time.Duration(p.AnalysisIntervalSec) * time.Second
}

// IdlePauseDuration returns the idle pause threshold as a time.Duration.
// Returns 0 if idle auto-pause is disabled.
func (p *Project) IdlePauseDuration() time.Duration {
	return time.Duration(p.IdlePauseSec) * time.Second
}

// Repo represents a monitored git repository.
type Repo struct {
	ID        int64  `db:"id"`
	ProjectID int64  `db:"project_id"`
	Path      string `db:"path"`
	ShortName string `db:"short_name"`
	Summary   string `db:"summary"`
}

// Worktree represents a git worktree within a repo.
type Worktree struct {
	ID     int64  `db:"id"`
	RepoID int64  `db:"repo_id"`
	Path   string `db:"path"`
	Branch string `db:"branch"`
}

// WorktreeWithRepo joins a worktree with its parent repo's short name,
// avoiding N+1 queries when displaying worktrees for a project.
type WorktreeWithRepo struct {
	Worktree
	RepoShortName string `db:"repo_short_name"`
}

// Topic represents a message bus topic.
type Topic struct {
	ID        int64  `db:"id"`
	ProjectID int64  `db:"project_id"`
	Name      string `db:"name"`
}

// Concern represents an active or resolved concern the minder is tracking.
type Concern struct {
	ID         int64          `db:"id"`
	ProjectID  int64          `db:"project_id"`
	Severity   string         `db:"severity"`
	Message    string         `db:"message"`
	Resolved   bool           `db:"resolved"`
	CreatedAt  string         `db:"created_at"`
	ResolvedAt sql.NullString `db:"resolved_at"`
}

// Poll represents a single poll cycle's results.
type Poll struct {
	ID             int64  `db:"id"`
	ProjectID      int64  `db:"project_id"`
	NewCommits     int    `db:"new_commits"`
	NewMessages    int    `db:"new_messages"`
	ConcernsRaised int    `db:"concerns_raised"`
	LLMResponseRaw string `db:"llm_response"`
	Tier1Response  string `db:"tier1_response"`
	Tier2Response  string `db:"tier2_response"`
	BusMessageSent string `db:"bus_message_sent"`
	PolledAt       string `db:"polled_at"`
}

// TrackedItem represents a GitHub issue or PR being tracked for a project.
type TrackedItem struct {
	ID               int64  `db:"id"`
	ProjectID        int64  `db:"project_id"`
	Source           string `db:"source"`       // "github"
	Owner            string `db:"owner"`        // repo owner
	Repo             string `db:"repo"`         // repo name
	Number           int    `db:"number"`       // issue/PR number
	ItemType         string `db:"item_type"`    // "issue" or "pull_request"
	Title            string `db:"title"`        // latest title
	State            string `db:"state"`        // "open", "closed", "merged"
	Labels           string `db:"labels"`       // comma-separated
	IsDraft          bool   `db:"is_draft"`     // PR only: true if draft
	ReviewState      string `db:"review_state"` // PR only: "approved", "changes_requested", "pending", ""
	LastStatus       string `db:"last_status"`  // compact status for TUI: "Open", "InProg", "Closd", "Mrgd", "Blckd", "Draft", "Appvd", "ChReq"
	LastCheckedAt    string `db:"last_checked_at"`
	ContentHash      string `db:"content_hash"`      // SHA-256 of body+comments+state+labels
	ObjectiveSummary string `db:"objective_summary"` // Haiku-generated objective summary
	ProgressSummary  string `db:"progress_summary"`  // Haiku-generated progress summary
	CreatedAt        string `db:"created_at"`
}

// DisplayRef returns a compact reference like "owner/repo#123".
func (t *TrackedItem) DisplayRef() string {
	return fmt.Sprintf("%s/%s#%d", t.Owner, t.Repo, t.Number)
}

// CompletedItem represents a tracked item that reached terminal state and was archived
// before being pruned. Only items with a progress summary are archived.
type CompletedItem struct {
	ID          int64  `db:"id"`
	ProjectID   int64  `db:"project_id"`
	Source      string `db:"source"`
	Owner       string `db:"owner"`
	Repo        string `db:"repo"`
	Number      int    `db:"number"`
	ItemType    string `db:"item_type"`
	Title       string `db:"title"`
	FinalStatus string `db:"final_status"` // "Closd", "Mrgd", "NotPl"
	Summary     string `db:"summary"`      // snapshot of objective + progress
	CompletedAt string `db:"completed_at"`
}

// DisplayRef returns a compact reference like "owner/repo#123".
func (c *CompletedItem) DisplayRef() string {
	return fmt.Sprintf("%s/%s#%d", c.Owner, c.Repo, c.Number)
}

// AutopilotTask represents an issue being worked on by an autopilot agent.
type AutopilotTask struct {
	ID           int64  `db:"id"`
	ProjectID    int64  `db:"project_id"`
	Owner        string `db:"owner"`
	Repo         string `db:"repo"`
	IssueNumber  int    `db:"issue_number"`
	IssueTitle   string `db:"issue_title"`
	IssueBody    string `db:"issue_body"`
	Dependencies string `db:"dependencies"` // JSON array of issue numbers
	Status       string `db:"status"`       // queued, running, done, bailed, stopped, blocked
	WorktreePath string `db:"worktree_path"`
	Branch       string `db:"branch"`
	PRNumber     int    `db:"pr_number"`
	AgentLog     string `db:"agent_log"`
	StartedAt    string `db:"started_at"`
	CompletedAt  string `db:"completed_at"`
}

// RepoEnrollment represents a cached enrollment file for a repo.
type RepoEnrollment struct {
	ID               int64  `db:"id"`
	RepoID           int64  `db:"repo_id"`
	EnrollmentYAML   string `db:"enrollment_yaml"`
	EnrolledAt       string `db:"enrolled_at"`
	ValidatedAt      string `db:"validated_at"`
	ValidationStatus string `db:"validation_status"` // "pass", "fail", "untested"
}

// LLMResponse returns the best available response: tier 2 if present, else tier 1, else raw.
func (p *Poll) LLMResponse() string {
	if p.Tier2Response != "" {
		return p.Tier2Response
	}
	if p.Tier1Response != "" {
		return p.Tier1Response
	}
	return p.LLMResponseRaw
}
