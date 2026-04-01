// Package db provides SQLite schema and CRUD operations for agent-minder v2.
package db

import (
	"fmt"
	"os"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS deployments (
	id TEXT PRIMARY KEY,
	repo_dir TEXT NOT NULL,
	owner TEXT NOT NULL,
	repo TEXT NOT NULL,
	mode TEXT NOT NULL DEFAULT 'issues',
	watch_filter TEXT,
	max_agents INTEGER DEFAULT 3,
	max_turns INTEGER DEFAULT 50,
	max_budget_usd REAL DEFAULT 5.0,
	analyzer_model TEXT DEFAULT 'sonnet',
	skip_label TEXT DEFAULT 'no-agent',
	auto_merge INTEGER DEFAULT 0,
	review_enabled INTEGER DEFAULT 1,
	review_max_turns INTEGER,
	review_max_budget REAL,
	total_budget_usd REAL DEFAULT 25.0,
	carried_cost_usd REAL DEFAULT 0.0,
	base_branch TEXT DEFAULT 'main',
	started_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS tasks (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	deployment_id TEXT NOT NULL REFERENCES deployments(id),
	issue_number INTEGER NOT NULL,
	issue_title TEXT,
	issue_body TEXT,
	owner TEXT NOT NULL,
	repo TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'queued',
	dependencies TEXT,
	worktree_path TEXT,
	branch TEXT,
	pr_number INTEGER,
	cost_usd REAL DEFAULT 0.0,
	failure_reason TEXT,
	failure_detail TEXT,
	review_risk TEXT,
	review_comment_id INTEGER,
	max_turns_override INTEGER,
	max_budget_override REAL,
	agent_log TEXT,
	started_at DATETIME,
	completed_at DATETIME,
	UNIQUE(deployment_id, issue_number)
);

CREATE TABLE IF NOT EXISTS dep_graphs (
	deployment_id TEXT PRIMARY KEY REFERENCES deployments(id),
	graph_json TEXT NOT NULL,
	option_name TEXT,
	reasoning TEXT,
	confidence TEXT,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS lessons (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	repo_scope TEXT,
	content TEXT NOT NULL,
	source TEXT NOT NULL DEFAULT 'manual',
	active INTEGER DEFAULT 1,
	pinned INTEGER DEFAULT 0,
	times_injected INTEGER DEFAULT 0,
	times_helpful INTEGER DEFAULT 0,
	times_unhelpful INTEGER DEFAULT 0,
	superseded_by INTEGER REFERENCES lessons(id),
	last_injected_at DATETIME,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS task_lessons (
	task_id INTEGER NOT NULL REFERENCES tasks(id),
	lesson_id INTEGER NOT NULL REFERENCES lessons(id),
	PRIMARY KEY (task_id, lesson_id)
);

CREATE TABLE IF NOT EXISTS repo_onboarding (
	repo_dir TEXT PRIMARY KEY,
	owner TEXT NOT NULL,
	repo TEXT NOT NULL,
	yaml_content TEXT NOT NULL,
	validation_status TEXT DEFAULT 'untested',
	validation_failures TEXT,
	scanned_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`

// DefaultDBPath returns the default database path for v2.
func DefaultDBPath() string {
	home, err := expandHome("~/.agent-minder")
	if err != nil {
		return "minder-v2.db"
	}
	return home + "/v2.db"
}

// Open opens or creates the SQLite database with WAL mode and foreign keys.
func Open(dsn string) (*sqlx.DB, error) {
	db, err := sqlx.Open("sqlite", dsn+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Apply schema.
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("applying schema: %w", err)
	}

	// Set or verify schema version.
	var count int
	if err := db.Get(&count, "SELECT COUNT(*) FROM schema_version"); err == nil && count == 0 {
		_, _ = db.Exec("INSERT INTO schema_version (version) VALUES (?)", schemaVersion)
	}

	return db, nil
}

// expandHome expands ~ to the user's home directory.
func expandHome(path string) (string, error) {
	if len(path) < 2 || path[:2] != "~/" {
		return path, nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	return home + path[1:], nil
}

func userHomeDir() (string, error) {
	return os.UserHomeDir()
}
