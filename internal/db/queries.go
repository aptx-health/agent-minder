package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

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
			llm_summarizer_model, llm_analyzer_model, status_interval_sec, analysis_interval_sec,
			idle_pause_sec, analyzer_focus,
			autopilot_filter_type, autopilot_filter_value, autopilot_max_agents,
			autopilot_max_turns, autopilot_max_budget_usd, autopilot_skip_label,
			autopilot_base_branch, is_deploy)
		VALUES (:name, :goal_type, :goal_description, :refresh_interval_sec,
			:message_ttl_sec, :auto_enroll_worktrees, :minder_identity, :llm_provider, :llm_model,
			:llm_summarizer_model, :llm_analyzer_model, :status_interval_sec, :analysis_interval_sec,
			:idle_pause_sec, :analyzer_focus,
			:autopilot_filter_type, :autopilot_filter_value, :autopilot_max_agents,
			:autopilot_max_turns, :autopilot_max_budget_usd, :autopilot_skip_label,
			:autopilot_base_branch, :is_deploy)
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
			llm_analyzer_model = :llm_analyzer_model,
			llm_summarizer_provider = :llm_summarizer_provider,
			llm_analyzer_provider = :llm_analyzer_provider,
			status_interval_sec = :status_interval_sec,
			analysis_interval_sec = :analysis_interval_sec,
			idle_pause_sec = :idle_pause_sec,
			analyzer_focus = :analyzer_focus,
			autopilot_filter_type = :autopilot_filter_type,
			autopilot_filter_value = :autopilot_filter_value,
			autopilot_max_agents = :autopilot_max_agents,
			autopilot_max_turns = :autopilot_max_turns,
			autopilot_max_budget_usd = :autopilot_max_budget_usd,
			autopilot_skip_label = :autopilot_skip_label,
			autopilot_base_branch = :autopilot_base_branch
		WHERE id = :id
	`, p)
	return err
}

// ListDeployProjects returns all deploy projects ordered by creation time (newest first).
func (s *Store) ListDeployProjects() ([]Project, error) {
	var projects []Project
	if err := s.db.Select(&projects, "SELECT * FROM projects WHERE is_deploy = 1 ORDER BY created_at DESC"); err != nil {
		return nil, fmt.Errorf("list deploy projects: %w", err)
	}
	return projects, nil
}

// IssuesInRunningDeploys returns a map of issue# → deploy project name for any issues
// currently queued or running in a deploy project for the given owner/repo.
func (s *Store) IssuesInRunningDeploys(owner, repo string, issues []int) (map[int]string, error) {
	if len(issues) == 0 {
		return nil, nil
	}
	result := make(map[int]string)
	for _, issueNum := range issues {
		var matches []struct {
			ProjectName string `db:"name"`
			IssueNumber int    `db:"issue_number"`
		}
		if err := s.db.Select(&matches, `
			SELECT p.name, t.issue_number
			FROM autopilot_tasks t
			JOIN projects p ON p.id = t.project_id
			WHERE p.is_deploy = 1
			  AND t.owner = ? AND t.repo = ?
			  AND t.issue_number = ?
			  AND t.status IN ('queued', 'running')
		`, owner, repo, issueNum); err != nil {
			return nil, fmt.Errorf("check deploy issue %d: %w", issueNum, err)
		}
		for _, m := range matches {
			result[m.IssueNumber] = m.ProjectName
		}
	}
	return result, nil
}

// UpdateAnalyzerSessionID sets the persistent analyzer session ID for a project.
func (s *Store) UpdateAnalyzerSessionID(projectID int64, sessionID string) error {
	_, err := s.db.Exec(`UPDATE projects SET analyzer_session_id = ? WHERE id = ?`, sessionID, projectID)
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
	defer func() { _ = tx.Rollback() }()

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

// GetWorktreesForProject returns all worktrees across all repos for a project,
// joined with the repo short name.
func (s *Store) GetWorktreesForProject(projectID int64) ([]WorktreeWithRepo, error) {
	var wts []WorktreeWithRepo
	if err := s.db.Select(&wts, `
		SELECT w.id, w.repo_id, w.path, w.branch, r.short_name AS repo_short_name
		FROM worktrees w
		JOIN repos r ON r.id = w.repo_id
		WHERE r.project_id = ?
		ORDER BY r.short_name, w.id DESC
	`, projectID); err != nil {
		return nil, fmt.Errorf("get worktrees for project: %w", err)
	}
	return wts, nil
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

// --- Tracked Items ---

// AddTrackedItem inserts a tracked item. Returns error if the cap (10) is reached.
// Uses atomic count-and-insert to prevent race conditions.
func (s *Store) AddTrackedItem(item *TrackedItem) error {
	result, err := s.db.Exec(`
		INSERT INTO tracked_items (project_id, source, owner, repo, number, item_type, title, state, labels, last_status, last_checked_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE (SELECT COUNT(*) FROM tracked_items WHERE project_id = ?) < 20
	`, item.ProjectID, item.Source, item.Owner, item.Repo, item.Number,
		item.ItemType, item.Title, item.State, item.Labels, item.LastStatus, item.LastCheckedAt,
		item.ProjectID)
	if err != nil {
		return fmt.Errorf("insert tracked item: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("tracked item limit reached (max 20 per project)")
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("last insert id: %w", err)
	}
	item.ID = id
	return nil
}

// GetTrackedItems returns all tracked items for a project.
func (s *Store) GetTrackedItems(projectID int64) ([]TrackedItem, error) {
	var items []TrackedItem
	if err := s.db.Select(&items, `
		SELECT * FROM tracked_items WHERE project_id = ? ORDER BY owner, repo, number
	`, projectID); err != nil {
		return nil, fmt.Errorf("get tracked items: %w", err)
	}
	return items, nil
}

// RemoveTrackedItem deletes a tracked item by project, owner, repo, and number.
func (s *Store) RemoveTrackedItem(projectID int64, owner, repo string, number int) error {
	result, err := s.db.Exec(`
		DELETE FROM tracked_items WHERE project_id = ? AND owner = ? AND repo = ? AND number = ?
	`, projectID, owner, repo, number)
	if err != nil {
		return fmt.Errorf("delete tracked item: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("tracked item %s/%s#%d not found", owner, repo, number)
	}
	return nil
}

// ClearTrackedItems removes all tracked items for a project.
func (s *Store) ClearTrackedItems(projectID int64) error {
	_, err := s.db.Exec(`DELETE FROM tracked_items WHERE project_id = ?`, projectID)
	if err != nil {
		return fmt.Errorf("clear tracked items: %w", err)
	}
	return nil
}

// BulkAddTrackedItems inserts multiple tracked items in a transaction.
// Uses INSERT OR IGNORE to skip duplicates (same project+owner+repo+number).
// Enforces the cap of 20 items per project. Returns the count of newly inserted rows.
func (s *Store) BulkAddTrackedItems(items []*TrackedItem) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}

	tx, err := s.db.Beginx()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	projectID := items[0].ProjectID

	var current int
	if err := tx.Get(&current, `SELECT COUNT(*) FROM tracked_items WHERE project_id = ?`, projectID); err != nil {
		return 0, fmt.Errorf("count tracked items: %w", err)
	}

	inserted := 0
	for _, item := range items {
		if current+inserted >= 20 {
			break
		}
		result, err := tx.Exec(`
			INSERT OR IGNORE INTO tracked_items (project_id, source, owner, repo, number, item_type, title, state, labels, last_status, last_checked_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, item.ProjectID, item.Source, item.Owner, item.Repo, item.Number,
			item.ItemType, item.Title, item.State, item.Labels, item.LastStatus, item.LastCheckedAt)
		if err != nil {
			return inserted, fmt.Errorf("insert tracked item: %w", err)
		}
		n, _ := result.RowsAffected()
		if n > 0 {
			inserted += int(n)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return inserted, nil
}

// UpdateTrackedItem updates a tracked item's mutable fields after a status check.
func (s *Store) UpdateTrackedItem(item *TrackedItem) error {
	_, err := s.db.NamedExec(`
		UPDATE tracked_items SET
			title = :title,
			state = :state,
			labels = :labels,
			is_draft = :is_draft,
			review_state = :review_state,
			last_status = :last_status,
			last_checked_at = :last_checked_at,
			content_hash = :content_hash,
			objective_summary = :objective_summary,
			progress_summary = :progress_summary
		WHERE id = :id
	`, item)
	return err
}

// PruneTrackedItems removes the oldest terminal (closed/merged/not-planned) tracked items
// when the total count for a project exceeds maxTotal. It keeps at least keepTerminal
// terminal items for historical context. Returns the number of items pruned.
func (s *Store) PruneTrackedItems(projectID int64, maxTotal, keepTerminal int) (int, error) {
	var total int
	if err := s.db.Get(&total, `SELECT COUNT(*) FROM tracked_items WHERE project_id = ?`, projectID); err != nil {
		return 0, fmt.Errorf("count tracked items: %w", err)
	}
	if total < maxTotal {
		return 0, nil
	}

	// Find terminal items ordered oldest first.
	var terminal []TrackedItem
	if err := s.db.Select(&terminal, `
		SELECT * FROM tracked_items
		WHERE project_id = ? AND last_status IN ('Closd', 'Mrgd', 'NotPl')
		ORDER BY last_checked_at ASC, created_at ASC
	`, projectID); err != nil {
		return 0, fmt.Errorf("select terminal items: %w", err)
	}

	// How many can we remove? We need to get below maxTotal but keep at least keepTerminal.
	removable := len(terminal) - keepTerminal
	if removable <= 0 {
		return 0, nil
	}
	excess := total - maxTotal + 1 // prune enough to get back under the cap
	if excess > removable {
		excess = removable
	}

	pruned := 0
	for i := 0; i < excess; i++ {
		if err := s.ArchiveTrackedItem(&terminal[i]); err != nil {
			return pruned, fmt.Errorf("archive tracked item %d: %w", terminal[i].ID, err)
		}
		if _, err := s.db.Exec(`DELETE FROM tracked_items WHERE id = ?`, terminal[i].ID); err != nil {
			return pruned, fmt.Errorf("delete tracked item %d: %w", terminal[i].ID, err)
		}
		pruned++
	}
	return pruned, nil
}

// RemoveTerminalTrackedItems deletes all tracked items with terminal status
// (Closd, Mrgd, NotPl) for a project. Returns the number of items removed.
func (s *Store) RemoveTerminalTrackedItems(projectID int64) (int, error) {
	result, err := s.db.Exec(`
		DELETE FROM tracked_items
		WHERE project_id = ? AND last_status IN ('Closd', 'Mrgd', 'NotPl')
	`, projectID)
	if err != nil {
		return 0, fmt.Errorf("remove terminal tracked items: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// CountTerminalTrackedItems returns the count of tracked items with terminal status.
func (s *Store) CountTerminalTrackedItems(projectID int64) (int, error) {
	var count int
	err := s.db.Get(&count, `
		SELECT COUNT(*) FROM tracked_items
		WHERE project_id = ? AND last_status IN ('Closd', 'Mrgd', 'NotPl')
	`, projectID)
	return count, err
}

// --- Completed Items ---

// ArchiveTrackedItem copies a terminal tracked item to the completed_items table.
// Only archives items that have a non-empty progress summary (i.e., real work was done).
// Builds a combined summary from the objective and progress summaries.
func (s *Store) ArchiveTrackedItem(item *TrackedItem) error {
	if item.ProgressSummary == "" {
		return nil // skip items with no progress signal
	}

	summary := item.ProgressSummary
	if item.ObjectiveSummary != "" {
		summary = item.ObjectiveSummary + " — " + item.ProgressSummary
	}

	_, err := s.db.Exec(`
		INSERT INTO completed_items (project_id, source, owner, repo, number, item_type, title, final_status, summary)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, item.ProjectID, item.Source, item.Owner, item.Repo, item.Number,
		item.ItemType, item.Title, item.LastStatus, summary)
	if err != nil {
		return fmt.Errorf("archive tracked item %s/%s#%d: %w", item.Owner, item.Repo, item.Number, err)
	}
	return nil
}

// ArchiveTerminalTrackedItems archives all terminal tracked items (that have progress summaries)
// and then deletes them. Returns the number removed.
func (s *Store) ArchiveTerminalTrackedItems(projectID int64) (int, error) {
	// Fetch terminal items first.
	var terminal []TrackedItem
	if err := s.db.Select(&terminal, `
		SELECT * FROM tracked_items
		WHERE project_id = ? AND last_status IN ('Closd', 'Mrgd', 'NotPl')
	`, projectID); err != nil {
		return 0, fmt.Errorf("select terminal items: %w", err)
	}

	for i := range terminal {
		if err := s.ArchiveTrackedItem(&terminal[i]); err != nil {
			return 0, err
		}
	}

	result, err := s.db.Exec(`
		DELETE FROM tracked_items
		WHERE project_id = ? AND last_status IN ('Closd', 'Mrgd', 'NotPl')
	`, projectID)
	if err != nil {
		return 0, fmt.Errorf("remove terminal tracked items: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// RecentCompletedItems returns completed items for a project within maxAge.
func (s *Store) RecentCompletedItems(projectID int64, maxAge int) ([]CompletedItem, error) {
	var items []CompletedItem
	if err := s.db.Select(&items, `
		SELECT * FROM completed_items
		WHERE project_id = ? AND completed_at > datetime('now', ? || ' seconds')
		ORDER BY completed_at DESC
	`, projectID, fmt.Sprintf("-%d", maxAge)); err != nil {
		return nil, fmt.Errorf("recent completed items: %w", err)
	}
	return items, nil
}

// PruneCompletedItems removes completed items older than maxAgeSec.
// Returns the number of items pruned.
func (s *Store) PruneCompletedItems(projectID int64, maxAgeSec int) (int, error) {
	result, err := s.db.Exec(`
		DELETE FROM completed_items
		WHERE project_id = ? AND completed_at <= datetime('now', ? || ' seconds')
	`, projectID, fmt.Sprintf("-%d", maxAgeSec))
	if err != nil {
		return 0, fmt.Errorf("prune completed items: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// --- Dep Graphs ---

// SaveDepGraph upserts a dependency graph for a project.
func (s *Store) SaveDepGraph(projectID int64, graphJSON, optionName string) error {
	_, err := s.db.Exec(`
		INSERT INTO autopilot_dep_graphs (project_id, graph_json, option_name)
		VALUES (?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			graph_json = excluded.graph_json,
			option_name = excluded.option_name,
			created_at = datetime('now')
	`, projectID, graphJSON, optionName)
	if err != nil {
		return fmt.Errorf("save dep graph: %w", err)
	}
	return nil
}

// GetDepGraph returns the stored dependency graph for a project, or (nil, nil) if none.
func (s *Store) GetDepGraph(projectID int64) (*DepGraph, error) {
	var dg DepGraph
	if err := s.db.Get(&dg, "SELECT * FROM autopilot_dep_graphs WHERE project_id = ?", projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get dep graph: %w", err)
	}
	return &dg, nil
}

// DeleteDepGraph removes the stored dependency graph for a project.
func (s *Store) DeleteDepGraph(projectID int64) error {
	_, err := s.db.Exec("DELETE FROM autopilot_dep_graphs WHERE project_id = ?", projectID)
	return err
}

// ClearNonTerminalAutopilotTasks deletes only queued and blocked autopilot tasks.
func (s *Store) ClearNonTerminalAutopilotTasks(projectID int64) error {
	_, err := s.db.Exec(`DELETE FROM autopilot_tasks WHERE project_id = ? AND status IN ('queued', 'blocked')`, projectID)
	if err != nil {
		return fmt.Errorf("clear non-terminal tasks: %w", err)
	}
	return nil
}

// TransitionAutopilotTasksForReprepare transitions tasks for a full rebuild re-prepare:
// review/failed/bailed/stopped/running → manual; done/manual/skipped unchanged; queued/blocked cleared.
func (s *Store) TransitionAutopilotTasksForReprepare(projectID int64) error {
	// Transition non-terminal active statuses to manual.
	_, err := s.db.Exec(`
		UPDATE autopilot_tasks SET status = 'manual'
		WHERE project_id = ? AND status IN ('review', 'failed', 'bailed', 'stopped', 'running')
	`, projectID)
	if err != nil {
		return fmt.Errorf("transition tasks to manual: %w", err)
	}
	// Clear queued and blocked tasks (safe to regenerate).
	return s.ClearNonTerminalAutopilotTasks(projectID)
}

// TransitionStaleRunningTasks resets only running tasks to queued (process is gone after restart).
// All other statuses are preserved. Used by the "keep" reprepare flow.
func (s *Store) TransitionStaleRunningTasks(projectID int64) (int, error) {
	result, err := s.db.Exec(`
		UPDATE autopilot_tasks
		SET status = 'queued', worktree_path = '', branch = '', agent_log = '',
		    started_at = '', completed_at = '', failure_reason = '', failure_detail = ''
		WHERE project_id = ? AND status = 'running'
	`, projectID)
	if err != nil {
		return 0, fmt.Errorf("transition stale running tasks: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// --- Autopilot Tasks ---

// CreateAutopilotTask inserts a new autopilot task.
func (s *Store) CreateAutopilotTask(task *AutopilotTask) error {
	result, err := s.db.NamedExec(`
		INSERT INTO autopilot_tasks (project_id, owner, repo, issue_number, issue_title, issue_body, dependencies, status)
		VALUES (:project_id, :owner, :repo, :issue_number, :issue_title, :issue_body, :dependencies, :status)
	`, task)
	if err != nil {
		return fmt.Errorf("insert autopilot task: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("last insert id: %w", err)
	}
	task.ID = id
	return nil
}

// BulkCreateAutopilotTasks inserts multiple autopilot tasks in a transaction.
// Uses INSERT OR IGNORE to skip duplicates.
func (s *Store) BulkCreateAutopilotTasks(tasks []*AutopilotTask) (int, error) {
	if len(tasks) == 0 {
		return 0, nil
	}
	tx, err := s.db.Beginx()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	inserted := 0
	for _, task := range tasks {
		result, err := tx.Exec(`
			INSERT OR IGNORE INTO autopilot_tasks (project_id, owner, repo, issue_number, issue_title, issue_body, dependencies, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, task.ProjectID, task.Owner, task.Repo, task.IssueNumber, task.IssueTitle, task.IssueBody, task.Dependencies, task.Status)
		if err != nil {
			return inserted, fmt.Errorf("insert autopilot task: %w", err)
		}
		n, _ := result.RowsAffected()
		if n > 0 {
			id, _ := result.LastInsertId()
			task.ID = id
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return inserted, nil
}

// GetAutopilotTasks returns all autopilot tasks for a project.
func (s *Store) GetAutopilotTasks(projectID int64) ([]AutopilotTask, error) {
	var tasks []AutopilotTask
	if err := s.db.Select(&tasks, `
		SELECT * FROM autopilot_tasks WHERE project_id = ? ORDER BY issue_number
	`, projectID); err != nil {
		return nil, fmt.Errorf("get autopilot tasks: %w", err)
	}
	return tasks, nil
}

// UpdateAutopilotTaskStatus updates only the status and completed_at of an autopilot task.
func (s *Store) UpdateAutopilotTaskStatus(id int64, status string) error {
	if status == "done" || status == "bailed" || status == "stopped" || status == "failed" {
		_, err := s.db.Exec(`
			UPDATE autopilot_tasks SET status = ?, completed_at = datetime('now') WHERE id = ?
		`, status, id)
		return err
	}
	_, err := s.db.Exec(`UPDATE autopilot_tasks SET status = ? WHERE id = ?`, status, id)
	return err
}

// UpdateAutopilotTaskRunning sets a task to running with its worktree info.
func (s *Store) UpdateAutopilotTaskRunning(id int64, worktreePath, branch, agentLog string) error {
	_, err := s.db.Exec(`
		UPDATE autopilot_tasks SET status = 'running', worktree_path = ?, branch = ?, agent_log = ?, started_at = datetime('now')
		WHERE id = ?
	`, worktreePath, branch, agentLog, id)
	return err
}

// UpdateAutopilotTaskPR sets the PR number for a completed task.
func (s *Store) UpdateAutopilotTaskPR(id int64, prNumber int) error {
	_, err := s.db.Exec(`UPDATE autopilot_tasks SET pr_number = ? WHERE id = ?`, prNumber, id)
	return err
}

// UpdateAutopilotTaskFailure sets a task to failed with reason and detail.
func (s *Store) UpdateAutopilotTaskFailure(id int64, reason, detail string) error {
	_, err := s.db.Exec(`
		UPDATE autopilot_tasks SET status = 'failed', failure_reason = ?, failure_detail = ?, completed_at = datetime('now')
		WHERE id = ?
	`, reason, detail, id)
	return err
}

// UpdateAutopilotTaskDeps updates the dependencies JSON for a task.
func (s *Store) UpdateAutopilotTaskDeps(id int64, deps string) error {
	_, err := s.db.Exec(`UPDATE autopilot_tasks SET dependencies = ? WHERE id = ?`, deps, id)
	return err
}

// QueuedUnblockedTasks returns queued tasks where all dependencies are done.
func (s *Store) QueuedUnblockedTasks(projectID int64) ([]AutopilotTask, error) {
	// Get all tasks for the project.
	allTasks, err := s.GetAutopilotTasks(projectID)
	if err != nil {
		return nil, err
	}

	// Build a map of issue number → status.
	statusMap := make(map[int]string, len(allTasks))
	for _, t := range allTasks {
		statusMap[t.IssueNumber] = t.Status
	}

	// Build a map of tracked item number → state for external dep checks.
	// If a dep isn't an autopilot task but IS a tracked open issue, it blocks.
	trackedItems, _ := s.GetTrackedItems(projectID)
	trackedState := make(map[int]string, len(trackedItems))
	for _, item := range trackedItems {
		trackedState[item.Number] = item.State
	}

	// Filter queued tasks where all deps are done.
	var unblocked []AutopilotTask
	for _, t := range allTasks {
		if t.Status != "queued" {
			continue
		}
		deps := parseDependencies(t.Dependencies)
		allDone := true
		for _, dep := range deps {
			if taskStatus, ok := statusMap[dep]; ok {
				// Dep is an autopilot task — must be done.
				if taskStatus != "done" {
					allDone = false
					break
				}
			} else if state, ok := trackedState[dep]; ok {
				// Dep is a tracked item but not an autopilot task (e.g., skipped via no-agent).
				// Block unless the issue is closed/merged.
				if state == "open" {
					allDone = false
					break
				}
			}
			// If dep isn't tracked at all, treat as non-blocking.
		}
		if allDone {
			unblocked = append(unblocked, t)
		}
	}
	return unblocked, nil
}

// RunningAutopilotTasks returns all running autopilot tasks for a project.
func (s *Store) RunningAutopilotTasks(projectID int64) ([]AutopilotTask, error) {
	var tasks []AutopilotTask
	if err := s.db.Select(&tasks, `
		SELECT * FROM autopilot_tasks WHERE project_id = ? AND status = 'running' ORDER BY issue_number
	`, projectID); err != nil {
		return nil, fmt.Errorf("running autopilot tasks: %w", err)
	}
	return tasks, nil
}

// DeleteAutopilotTask removes a single autopilot task by ID.
func (s *Store) DeleteAutopilotTask(id int64) error {
	_, err := s.db.Exec(`DELETE FROM autopilot_tasks WHERE id = ?`, id)
	return err
}

// ClearAutopilotTasks removes all autopilot tasks for a project.
func (s *Store) ClearAutopilotTasks(projectID int64) error {
	_, err := s.db.Exec(`DELETE FROM autopilot_tasks WHERE project_id = ?`, projectID)
	return err
}

// ResetStaleAutopilotTasks resets interrupted tasks back to "queued".
// "running" tasks are always reset (the process is gone).
// "bailed" tasks are only reset if completed_at is NULL (interrupted before finishing).
// Bailed tasks WITH completed_at were legitimate failures — the agent ran and gave up.
// Returns the number of tasks reset.
func (s *Store) ResetStaleAutopilotTasks(projectID int64) (int, error) {
	result, err := s.db.Exec(`
		UPDATE autopilot_tasks
		SET status = 'queued', worktree_path = '', branch = '', agent_log = '',
		    started_at = '', completed_at = '', failure_reason = '', failure_detail = ''
		WHERE project_id = ? AND (
			status = 'running'
			OR (status = 'bailed' AND completed_at IS NULL)
		)
	`, projectID)
	if err != nil {
		return 0, fmt.Errorf("reset stale tasks: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// ResumeAutopilotTask sets a task back to running, preserving worktree/branch/log
// but clearing failure fields and updating started_at. Used when resuming work in
// an existing worktree rather than starting fresh.
func (s *Store) ResumeAutopilotTask(id int64) error {
	_, err := s.db.Exec(`
		UPDATE autopilot_tasks
		SET status = 'running', started_at = datetime('now'), completed_at = '',
		    failure_reason = '', failure_detail = ''
		WHERE id = ?
	`, id)
	return err
}

// ClearAutopilotTaskWorktree clears the worktree_path for a task after cleanup.
func (s *Store) ClearAutopilotTaskWorktree(id int64) error {
	_, err := s.db.Exec(`UPDATE autopilot_tasks SET worktree_path = '' WHERE id = ?`, id)
	return err
}

// ResetAutopilotTask resets a single task back to queued, clearing runtime fields.
func (s *Store) ResetAutopilotTask(id int64) error {
	_, err := s.db.Exec(`
		UPDATE autopilot_tasks
		SET status = 'queued', worktree_path = '', branch = '', agent_log = '',
		    started_at = '', completed_at = '', pr_number = 0,
		    failure_reason = '', failure_detail = ''
		WHERE id = ?
	`, id)
	return err
}

// parseDependencies parses a JSON array of issue numbers.
func parseDependencies(deps string) []int {
	if deps == "" || deps == "[]" {
		return nil
	}
	// Simple parser for JSON int arrays like [42, 38].
	deps = strings.TrimSpace(deps)
	if len(deps) < 2 || deps[0] != '[' || deps[len(deps)-1] != ']' {
		return nil
	}
	inner := deps[1 : len(deps)-1]
	if strings.TrimSpace(inner) == "" {
		return nil
	}
	parts := strings.Split(inner, ",")
	var result []int
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err == nil {
			result = append(result, n)
		}
	}
	return result
}

// --- Repo Onboarding ---

// UpsertRepoOnboarding inserts or updates an onboarding record for a repo.
func (s *Store) UpsertRepoOnboarding(e *RepoOnboarding) error {
	result, err := s.db.Exec(`
		INSERT INTO repo_onboarding (repo_id, onboarding_yaml, validated_at, validation_status)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(repo_id) DO UPDATE SET
			onboarding_yaml = excluded.onboarding_yaml,
			onboarded_at = datetime('now'),
			validated_at = excluded.validated_at,
			validation_status = excluded.validation_status
	`, e.RepoID, e.OnboardingYAML, e.ValidatedAt, e.ValidationStatus)
	if err != nil {
		return fmt.Errorf("upsert repo onboarding: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("last insert id: %w", err)
	}
	e.ID = id
	return nil
}

// GetRepoOnboarding returns the onboarding record for a repo, or (nil, nil) if none exists.
func (s *Store) GetRepoOnboarding(repoID int64) (*RepoOnboarding, error) {
	var e RepoOnboarding
	if err := s.db.Get(&e, "SELECT * FROM repo_onboarding WHERE repo_id = ?", repoID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get repo onboarding: %w", err)
	}
	return &e, nil
}

// GetRepoOnboardings returns all onboarding records for repos in a project.
func (s *Store) GetRepoOnboardings(projectID int64) ([]RepoOnboarding, error) {
	var records []RepoOnboarding
	if err := s.db.Select(&records, `
		SELECT ro.* FROM repo_onboarding ro
		JOIN repos r ON r.id = ro.repo_id
		WHERE r.project_id = ?
		ORDER BY ro.onboarded_at DESC
	`, projectID); err != nil {
		return nil, fmt.Errorf("get repo onboarding records: %w", err)
	}
	return records, nil
}

// UpdateRepoOnboardingValidation updates only the validation fields.
func (s *Store) UpdateRepoOnboardingValidation(repoID int64, status string) error {
	_, err := s.db.Exec(`
		UPDATE repo_onboarding SET validation_status = ?, validated_at = datetime('now')
		WHERE repo_id = ?
	`, status, repoID)
	if err != nil {
		return fmt.Errorf("update onboarding validation: %w", err)
	}
	return nil
}

// DeleteRepoOnboarding removes the onboarding record for a repo.
func (s *Store) DeleteRepoOnboarding(repoID int64) error {
	_, err := s.db.Exec("DELETE FROM repo_onboarding WHERE repo_id = ?", repoID)
	return err
}

// UpdateAutopilotTaskOverrides sets the per-task max_turns and max_budget overrides.
// Pass nil to clear an override (revert to project default).
func (s *Store) UpdateAutopilotTaskOverrides(id int64, maxTurns *int, maxBudget *float64) error {
	_, err := s.db.Exec(`
		UPDATE autopilot_tasks SET max_turns_override = ?, max_budget_override = ?
		WHERE id = ?
	`, maxTurns, maxBudget, id)
	return err
}

// UpdateAutopilotTaskCost sets the cost_usd for an autopilot task.
func (s *Store) UpdateAutopilotTaskCost(id int64, costUSD float64) error {
	_, err := s.db.Exec(`UPDATE autopilot_tasks SET cost_usd = ? WHERE id = ?`, costUSD, id)
	return err
}

// --- Cost Aggregation ---

// DailyCost returns the aggregated cost for a project on a given date (YYYY-MM-DD).
// Only includes tasks with terminal status (done, bailed, failed, stopped).
func (s *Store) DailyCost(projectID int64, date string) (*CostSummary, error) {
	var cs CostSummary
	err := s.db.Get(&cs, `
		SELECT COALESCE(SUM(cost_usd), 0) AS total_cost,
		       COUNT(*) AS task_count
		FROM autopilot_tasks
		WHERE project_id = ?
		  AND status IN ('done', 'bailed', 'failed', 'stopped')
		  AND date(completed_at) = date(?)
	`, projectID, date)
	if err != nil {
		return nil, fmt.Errorf("daily cost: %w", err)
	}
	return &cs, nil
}

// WeeklyCost returns the aggregated cost for a project in the 7-day window
// ending on the given date (inclusive). Only includes terminal tasks.
func (s *Store) WeeklyCost(projectID int64, endDate string) (*CostSummary, error) {
	var cs CostSummary
	err := s.db.Get(&cs, `
		SELECT COALESCE(SUM(cost_usd), 0) AS total_cost,
		       COUNT(*) AS task_count
		FROM autopilot_tasks
		WHERE project_id = ?
		  AND status IN ('done', 'bailed', 'failed', 'stopped')
		  AND date(completed_at) >= date(?, '-6 days')
		  AND date(completed_at) <= date(?)
	`, projectID, endDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("weekly cost: %w", err)
	}
	return &cs, nil
}

// OverallCost returns the aggregated cost for all terminal tasks in a project.
func (s *Store) OverallCost(projectID int64) (*CostSummary, error) {
	var cs CostSummary
	err := s.db.Get(&cs, `
		SELECT COALESCE(SUM(cost_usd), 0) AS total_cost,
		       COUNT(*) AS task_count
		FROM autopilot_tasks
		WHERE project_id = ?
		  AND status IN ('done', 'bailed', 'failed', 'stopped')
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("overall cost: %w", err)
	}
	return &cs, nil
}

// DailyTaskCosts returns per-task cost details for a project on a given date.
func (s *Store) DailyTaskCosts(projectID int64, date string) ([]TaskCostDetail, error) {
	var details []TaskCostDetail
	err := s.db.Select(&details, `
		SELECT issue_number, issue_title, status, cost_usd, completed_at
		FROM autopilot_tasks
		WHERE project_id = ?
		  AND status IN ('done', 'bailed', 'failed', 'stopped')
		  AND date(completed_at) = date(?)
		ORDER BY completed_at DESC
	`, projectID, date)
	if err != nil {
		return nil, fmt.Errorf("daily task costs: %w", err)
	}
	return details, nil
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
