package db

import (
	"database/sql"
	"fmt"
	"time"
)

// Project represents a monitored project.
type Project struct {
	ID                   int64  `db:"id"`
	Name                 string `db:"name"`
	GoalType             string `db:"goal_type"`
	GoalDescription      string `db:"goal_description"`
	RefreshIntervalSec   int    `db:"refresh_interval_sec"`
	MessageTTLSec        int    `db:"message_ttl_sec"`
	AutoEnrollWorktrees  bool   `db:"auto_enroll_worktrees"`
	MinderIdentity       string `db:"minder_identity"`
	LLMProvider          string `db:"llm_provider"`
	LLMModel             string `db:"llm_model"`
	LLMSummarizerModel   string `db:"llm_summarizer_model"`
	LLMAnalyzerModel     string `db:"llm_analyzer_model"`
	CreatedAt            string `db:"created_at"`
}

// RefreshInterval returns the refresh interval as a time.Duration.
func (p *Project) RefreshInterval() time.Duration {
	return time.Duration(p.RefreshIntervalSec) * time.Second
}

// MessageTTL returns the message TTL as a time.Duration.
func (p *Project) MessageTTL() time.Duration {
	return time.Duration(p.MessageTTLSec) * time.Second
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
	ID            int64  `db:"id"`
	ProjectID     int64  `db:"project_id"`
	Source        string `db:"source"`         // "github"
	Owner         string `db:"owner"`          // repo owner
	Repo          string `db:"repo"`           // repo name
	Number        int    `db:"number"`         // issue/PR number
	ItemType      string `db:"item_type"`      // "issue" or "pull_request"
	Title         string `db:"title"`          // latest title
	State         string `db:"state"`          // "open", "closed", "merged"
	Labels        string `db:"labels"`         // comma-separated
	LastStatus       string `db:"last_status"`        // compact status for TUI: "Open", "InProg", "Closd", "Mrgd", "Blckd"
	LastCheckedAt    string `db:"last_checked_at"`
	ContentHash      string `db:"content_hash"`       // SHA-256 of body+comments+state+labels
	ObjectiveSummary string `db:"objective_summary"`  // Haiku-generated objective summary
	ProgressSummary  string `db:"progress_summary"`   // Haiku-generated progress summary
	CreatedAt        string `db:"created_at"`
}

// DisplayRef returns a compact reference like "owner/repo#123".
func (t *TrackedItem) DisplayRef() string {
	return fmt.Sprintf("%s/%s#%d", t.Owner, t.Repo, t.Number)
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
