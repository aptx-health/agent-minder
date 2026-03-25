// Package sqliteutil provides shared SQLite health-check and recovery utilities.
package sqliteutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// CheckAndRecover attempts to detect and fix stale WAL/SHM files that cause
// "disk I/O error" on SQLite databases using WAL journal mode. This happens
// when a process crashes or doesn't cleanly close its connection, leaving
// behind -shm and -wal files that reference invalid shared memory state.
//
// Recovery strategy:
//  1. Try to ping the database
//  2. If that succeeds, run PRAGMA integrity_check as a deeper validation
//  3. If either fails, close the connection, remove stale -shm/-wal files,
//     and return an error indicating the caller should retry
//
// Returns (healthy bool, error). If healthy is false and error is nil,
// stale files were removed and the caller should reopen the connection.
func CheckAndRecover(db *sqlx.DB, dbPath string) (bool, error) {
	return CheckAndRecoverContext(context.Background(), db, dbPath)
}

// CheckAndRecoverContext is like CheckAndRecover but respects context cancellation.
func CheckAndRecoverContext(ctx context.Context, db *sqlx.DB, dbPath string) (bool, error) {
	// Quick check: can we ping?
	if err := db.PingContext(ctx); err != nil {
		if isIOError(err) {
			_ = db.Close()
			if removeErr := removeStaleFiles(dbPath); removeErr != nil {
				return false, fmt.Errorf("WAL recovery failed for %s: %w (original: %v)", dbPath, removeErr, err)
			}
			return false, nil
		}
		return false, fmt.Errorf("ping failed: %w", err)
	}

	// Deeper check: integrity_check catches corrupted WAL state that ping misses.
	var result string
	if err := db.GetContext(ctx, &result, "PRAGMA integrity_check"); err != nil {
		if isIOError(err) {
			_ = db.Close()
			if removeErr := removeStaleFiles(dbPath); removeErr != nil {
				return false, fmt.Errorf("WAL recovery failed for %s: %w (original: %v)", dbPath, removeErr, err)
			}
			return false, nil
		}
		return false, fmt.Errorf("integrity check failed: %w", err)
	}

	if result != "ok" {
		return false, fmt.Errorf("integrity check returned: %s", result)
	}

	return true, nil
}

// removeStaleFiles removes -shm and -wal files for a database path.
// These files are safe to remove when no process has the database open,
// and their removal allows SQLite to rebuild them on next connection.
func removeStaleFiles(dbPath string) error {
	var errs []string
	for _, suffix := range []string{"-shm", "-wal"} {
		path := dbPath + suffix
		if _, err := os.Stat(path); err == nil {
			if err := os.Remove(path); err != nil {
				errs = append(errs, fmt.Sprintf("remove %s: %v", path, err))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// isIOError checks if an error is a SQLite disk I/O error (code 10).
func isIOError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "disk I/O error") ||
		strings.Contains(msg, "(10)") ||
		strings.Contains(msg, "SQLITE_IOERR")
}

// OpenWithRecovery opens a SQLite database with the given DSN, performing
// health checks and automatic WAL recovery if stale files are detected.
// It will attempt recovery once before giving up.
func OpenWithRecovery(dbPath, dsn string) (*sqlx.DB, error) {
	return OpenWithRecoveryContext(context.Background(), dbPath, dsn)
}

// OpenWithRecoveryContext is like OpenWithRecovery but respects context cancellation
// and configures the connection pool for resilience after sleep/wake cycles.
func OpenWithRecoveryContext(ctx context.Context, dbPath, dsn string) (*sqlx.DB, error) {
	db, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	configurePool(db)

	healthy, err := CheckAndRecoverContext(ctx, db, dbPath)
	if healthy {
		return db, nil
	}

	if err != nil {
		// Recovery failed or non-recoverable error.
		_ = db.Close()
		return nil, fmt.Errorf("health check %s: %w", dbPath, err)
	}

	// Stale files were removed — retry once.
	db, err = sqlx.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("reopen after recovery %s: %w", dbPath, err)
	}
	configurePool(db)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping after recovery %s: %w", dbPath, err)
	}

	return db, nil
}

// configurePool sets connection pool parameters for resilience.
// SQLite is single-writer, so one connection suffices. ConnMaxIdleTime
// ensures stale connections (e.g., after laptop sleep) are recycled.
func configurePool(db *sqlx.DB) {
	db.SetMaxOpenConns(1)
	db.SetConnMaxIdleTime(5 * time.Minute)
}
