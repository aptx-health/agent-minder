package db

import (
	"database/sql"
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
	LLMResponse    string `db:"llm_response"`
	PolledAt       string `db:"polled_at"`
}
