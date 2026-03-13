package db

import (
	"fmt"

	"github.com/dustinlange/agent-minder/internal/sqliteutil"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

const currentVersion = 7

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
	idle_pause_sec        INTEGER DEFAULT 14400,
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

CREATE TABLE IF NOT EXISTS tracked_items (
	id              INTEGER PRIMARY KEY,
	project_id      INTEGER REFERENCES projects(id) ON DELETE CASCADE,
	source          TEXT NOT NULL DEFAULT 'github',
	owner           TEXT NOT NULL,
	repo            TEXT NOT NULL,
	number          INTEGER NOT NULL,
	item_type       TEXT NOT NULL DEFAULT 'issue',
	title           TEXT NOT NULL DEFAULT '',
	state           TEXT NOT NULL DEFAULT 'open',
	labels          TEXT NOT NULL DEFAULT '',
	is_draft        BOOLEAN NOT NULL DEFAULT 0,
	review_state    TEXT NOT NULL DEFAULT '',
	last_status     TEXT NOT NULL DEFAULT 'Open',
	last_checked_at     TEXT DEFAULT '',
	content_hash        TEXT DEFAULT '',
	objective_summary   TEXT DEFAULT '',
	progress_summary    TEXT DEFAULT '',
	created_at          TEXT DEFAULT (datetime('now')),
	UNIQUE(project_id, source, owner, repo, number)
);

CREATE TABLE IF NOT EXISTS completed_items (
	id           INTEGER PRIMARY KEY,
	project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	source       TEXT NOT NULL DEFAULT 'github',
	owner        TEXT NOT NULL,
	repo         TEXT NOT NULL,
	number       INTEGER NOT NULL,
	item_type    TEXT NOT NULL DEFAULT 'issue',
	title        TEXT NOT NULL DEFAULT '',
	final_status TEXT NOT NULL DEFAULT 'Closd',
	summary      TEXT NOT NULL DEFAULT '',
	completed_at TEXT DEFAULT (datetime('now'))
);
`

// Open opens (or creates) the agent-minder SQLite database and runs migrations,
// with automatic WAL recovery if stale -shm/-wal files are detected.
func Open(path string) (*sqlx.DB, error) {
	db, err := sqliteutil.OpenWithRecovery(path, path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
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

	if version < 3 {
		if err := migrateV3(db); err != nil {
			return fmt.Errorf("apply migration v3: %w", err)
		}
	}

	if version < 4 {
		if err := migrateV4(db); err != nil {
			return fmt.Errorf("apply migration v4: %w", err)
		}
	}

	if version < 5 {
		if err := migrateV5(db); err != nil {
			return fmt.Errorf("apply migration v5: %w", err)
		}
	}

	if version < 6 {
		if err := migrateV6(db); err != nil {
			return fmt.Errorf("apply migration v6: %w", err)
		}
	}

	if version < 7 {
		if err := migrateV7(db); err != nil {
			return fmt.Errorf("apply migration v7: %w", err)
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

func migrateV3(db *sqlx.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS tracked_items (
			id              INTEGER PRIMARY KEY,
			project_id      INTEGER REFERENCES projects(id) ON DELETE CASCADE,
			source          TEXT NOT NULL DEFAULT 'github',
			owner           TEXT NOT NULL,
			repo            TEXT NOT NULL,
			number          INTEGER NOT NULL,
			item_type       TEXT NOT NULL DEFAULT 'issue',
			title           TEXT NOT NULL DEFAULT '',
			state           TEXT NOT NULL DEFAULT 'open',
			labels          TEXT NOT NULL DEFAULT '',
			last_status     TEXT NOT NULL DEFAULT 'Open',
			last_checked_at TEXT DEFAULT '',
			created_at      TEXT DEFAULT (datetime('now')),
			UNIQUE(project_id, source, owner, repo, number)
		)
	`)
	if err != nil {
		return fmt.Errorf("create tracked_items table: %w", err)
	}
	return nil
}

func migrateV4(db *sqlx.DB) error {
	stmts := []string{
		`ALTER TABLE tracked_items ADD COLUMN content_hash TEXT DEFAULT ''`,
		`ALTER TABLE tracked_items ADD COLUMN objective_summary TEXT DEFAULT ''`,
		`ALTER TABLE tracked_items ADD COLUMN progress_summary TEXT DEFAULT ''`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	// Ensure no NULLs in new text columns.
	for _, col := range []string{"content_hash", "objective_summary", "progress_summary"} {
		if _, err := db.Exec(fmt.Sprintf(`UPDATE tracked_items SET %s = '' WHERE %s IS NULL`, col, col)); err != nil {
			return fmt.Errorf("null-fill %s: %w", col, err)
		}
	}
	return nil
}

func migrateV5(db *sqlx.DB) error {
	_, err := db.Exec(`ALTER TABLE projects ADD COLUMN idle_pause_sec INTEGER DEFAULT 14400`)
	if err != nil {
		return fmt.Errorf("add idle_pause_sec: %w", err)
	}
	// Ensure no NULLs (Go int can't scan NULL).
	if _, err := db.Exec(`UPDATE projects SET idle_pause_sec = 14400 WHERE idle_pause_sec IS NULL`); err != nil {
		return fmt.Errorf("null-fill idle_pause_sec: %w", err)
	}
	return nil
}

func migrateV6(db *sqlx.DB) error {
	stmts := []string{
		`ALTER TABLE tracked_items ADD COLUMN is_draft BOOLEAN NOT NULL DEFAULT 0`,
		`ALTER TABLE tracked_items ADD COLUMN review_state TEXT NOT NULL DEFAULT ''`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

func migrateV7(db *sqlx.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS completed_items (
			id           INTEGER PRIMARY KEY,
			project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			source       TEXT NOT NULL DEFAULT 'github',
			owner        TEXT NOT NULL,
			repo         TEXT NOT NULL,
			number       INTEGER NOT NULL,
			item_type    TEXT NOT NULL DEFAULT 'issue',
			title        TEXT NOT NULL DEFAULT '',
			final_status TEXT NOT NULL DEFAULT 'Closd',
			summary      TEXT NOT NULL DEFAULT '',
			completed_at TEXT DEFAULT (datetime('now'))
		)
	`)
	if err != nil {
		return fmt.Errorf("create completed_items table: %w", err)
	}
	return nil
}

// DefaultDBPath returns the default path for the agent-minder database.
func DefaultDBPath() string {
	return ExpandHome("~/.agent-minder/minder.db")
}
