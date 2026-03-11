package db

import (
	"fmt"

	"github.com/dustinlange/agent-minder/internal/sqliteutil"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

const currentVersion = 2

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
	llm_summarizer_model  TEXT DEFAULT 'claude-haiku-4-5',
	llm_analyzer_model    TEXT DEFAULT 'claude-sonnet-4-6',
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
	tier1_response  TEXT DEFAULT '',
	tier2_response  TEXT DEFAULT '',
	bus_message_sent TEXT DEFAULT '',
	polled_at       TEXT DEFAULT (datetime('now'))
);
`

// Open opens (or creates) the agent-minder SQLite database and runs migrations,
// with automatic WAL recovery if stale -shm/-wal files are detected.
func Open(path string) (*sqlx.DB, error) {
	db, err := sqliteutil.OpenWithRecovery(path, path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
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

	if version < 2 {
		if err := migrateV2(db); err != nil {
			return fmt.Errorf("apply migration v2: %w", err)
		}
	}

	_, err = db.Exec("UPDATE schema_version SET version = ?", currentVersion)
	return err
}

func migrateV2(db *sqlx.DB) error {
	stmts := []string{
		`ALTER TABLE projects ADD COLUMN llm_summarizer_model TEXT DEFAULT 'claude-haiku-4-5'`,
		`ALTER TABLE projects ADD COLUMN llm_analyzer_model TEXT DEFAULT 'claude-sonnet-4-6'`,
		`ALTER TABLE polls ADD COLUMN tier1_response TEXT`,
		`ALTER TABLE polls ADD COLUMN tier2_response TEXT`,
		`ALTER TABLE polls ADD COLUMN bus_message_sent TEXT`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	// Copy existing data into new columns.
	if _, err := db.Exec(`UPDATE projects SET llm_summarizer_model = llm_model WHERE llm_model IS NOT NULL AND llm_model != ''`); err != nil {
		return fmt.Errorf("copy llm_model to llm_summarizer_model: %w", err)
	}
	if _, err := db.Exec(`UPDATE polls SET tier1_response = llm_response WHERE llm_response IS NOT NULL`); err != nil {
		return fmt.Errorf("copy llm_response to tier1_response: %w", err)
	}
	// Ensure no NULLs in new text columns (Go strings can't scan NULL).
	if _, err := db.Exec(`UPDATE polls SET tier1_response = '' WHERE tier1_response IS NULL`); err != nil {
		return fmt.Errorf("null-fill tier1_response: %w", err)
	}
	if _, err := db.Exec(`UPDATE polls SET tier2_response = '' WHERE tier2_response IS NULL`); err != nil {
		return fmt.Errorf("null-fill tier2_response: %w", err)
	}
	if _, err := db.Exec(`UPDATE polls SET bus_message_sent = '' WHERE bus_message_sent IS NULL`); err != nil {
		return fmt.Errorf("null-fill bus_message_sent: %w", err)
	}
	return nil
}

// DefaultDBPath returns the default path for the agent-minder database.
func DefaultDBPath() string {
	return ExpandHome("~/.agent-minder/minder.db")
}
