package db

import (
	"fmt"
	"os"
	"strings"

	"github.com/dustinlange/agent-minder/internal/sqliteutil"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

const currentVersion = 23

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
	llm_summarizer_provider TEXT DEFAULT '',
	llm_analyzer_provider   TEXT DEFAULT '',
	idle_pause_sec        INTEGER DEFAULT 14400,
	analyzer_focus        TEXT DEFAULT '',
	autopilot_filter_type  TEXT DEFAULT '',
	autopilot_filter_value TEXT DEFAULT '',
	status_interval_sec    INTEGER DEFAULT 300,
	analysis_interval_sec  INTEGER DEFAULT 1800,
	autopilot_max_agents   INTEGER DEFAULT 3,
	autopilot_max_turns    INTEGER DEFAULT 50,
	autopilot_max_budget_usd REAL DEFAULT 3.00,
	autopilot_skip_label   TEXT DEFAULT 'no-agent',
	autopilot_base_branch  TEXT DEFAULT '',
	is_deploy             INTEGER DEFAULT 0,
	analyzer_session_id          TEXT DEFAULT '',
	autopilot_auto_merge         INTEGER DEFAULT 0,
	autopilot_review_max_turns   INTEGER,
	autopilot_review_max_budget_usd REAL,
	created_at                   TEXT DEFAULT (datetime('now'))
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

CREATE TABLE IF NOT EXISTS autopilot_tasks (
	id             INTEGER PRIMARY KEY,
	project_id     INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	owner          TEXT NOT NULL DEFAULT '',
	repo           TEXT NOT NULL DEFAULT '',
	issue_number   INTEGER NOT NULL,
	issue_title    TEXT NOT NULL DEFAULT '',
	issue_body     TEXT NOT NULL DEFAULT '',
	dependencies   TEXT DEFAULT '[]',
	status         TEXT NOT NULL DEFAULT 'queued',
	worktree_path  TEXT DEFAULT '',
	branch         TEXT DEFAULT '',
	pr_number      INTEGER DEFAULT 0,
	agent_log      TEXT DEFAULT '',
	started_at     TEXT DEFAULT '',
	completed_at   TEXT DEFAULT '',
	failure_reason TEXT DEFAULT '',
	failure_detail TEXT DEFAULT '',
	cost_usd              REAL DEFAULT 0,
	max_turns_override    INTEGER,
	max_budget_override   REAL,
	review_risk           TEXT,
	review_comment_id     INTEGER,
	UNIQUE(project_id, issue_number)
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

CREATE TABLE IF NOT EXISTS repo_onboarding (
	id                INTEGER PRIMARY KEY,
	repo_id           INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
	onboarding_yaml   TEXT NOT NULL DEFAULT '',
	onboarded_at      TEXT DEFAULT (datetime('now')),
	validated_at      TEXT DEFAULT '',
	validation_status TEXT NOT NULL DEFAULT 'untested',
	UNIQUE(repo_id)
);

CREATE TABLE IF NOT EXISTS autopilot_dep_graphs (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	graph_json  TEXT NOT NULL,
	option_name TEXT DEFAULT '',
	created_at  TEXT DEFAULT (datetime('now')),
	UNIQUE(project_id)
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
		_ = db.Close()
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

	if version < 8 {
		if err := migrateV8(db); err != nil {
			return fmt.Errorf("apply migration v8: %w", err)
		}
	}

	if version < 9 {
		if err := migrateV9(db); err != nil {
			return fmt.Errorf("apply migration v9: %w", err)
		}
	}

	if version < 10 {
		if err := migrateV10(db); err != nil {
			return fmt.Errorf("apply migration v10: %w", err)
		}
	}

	if version < 11 {
		if err := migrateV11(db); err != nil {
			return fmt.Errorf("apply migration v11: %w", err)
		}
	}

	if version < 12 {
		if err := migrateV12(db); err != nil {
			return fmt.Errorf("apply migration v12: %w", err)
		}
	}

	if version < 13 {
		if err := migrateV13(db); err != nil {
			return fmt.Errorf("apply migration v13: %w", err)
		}
	}

	if version < 14 {
		if err := migrateV14(db); err != nil {
			return fmt.Errorf("apply migration v14: %w", err)
		}
	}

	if version < 15 {
		if err := migrateV15(db); err != nil {
			return fmt.Errorf("apply migration v15: %w", err)
		}
	}

	if version < 16 {
		if err := migrateV16(db); err != nil {
			return fmt.Errorf("apply migration v16: %w", err)
		}
	}

	if version < 17 {
		if err := migrateV17(db); err != nil {
			return fmt.Errorf("apply migration v17: %w", err)
		}
	}

	if version < 18 {
		if err := migrateV18(db); err != nil {
			return fmt.Errorf("apply migration v18: %w", err)
		}
	}

	if version < 19 {
		if err := migrateV19(db); err != nil {
			return fmt.Errorf("apply migration v19: %w", err)
		}
	}

	if version < 20 {
		if err := migrateV20(db); err != nil {
			return fmt.Errorf("apply migration v20: %w", err)
		}
	}

	if version < 21 {
		if err := migrateV21(db); err != nil {
			return fmt.Errorf("apply migration v21: %w", err)
		}
	}

	if version < 22 {
		if err := migrateV22(db); err != nil {
			return fmt.Errorf("apply migration v22: %w", err)
		}
	}

	if version < 23 {
		if err := migrateV23(db); err != nil {
			return fmt.Errorf("apply migration v23: %w", err)
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

func migrateV8(db *sqlx.DB) error {
	_, err := db.Exec(`ALTER TABLE projects ADD COLUMN analyzer_focus TEXT DEFAULT ''`)
	if err != nil {
		return fmt.Errorf("add analyzer_focus: %w", err)
	}
	if _, err := db.Exec(`UPDATE projects SET analyzer_focus = '' WHERE analyzer_focus IS NULL`); err != nil {
		return fmt.Errorf("null-fill analyzer_focus: %w", err)
	}
	return nil
}

func migrateV9(db *sqlx.DB) error {
	// Create autopilot_tasks table.
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS autopilot_tasks (
			id             INTEGER PRIMARY KEY,
			project_id     INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			issue_number   INTEGER NOT NULL,
			issue_title    TEXT NOT NULL DEFAULT '',
			issue_body     TEXT NOT NULL DEFAULT '',
			dependencies   TEXT DEFAULT '[]',
			status         TEXT NOT NULL DEFAULT 'queued',
			worktree_path  TEXT DEFAULT '',
			branch         TEXT DEFAULT '',
			pr_number      INTEGER DEFAULT 0,
			agent_log      TEXT DEFAULT '',
			started_at     TEXT DEFAULT '',
			completed_at   TEXT DEFAULT '',
			UNIQUE(project_id, issue_number)
		)
	`)
	if err != nil {
		return fmt.Errorf("create autopilot_tasks table: %w", err)
	}

	// Add autopilot columns to projects.
	stmts := []string{
		`ALTER TABLE projects ADD COLUMN autopilot_filter_type TEXT DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN autopilot_filter_value TEXT DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN autopilot_max_agents INTEGER DEFAULT 3`,
		`ALTER TABLE projects ADD COLUMN autopilot_max_turns INTEGER DEFAULT 50`,
		`ALTER TABLE projects ADD COLUMN autopilot_max_budget_usd REAL DEFAULT 3.00`,
		`ALTER TABLE projects ADD COLUMN autopilot_skip_label TEXT DEFAULT 'no-agent'`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	// Null-fill new text columns.
	for _, col := range []string{"autopilot_filter_type", "autopilot_filter_value", "autopilot_skip_label"} {
		if _, err := db.Exec(fmt.Sprintf(`UPDATE projects SET %s = '' WHERE %s IS NULL`, col, col)); err != nil {
			return fmt.Errorf("null-fill %s: %w", col, err)
		}
	}
	return nil
}

func migrateV10(db *sqlx.DB) error {
	_, err := db.Exec(`ALTER TABLE projects ADD COLUMN autopilot_base_branch TEXT DEFAULT ''`)
	if err != nil {
		return fmt.Errorf("add autopilot_base_branch: %w", err)
	}
	if _, err := db.Exec(`UPDATE projects SET autopilot_base_branch = '' WHERE autopilot_base_branch IS NULL`); err != nil {
		return fmt.Errorf("null-fill autopilot_base_branch: %w", err)
	}
	return nil
}

func migrateV11(db *sqlx.DB) error {
	stmts := []string{
		`ALTER TABLE projects ADD COLUMN status_interval_sec INTEGER DEFAULT 300`,
		`ALTER TABLE projects ADD COLUMN analysis_interval_sec INTEGER DEFAULT 1800`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	// Copy existing refresh_interval_sec into analysis_interval_sec for migration continuity.
	if _, err := db.Exec(`UPDATE projects SET analysis_interval_sec = refresh_interval_sec WHERE refresh_interval_sec > 0`); err != nil {
		return fmt.Errorf("copy refresh_interval to analysis_interval: %w", err)
	}
	return nil
}

func migrateV12(db *sqlx.DB) error {
	stmts := []string{
		`ALTER TABLE autopilot_tasks ADD COLUMN owner TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE autopilot_tasks ADD COLUMN repo TEXT NOT NULL DEFAULT ''`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

func migrateV13(db *sqlx.DB) error {
	stmts := []string{
		`ALTER TABLE projects ADD COLUMN llm_summarizer_provider TEXT DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN llm_analyzer_provider TEXT DEFAULT ''`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

func migrateV14(db *sqlx.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS repo_enrollments (
			id                INTEGER PRIMARY KEY,
			repo_id           INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
			enrollment_yaml   TEXT NOT NULL DEFAULT '',
			enrolled_at       TEXT DEFAULT (datetime('now')),
			validated_at      TEXT DEFAULT '',
			validation_status TEXT NOT NULL DEFAULT 'untested',
			UNIQUE(repo_id)
		)
	`)
	if err != nil {
		return fmt.Errorf("create repo_enrollments table: %w", err)
	}
	return nil
}

func migrateV15(db *sqlx.DB) error {
	stmts := []string{
		`ALTER TABLE autopilot_tasks ADD COLUMN failure_reason TEXT DEFAULT ''`,
		`ALTER TABLE autopilot_tasks ADD COLUMN failure_detail TEXT DEFAULT ''`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	// Null-fill new columns.
	for _, col := range []string{"failure_reason", "failure_detail"} {
		if _, err := db.Exec(fmt.Sprintf(`UPDATE autopilot_tasks SET %s = '' WHERE %s IS NULL`, col, col)); err != nil {
			return fmt.Errorf("null-fill %s: %w", col, err)
		}
	}
	return nil
}

func migrateV16(db *sqlx.DB) error {
	stmts := []string{
		`ALTER TABLE repo_enrollments RENAME TO repo_onboarding`,
		`ALTER TABLE repo_onboarding RENAME COLUMN enrollment_yaml TO onboarding_yaml`,
		`ALTER TABLE repo_onboarding RENAME COLUMN enrolled_at TO onboarded_at`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

func migrateV17(db *sqlx.DB) error {
	_, err := db.Exec(`ALTER TABLE autopilot_tasks ADD COLUMN cost_usd REAL DEFAULT 0`)
	if err != nil {
		return fmt.Errorf("add cost_usd: %w", err)
	}
	if _, err := db.Exec(`UPDATE autopilot_tasks SET cost_usd = 0 WHERE cost_usd IS NULL`); err != nil {
		return fmt.Errorf("null-fill cost_usd: %w", err)
	}
	return nil
}

// migrateV18 clears deprecated LLM provider columns now that all LLM calls
// go through the Claude Code CLI. Columns are kept for rollback safety.
func migrateV18(db *sqlx.DB) error {
	_, err := db.Exec(`UPDATE projects SET llm_provider = '', llm_model = '' WHERE llm_provider != '' OR llm_model != ''`)
	return err
}

func migrateV19(db *sqlx.DB) error {
	_, err := db.Exec(`ALTER TABLE projects ADD COLUMN is_deploy INTEGER DEFAULT 0`)
	if err != nil {
		return fmt.Errorf("add is_deploy: %w", err)
	}
	if _, err := db.Exec(`UPDATE projects SET is_deploy = 0 WHERE is_deploy IS NULL`); err != nil {
		return fmt.Errorf("null-fill is_deploy: %w", err)
	}
	return nil
}

func migrateV20(db *sqlx.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS autopilot_dep_graphs (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			graph_json  TEXT NOT NULL,
			option_name TEXT DEFAULT '',
			created_at  TEXT DEFAULT (datetime('now')),
			UNIQUE(project_id)
		)
	`)
	if err != nil {
		return fmt.Errorf("create autopilot_dep_graphs table: %w", err)
	}
	return nil
}

func migrateV21(db *sqlx.DB) error {
	stmts := []string{
		`ALTER TABLE autopilot_tasks ADD COLUMN max_turns_override INTEGER`,
		`ALTER TABLE autopilot_tasks ADD COLUMN max_budget_override REAL`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

func migrateV22(db *sqlx.DB) error {
	// ALTER TABLE ADD COLUMN will fail if the column already exists (e.g., fresh DB
	// created with schemaV1 that already includes it). Ignore "duplicate column" errors.
	_, err := db.Exec(`ALTER TABLE projects ADD COLUMN analyzer_session_id TEXT DEFAULT ''`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("add analyzer_session_id: %w", err)
	}
	if _, err := db.Exec(`UPDATE projects SET analyzer_session_id = '' WHERE analyzer_session_id IS NULL`); err != nil {
		return fmt.Errorf("null-fill analyzer_session_id: %w", err)
	}
	return nil
}

func migrateV23(db *sqlx.DB) error {
	stmts := []string{
		// Projects: review automation settings.
		`ALTER TABLE projects ADD COLUMN autopilot_auto_merge INTEGER DEFAULT 0`,
		`ALTER TABLE projects ADD COLUMN autopilot_review_max_turns INTEGER`,
		`ALTER TABLE projects ADD COLUMN autopilot_review_max_budget_usd REAL`,
		// Autopilot tasks: review pipeline columns.
		`ALTER TABLE autopilot_tasks ADD COLUMN review_risk TEXT`,
		`ALTER TABLE autopilot_tasks ADD COLUMN review_comment_id INTEGER`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			if strings.Contains(err.Error(), "duplicate column") {
				continue
			}
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	// Null-fill the boolean column so Go bool scanning works.
	if _, err := db.Exec(`UPDATE projects SET autopilot_auto_merge = 0 WHERE autopilot_auto_merge IS NULL`); err != nil {
		return fmt.Errorf("null-fill autopilot_auto_merge: %w", err)
	}
	return nil
}

// DefaultDBPath returns the agent-minder database path, respecting MINDER_DB env var.
func DefaultDBPath() string {
	if p := os.Getenv("MINDER_DB"); p != "" {
		return ExpandHome(p)
	}
	return ExpandHome("~/.agent-minder/minder.db")
}
