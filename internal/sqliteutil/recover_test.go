package sqliteutil

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

func TestOpenWithRecoveryHealthyDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := OpenWithRecovery(dbPath, dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("OpenWithRecovery on fresh DB: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Should be able to create a table and query.
	_, err = db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY)")
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
}

func TestOpenWithRecoveryStaleSHM(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create a valid DB first.
	db, err := sqlx.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("create DB: %v", err)
	}
	_, err = db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY); INSERT INTO t VALUES (1)")
	if err != nil {
		t.Fatalf("seed DB: %v", err)
	}
	_ = db.Close()

	// Write garbage to the -shm file to simulate a stale/corrupt shm.
	shmPath := dbPath + "-shm"
	if err := os.WriteFile(shmPath, []byte("corrupt garbage data that will cause issues"), 0644); err != nil {
		t.Fatalf("write stale shm: %v", err)
	}

	// OpenWithRecovery should either succeed directly (SQLite handles it)
	// or recover by removing stale files and retrying.
	db2, err := OpenWithRecovery(dbPath, dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("OpenWithRecovery with stale shm: %v", err)
	}
	defer func() { _ = db2.Close() }()

	// Data should still be accessible.
	var count int
	if err := db2.Get(&count, "SELECT COUNT(*) FROM t"); err != nil {
		t.Fatalf("query after recovery: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestRemoveStaleFiles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create the stale files.
	for _, suffix := range []string{"-shm", "-wal"} {
		if err := os.WriteFile(dbPath+suffix, []byte("stale"), 0644); err != nil {
			t.Fatalf("create %s: %v", suffix, err)
		}
	}

	if err := removeStaleFiles(dbPath); err != nil {
		t.Fatalf("removeStaleFiles: %v", err)
	}

	// Both should be gone.
	for _, suffix := range []string{"-shm", "-wal"} {
		if _, err := os.Stat(dbPath + suffix); !os.IsNotExist(err) {
			t.Errorf("%s still exists after removal", suffix)
		}
	}
}

func TestRemoveStaleFilesNoFiles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nonexistent.db")

	// Should not error when files don't exist.
	if err := removeStaleFiles(dbPath); err != nil {
		t.Fatalf("removeStaleFiles on missing files: %v", err)
	}
}

func TestIsIOError(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"disk I/O error", true},
		{"in prepare, disk I/O error (10)", true},
		{"SQLITE_IOERR: something", true},
		{"table not found", false},
		{"", false},
	}
	for _, tt := range tests {
		err := fmt.Errorf("%s", tt.msg)
		if got := isIOError(err); got != tt.want {
			t.Errorf("isIOError(%q) = %v, want %v", tt.msg, got, tt.want)
		}
	}

	if isIOError(nil) {
		t.Error("isIOError(nil) should be false")
	}
}
