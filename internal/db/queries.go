package db

import (
	"fmt"

	"github.com/jmoiron/sqlx"
)

// Store wraps the database connection and provides CRUD operations.
type Store struct {
	db *sqlx.DB
}

// NewStore creates a new Store from an open database.
func NewStore(db *sqlx.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying sqlx.DB for advanced queries.
func (s *Store) DB() *sqlx.DB {
	return s.db
}

// --- Projects ---

// CreateProject inserts a new project and returns it with its ID populated.
func (s *Store) CreateProject(p *Project) error {
	result, err := s.db.NamedExec(`
		INSERT INTO projects (name, goal_type, goal_description, refresh_interval_sec,
			message_ttl_sec, auto_enroll_worktrees, minder_identity, llm_provider, llm_model,
			llm_summarizer_model, llm_analyzer_model)
		VALUES (:name, :goal_type, :goal_description, :refresh_interval_sec,
			:message_ttl_sec, :auto_enroll_worktrees, :minder_identity, :llm_provider, :llm_model,
			:llm_summarizer_model, :llm_analyzer_model)
	`, p)
	if err != nil {
		return fmt.Errorf("insert project: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("last insert id: %w", err)
	}
	p.ID = id
	return nil
}

// GetProject loads a project by name.
func (s *Store) GetProject(name string) (*Project, error) {
	var p Project
	if err := s.db.Get(&p, "SELECT * FROM projects WHERE name = ?", name); err != nil {
		return nil, fmt.Errorf("get project %q: %w", name, err)
	}
	return &p, nil
}

// GetProjectByID loads a project by ID.
func (s *Store) GetProjectByID(id int64) (*Project, error) {
	var p Project
	if err := s.db.Get(&p, "SELECT * FROM projects WHERE id = ?", id); err != nil {
		return nil, fmt.Errorf("get project id=%d: %w", id, err)
	}
	return &p, nil
}

// ListProjects returns all projects.
func (s *Store) ListProjects() ([]Project, error) {
	var projects []Project
	if err := s.db.Select(&projects, "SELECT * FROM projects ORDER BY name"); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return projects, nil
}

// UpdateProject updates a project's mutable fields.
func (s *Store) UpdateProject(p *Project) error {
	_, err := s.db.NamedExec(`
		UPDATE projects SET
			goal_type = :goal_type,
			goal_description = :goal_description,
			refresh_interval_sec = :refresh_interval_sec,
			message_ttl_sec = :message_ttl_sec,
			auto_enroll_worktrees = :auto_enroll_worktrees,
			minder_identity = :minder_identity,
			llm_provider = :llm_provider,
			llm_model = :llm_model,
			llm_summarizer_model = :llm_summarizer_model,
			llm_analyzer_model = :llm_analyzer_model
		WHERE id = :id
	`, p)
	return err
}

// DeleteProject removes a project and all associated data (cascading).
func (s *Store) DeleteProject(id int64) error {
	_, err := s.db.Exec("DELETE FROM projects WHERE id = ?", id)
	return err
}

// --- Repos ---

// AddRepo inserts a repo for a project.
func (s *Store) AddRepo(r *Repo) error {
	result, err := s.db.NamedExec(`
		INSERT INTO repos (project_id, path, short_name, summary)
		VALUES (:project_id, :path, :short_name, :summary)
	`, r)
	if err != nil {
		return fmt.Errorf("insert repo: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("last insert id: %w", err)
	}
	r.ID = id
	return nil
}

// GetRepos returns all repos for a project.
func (s *Store) GetRepos(projectID int64) ([]Repo, error) {
	var repos []Repo
	if err := s.db.Select(&repos, "SELECT * FROM repos WHERE project_id = ? ORDER BY short_name", projectID); err != nil {
		return nil, fmt.Errorf("get repos: %w", err)
	}
	return repos, nil
}

// DeleteRepo removes a repo and its worktrees.
func (s *Store) DeleteRepo(id int64) error {
	_, err := s.db.Exec("DELETE FROM repos WHERE id = ?", id)
	return err
}

// --- Worktrees ---

// AddWorktree inserts a worktree for a repo.
func (s *Store) AddWorktree(w *Worktree) error {
	result, err := s.db.NamedExec(`
		INSERT INTO worktrees (repo_id, path, branch)
		VALUES (:repo_id, :path, :branch)
	`, w)
	if err != nil {
		return fmt.Errorf("insert worktree: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("last insert id: %w", err)
	}
	w.ID = id
	return nil
}

// GetWorktrees returns all worktrees for a repo.
func (s *Store) GetWorktrees(repoID int64) ([]Worktree, error) {
	var wts []Worktree
	if err := s.db.Select(&wts, "SELECT * FROM worktrees WHERE repo_id = ? ORDER BY branch", repoID); err != nil {
		return nil, fmt.Errorf("get worktrees: %w", err)
	}
	return wts, nil
}

// ReplaceWorktrees deletes all worktrees for a repo and inserts the new ones.
func (s *Store) ReplaceWorktrees(repoID int64, wts []Worktree) error {
	tx, err := s.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM worktrees WHERE repo_id = ?", repoID); err != nil {
		return err
	}
	for i := range wts {
		wts[i].RepoID = repoID
		result, err := tx.NamedExec(`
			INSERT INTO worktrees (repo_id, path, branch)
			VALUES (:repo_id, :path, :branch)
		`, &wts[i])
		if err != nil {
			return err
		}
		id, _ := result.LastInsertId()
		wts[i].ID = id
	}
	return tx.Commit()
}

// --- Topics ---

// AddTopic inserts a topic for a project.
func (s *Store) AddTopic(t *Topic) error {
	result, err := s.db.NamedExec(`
		INSERT INTO topics (project_id, name)
		VALUES (:project_id, :name)
	`, t)
	if err != nil {
		return fmt.Errorf("insert topic: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("last insert id: %w", err)
	}
	t.ID = id
	return nil
}

// GetTopics returns all topics for a project.
func (s *Store) GetTopics(projectID int64) ([]Topic, error) {
	var topics []Topic
	if err := s.db.Select(&topics, "SELECT * FROM topics WHERE project_id = ? ORDER BY name", projectID); err != nil {
		return nil, fmt.Errorf("get topics: %w", err)
	}
	return topics, nil
}

// --- Concerns ---

// AddConcern inserts a new concern.
func (s *Store) AddConcern(c *Concern) error {
	result, err := s.db.NamedExec(`
		INSERT INTO concerns (project_id, severity, message)
		VALUES (:project_id, :severity, :message)
	`, c)
	if err != nil {
		return fmt.Errorf("insert concern: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("last insert id: %w", err)
	}
	c.ID = id
	return nil
}

// ActiveConcerns returns unresolved concerns for a project.
func (s *Store) ActiveConcerns(projectID int64) ([]Concern, error) {
	var concerns []Concern
	if err := s.db.Select(&concerns, `
		SELECT * FROM concerns
		WHERE project_id = ? AND resolved = 0
		ORDER BY created_at DESC
	`, projectID); err != nil {
		return nil, fmt.Errorf("active concerns: %w", err)
	}
	return concerns, nil
}

// ResolveConcern marks a concern as resolved.
func (s *Store) ResolveConcern(id int64) error {
	_, err := s.db.Exec(`
		UPDATE concerns SET resolved = 1, resolved_at = datetime('now')
		WHERE id = ?
	`, id)
	return err
}

// UpdateConcernSeverity changes the severity level of an existing concern.
func (s *Store) UpdateConcernSeverity(id int64, severity string) error {
	_, err := s.db.Exec(`UPDATE concerns SET severity = ? WHERE id = ?`, severity, id)
	return err
}

// --- Polls ---

// RecordPoll inserts a poll result.
func (s *Store) RecordPoll(p *Poll) error {
	result, err := s.db.NamedExec(`
		INSERT INTO polls (project_id, new_commits, new_messages, concerns_raised, llm_response,
			tier1_response, tier2_response, bus_message_sent)
		VALUES (:project_id, :new_commits, :new_messages, :concerns_raised, :llm_response,
			:tier1_response, :tier2_response, :bus_message_sent)
	`, p)
	if err != nil {
		return fmt.Errorf("insert poll: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("last insert id: %w", err)
	}
	p.ID = id
	return nil
}

// RecentPolls returns the N most recent polls for a project.
func (s *Store) RecentPolls(projectID int64, limit int) ([]Poll, error) {
	var polls []Poll
	if err := s.db.Select(&polls, `
		SELECT * FROM polls
		WHERE project_id = ?
		ORDER BY polled_at DESC, id DESC
		LIMIT ?
	`, projectID, limit); err != nil {
		return nil, fmt.Errorf("recent polls: %w", err)
	}
	return polls, nil
}

// LastPoll returns the most recent poll for a project, or nil if none.
func (s *Store) LastPoll(projectID int64) (*Poll, error) {
	polls, err := s.RecentPolls(projectID, 1)
	if err != nil {
		return nil, err
	}
	if len(polls) == 0 {
		return nil, nil
	}
	return &polls[0], nil
}
