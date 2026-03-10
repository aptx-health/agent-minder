package db

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

const currentVersion = 1

const schemaV1 = `
CREATE TABLE IF NOT EXISTS schema_version (
	version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS projects (
	id                    INTEGER PRIMARY KEY,
	name                  TEXT UNIQUE NOT NULL,
	goal_type             TEXT,
	goal_description      TEXT,
	refresh_interval_sec  INTEGER DEFAULT 300,
	message_ttl_sec       INTEGER DEFAULT 172800,
	auto_enroll_worktrees BOOLEAN DEFAULT 1,
	minder_identity       TEXT,
	llm_provider          TEXT DEFAULT 'anthropic',
	llm_model             TEXT DEFAULT 'claude-haiku-4-5',
	created_at            TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS repos (
	id         INTEGER PRIMARY KEY,
	project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	path       TEXT NOT NULL,
	short_name TEXT NOT NULL,
	summary    TEXT,
	UNIQUE(project_id, path)
);

CREATE TABLE IF NOT EXISTS worktrees (
	id      INTEGER PRIMARY KEY,
	repo_id INTEGER REFERENCES repos(id) ON DELETE CASCADE,
	path    TEXT NOT NULL,
	branch  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS topics (
	id         INTEGER PRIMARY KEY,
	project_id INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	UNIQUE(project_id, name)
);

CREATE TABLE IF NOT EXISTS concerns (
	id          INTEGER PRIMARY KEY,
	project_id  INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	severity    TEXT DEFAULT 'warning',
	message     TEXT NOT NULL,
	resolved    BOOLEAN DEFAULT 0,
	created_at  TEXT DEFAULT (datetime('now')),
	resolved_at TEXT
);

CREATE TABLE IF NOT EXISTS polls (
	id              INTEGER PRIMARY KEY,
	project_id      INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	new_commits     INTEGER DEFAULT 0,
	new_messages    INTEGER DEFAULT 0,
	concerns_raised INTEGER DEFAULT 0,
	llm_response    TEXT,
	polled_at       TEXT DEFAULT (datetime('now'))
);
`

// Open opens (or creates) the agent-minder SQLite database and runs migrations.
func Open(path string) (*sqlx.DB, error) {
	db, err := sqlx.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

func migrate(db *sqlx.DB) error {
	// Check current version.
	var version int
	err := db.Get(&version, "SELECT version FROM schema_version LIMIT 1")
	if err != nil {
		// Table doesn't exist or is empty — apply from scratch.
		if _, err := db.Exec(schemaV1); err != nil {
			return fmt.Errorf("apply schema v1: %w", err)
		}
		if _, err := db.Exec("INSERT INTO schema_version (version) VALUES (?)", currentVersion); err != nil {
			return fmt.Errorf("set schema version: %w", err)
		}
		return nil
	}

	if version >= currentVersion {
		return nil
	}

	// Future migrations go here: if version < 2 { ... }

	_, err = db.Exec("UPDATE schema_version SET version = ?", currentVersion)
	return err
}

// DefaultDBPath returns the default path for the agent-minder database.
func DefaultDBPath() string {
	return ExpandHome("~/.agent-minder/minder.db")
}
