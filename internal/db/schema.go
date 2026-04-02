// Package db provides SQLite schema and CRUD operations for agent-minder v2.
package db

import (
	"fmt"
	"os"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

const schemaVersion = 3

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

CREATE TABLE IF NOT EXISTS jobs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	deployment_id TEXT NOT NULL REFERENCES deployments(id),

	-- What to run
	agent TEXT NOT NULL DEFAULT 'autopilot',
	name TEXT NOT NULL,

	-- Context (nullable for proactive agents)
	issue_number INTEGER,
	issue_title TEXT,
	issue_body TEXT,
	owner TEXT NOT NULL,
	repo TEXT NOT NULL,

	-- Lifecycle
	status TEXT NOT NULL DEFAULT 'queued',
	current_stage TEXT,
	stages_json TEXT,
	result_json TEXT,

	-- Execution
	worktree_path TEXT,
	branch TEXT,
	pr_number INTEGER,
	cost_usd REAL DEFAULT 0.0,
	agent_log TEXT,

	-- Failure
	failure_reason TEXT,
	failure_detail TEXT,

	-- Review
	review_risk TEXT,
	review_comment_id INTEGER,

	-- Dependencies
	dependencies TEXT,

	-- Budget overrides
	max_turns INTEGER,
	max_budget_usd REAL,

	-- Timestamps
	queued_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	started_at DATETIME,
	completed_at DATETIME,

	UNIQUE(deployment_id, name)
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

CREATE TABLE IF NOT EXISTS job_lessons (
	job_id INTEGER NOT NULL REFERENCES jobs(id),
	lesson_id INTEGER NOT NULL REFERENCES lessons(id),
	PRIMARY KEY (job_id, lesson_id)
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

CREATE TABLE IF NOT EXISTS job_schedules (
	name TEXT PRIMARY KEY,
	deployment_id TEXT NOT NULL,
	cron_expr TEXT,
	trigger_expr TEXT,
	agent TEXT NOT NULL,
	description TEXT,
	budget REAL,
	max_turns INTEGER,
	enabled INTEGER DEFAULT 1,
	last_run_at DATETIME,
	next_run_at DATETIME,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`

// migrateV1toV2 migrates a v1 database (tasks table) to v2 (jobs table).
const migrateV1toV2 = `
-- Rename tasks → jobs and add new columns.
ALTER TABLE tasks RENAME TO jobs;
ALTER TABLE jobs ADD COLUMN agent TEXT NOT NULL DEFAULT 'autopilot';
ALTER TABLE jobs ADD COLUMN name TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN current_stage TEXT;
ALTER TABLE jobs ADD COLUMN stages_json TEXT;
ALTER TABLE jobs ADD COLUMN result_json TEXT;
ALTER TABLE jobs ADD COLUMN queued_at DATETIME;

-- Rename max_turns_override → max_turns, max_budget_override → max_budget_usd (on jobs).
-- SQLite doesn't support RENAME COLUMN on older versions, but modernc/sqlite does.
ALTER TABLE jobs RENAME COLUMN max_turns_override TO max_turns;
ALTER TABLE jobs RENAME COLUMN max_budget_override TO max_budget_usd;

-- Backfill name from issue_number for existing rows.
UPDATE jobs SET name = 'issue-' || issue_number WHERE name = '';

-- Backfill queued_at from started_at or current time.
UPDATE jobs SET queued_at = COALESCE(started_at, CURRENT_TIMESTAMP) WHERE queued_at IS NULL;

-- Rename task_lessons → job_lessons.
ALTER TABLE task_lessons RENAME TO job_lessons;
ALTER TABLE job_lessons RENAME COLUMN task_id TO job_id;

-- Update schema version.
UPDATE schema_version SET version = 2;
`

const migrateV2toV3 = `
CREATE TABLE IF NOT EXISTS job_schedules (
	name TEXT PRIMARY KEY,
	deployment_id TEXT NOT NULL,
	cron_expr TEXT,
	trigger_expr TEXT,
	agent TEXT NOT NULL,
	description TEXT,
	budget REAL,
	max_turns INTEGER,
	enabled INTEGER DEFAULT 1,
	last_run_at DATETIME,
	next_run_at DATETIME,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
UPDATE schema_version SET version = 3;
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

	// Check if this is an existing v1 database that needs migration.
	var version int
	hasVersion := false
	if err := db.Get(&version, "SELECT version FROM schema_version LIMIT 1"); err == nil {
		hasVersion = true
	}

	if hasVersion && version < schemaVersion {
		// Run migrations sequentially.
		if version < 2 {
			if _, err := db.Exec(migrateV1toV2); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("migrating v1→v2: %w", err)
			}
		}
		if version < 3 {
			if _, err := db.Exec(migrateV2toV3); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("migrating v2→v3: %w", err)
			}
		}
	} else if !hasVersion {
		// Fresh database — apply schema from scratch.
		if _, err := db.Exec(schema); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("applying schema: %w", err)
		}
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
