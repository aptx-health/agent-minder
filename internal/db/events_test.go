package db

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
)

func appendTestEvent(t *testing.T, s *Store, deploymentID, summary string) *Event {
	t.Helper()
	e := &Event{DeploymentID: deploymentID, Type: "info", Severity: "info", Summary: summary}
	if err := s.AppendEvent(e); err != nil {
		t.Fatalf("AppendEvent(%q): %v", summary, err)
	}
	return e
}

func TestEventAppendAndWatermark(t *testing.T) {
	s := testStore(t)

	if w, err := s.EventWatermark(); err != nil || w != 0 {
		t.Fatalf("empty log watermark = (%d, %v), want (0, nil)", w, err)
	}

	first := appendTestEvent(t, s, "d1", "one")
	second := appendTestEvent(t, s, "d1", "two")
	if first.ID == 0 || second.ID <= first.ID {
		t.Fatalf("ids not strictly increasing: %d then %d", first.ID, second.ID)
	}

	w, err := s.EventWatermark()
	if err != nil {
		t.Fatalf("EventWatermark: %v", err)
	}
	if w != second.ID {
		t.Errorf("watermark = %d, want %d", w, second.ID)
	}

	events, err := s.EventsAfter("d1", 0, 0)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(events) != 2 || events[0].Summary != "one" || events[1].Summary != "two" {
		t.Fatalf("EventsAfter returned %d events, want [one two]", len(events))
	}
	if events[0].Time.IsZero() {
		t.Error("stored event has zero time")
	}
}

// TestEventAppendInTransactionVisibility is V-1/V-2 at the SQL level
// (Expedition IV suite item 2): before the transaction commits, neither the
// state change nor its event is visible to another connection; after commit,
// both are. Commit is the publish.
func TestEventAppendInTransactionVisibility(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "visibility.db")
	conn, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()
	s := NewStore(conn)

	deploy := &Deployment{ID: "d1", RepoDir: dir, Owner: "acme", Repo: "widgets", Mode: "issues"}
	if err := s.CreateDeployment(deploy); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	job := &Job{DeploymentID: "d1", Agent: "autopilot", Name: "issue-1", Owner: "acme", Repo: "widgets", Status: StatusQueued}
	if err := s.CreateJob(job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// Independent reader connection: WAL mode gives it a consistent view that
	// must not include uncommitted writes from the store's transaction.
	reader, err := sqlx.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer func() { _ = reader.Close() }()

	readState := func() (status string, eventCount int) {
		t.Helper()
		if err := reader.Get(&status, "SELECT status FROM jobs WHERE id = ?", job.ID); err != nil {
			t.Fatalf("read status: %v", err)
		}
		if err := reader.Get(&eventCount, "SELECT COUNT(*) FROM events"); err != nil {
			t.Fatalf("count events: %v", err)
		}
		return status, eventCount
	}

	event := &Event{DeploymentID: "d1", JobID: job.ID, Type: "started", Severity: "info", Summary: "Agent started on #1"}
	err = s.WithTx(func(tx *sqlx.Tx) error {
		if err := UpdateJobStatusTx(tx, job.ID, StatusRunning); err != nil {
			return err
		}
		if err := AppendEventTx(tx, event); err != nil {
			return err
		}
		// Mid-transaction: the reader must see the old state and no event.
		status, count := readState()
		if status != StatusQueued {
			t.Errorf("pre-commit status = %q, want %q", status, StatusQueued)
		}
		if count != 0 {
			t.Errorf("pre-commit event count = %d, want 0", count)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	status, count := readState()
	if status != StatusRunning {
		t.Errorf("post-commit status = %q, want %q", status, StatusRunning)
	}
	if count != 1 {
		t.Errorf("post-commit event count = %d, want 1", count)
	}

	// The snapshot pairing (§6): jobs and watermark from one transaction agree.
	jobs, watermark, err := s.SnapshotJobs("d1")
	if err != nil {
		t.Fatalf("SnapshotJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Status != StatusRunning {
		t.Fatalf("snapshot jobs = %+v, want one running job", jobs)
	}
	if watermark != event.ID {
		t.Errorf("snapshot watermark = %d, want %d", watermark, event.ID)
	}
}

// TestEventRolledBackTransactionLeavesNothing: a failed transaction loses the
// state change and the event together (Expedition IV F2 — consistent by
// construction).
func TestEventRolledBackTransactionLeavesNothing(t *testing.T) {
	s := testStore(t)
	sentinel := errors.New("boom")
	event := &Event{DeploymentID: "d1", Type: "info", Severity: "info", Summary: "never happened"}
	err := s.WithTx(func(tx *sqlx.Tx) error {
		if err := AppendEventTx(tx, event); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTx error = %v, want sentinel", err)
	}
	if w, err := s.EventWatermark(); err != nil || w != 0 {
		t.Fatalf("watermark after rollback = (%d, %v), want (0, nil)", w, err)
	}
}

// TestEventIDsNeverReusedAfterDeletion is V-6: AUTOINCREMENT must keep
// allocating past deleted rows, because a reused cursor id silently corrupts
// every client holding the old one. This is the failure that would only
// appear after retention runs in production (Expedition IV trap 3).
func TestEventIDsNeverReusedAfterDeletion(t *testing.T) {
	s := testStore(t)

	var lastID int64
	for range 5 {
		lastID = appendTestEvent(t, s, "d1", "before prune").ID
	}

	// Prune everything.
	deleted, err := s.PruneEvents(0)
	if err != nil {
		t.Fatalf("PruneEvents: %v", err)
	}
	if deleted != 5 {
		t.Fatalf("pruned %d rows, want 5", deleted)
	}

	next := appendTestEvent(t, s, "d1", "after prune")
	if next.ID <= lastID {
		t.Fatalf("id %d reused after deletion (last was %d)", next.ID, lastID)
	}
}

func TestEventRetentionFloor(t *testing.T) {
	s := testStore(t)

	ids := make([]int64, 0, 5)
	for range 5 {
		ids = append(ids, appendTestEvent(t, s, "d1", "e").ID)
	}

	// Keep the newest 2; the floor advances to the highest deleted id.
	deleted, err := s.PruneEvents(2)
	if err != nil {
		t.Fatalf("PruneEvents: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("pruned %d rows, want 3", deleted)
	}
	floor, err := s.EventsTruncatedThrough()
	if err != nil {
		t.Fatalf("EventsTruncatedThrough: %v", err)
	}
	if floor != ids[2] {
		t.Errorf("floor = %d, want %d", floor, ids[2])
	}

	// Below the floor: typed refusal, never a silent gap (R-6/R-7).
	for _, cursor := range []int64{0, ids[1]} {
		if _, err := s.EventsAfter("d1", cursor, 0); !errors.Is(err, ErrEventsTruncated) {
			t.Errorf("EventsAfter(%d) error = %v, want ErrEventsTruncated", cursor, err)
		}
	}

	// At the floor: the surviving tail replays intact.
	events, err := s.EventsAfter("d1", floor, 0)
	if err != nil {
		t.Fatalf("EventsAfter(floor): %v", err)
	}
	if len(events) != 2 || events[0].ID != ids[3] || events[1].ID != ids[4] {
		t.Fatalf("replay from floor returned %d events, want ids %v", len(events), ids[3:])
	}

	// Watermark never regresses, even with the whole log pruned.
	if _, err := s.PruneEvents(0); err != nil {
		t.Fatalf("PruneEvents(0): %v", err)
	}
	w, err := s.EventWatermark()
	if err != nil {
		t.Fatalf("EventWatermark: %v", err)
	}
	if w != ids[4] {
		t.Errorf("watermark after full prune = %d, want %d", w, ids[4])
	}
}

// TestEventsAfterScopedByDeployment is R-9: replay applies the same
// deployment filter as live delivery even though the table is host-global.
func TestEventsAfterScopedByDeployment(t *testing.T) {
	s := testStore(t)
	appendTestEvent(t, s, "d1", "mine")
	foreign := appendTestEvent(t, s, "d2", "other deployment")
	appendTestEvent(t, s, "d1", "also mine")

	events, err := s.EventsAfter("d1", 0, 0)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	for _, e := range events {
		if e.DeploymentID != "d1" || e.ID == foreign.ID {
			t.Errorf("foreign deployment event leaked into replay: %+v", e)
		}
	}
}

func TestReadEventBatchAtomicMetadataAndRefusals(t *testing.T) {
	s := testStore(t)
	first := appendTestEvent(t, s, "d1", "one")
	foreign := appendTestEvent(t, s, "d2", "global hole")
	third := appendTestEvent(t, s, "d1", "three")
	epoch, err := s.EventLogEpoch()
	if err != nil {
		t.Fatal(err)
	}

	batch, err := s.ReadEventBatch("d1", 0, "", 10)
	if err != nil {
		t.Fatalf("ReadEventBatch: %v", err)
	}
	if batch.Head != third.ID || batch.LogEpoch != epoch || batch.RetentionFloor != 0 {
		t.Fatalf("metadata = %#v", batch)
	}
	if len(batch.Events) != 2 || batch.Events[0].ID != first.ID || batch.Events[1].ID != third.ID {
		t.Fatalf("deployment-scoped events = %#v", batch.Events)
	}

	// A cursor landing on another deployment's id is valid; clients must not
	// infer loss from holes in the host-global sequence.
	batch, err = s.ReadEventBatch("d1", foreign.ID, epoch, 10)
	if err != nil || len(batch.Events) != 1 || batch.Events[0].ID != third.ID {
		t.Fatalf("read from global hole = (%#v, %v)", batch, err)
	}
	if _, err := s.ReadEventBatch("d1", third.ID, "", 10); !errors.Is(err, ErrEventEpochRequired) {
		t.Fatalf("missing epoch error = %v", err)
	}
	if batch, err := s.ReadEventBatch("d1", third.ID, "wrong", 10); !errors.Is(err, ErrEventEpochMismatch) || batch.Head != third.ID {
		t.Fatalf("wrong epoch = (%#v, %v)", batch, err)
	}
	if batch, err := s.ReadEventBatch("d1", third.ID+1, epoch, 10); !errors.Is(err, ErrEventCursorAhead) || batch.Head != third.ID {
		t.Fatalf("ahead = (%#v, %v)", batch, err)
	}

	if _, err := s.PruneEvents(2); err != nil {
		t.Fatal(err)
	}
	floor, _ := s.EventsTruncatedThrough()
	if _, err := s.ReadEventBatch("d1", floor, epoch, 10); err != nil {
		t.Fatalf("cursor at floor refused: %v", err)
	}
	if batch, err := s.ReadEventBatch("d1", floor-1, epoch, 10); !errors.Is(err, ErrEventsTruncated) || batch.RetentionFloor != floor {
		t.Fatalf("below floor = (%#v, %v)", batch, err)
	}
}

func TestEventLogEpochStableAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "epoch.db")

	conn, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	epoch, err := NewStore(conn).EventLogEpoch()
	if err != nil {
		t.Fatalf("EventLogEpoch: %v", err)
	}
	if epoch == "" {
		t.Fatal("fresh database has empty log epoch")
	}
	_ = conn.Close()

	conn2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = conn2.Close() }()
	epoch2, err := NewStore(conn2).EventLogEpoch()
	if err != nil {
		t.Fatalf("EventLogEpoch after reopen: %v", err)
	}
	if epoch2 != epoch {
		t.Errorf("epoch changed across clean reopen: %q → %q", epoch, epoch2)
	}
}

// TestEventLogEpochRotatesAfterWALRecovery is F6: WAL recovery deletes the
// -wal file, potentially truncating committed event history, and leaves a
// marker; the next Open must rotate the epoch so clients discard their
// cursors and resync.
func TestEventLogEpochRotatesAfterWALRecovery(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "recovered.db")

	conn, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	epoch, err := NewStore(conn).EventLogEpoch()
	if err != nil {
		t.Fatalf("EventLogEpoch: %v", err)
	}
	_ = conn.Close()

	// Simulate sqliteutil's recovery having removed a -wal file.
	if err := os.WriteFile(dbPath+".wal-recovered", []byte("2026-08-09T00:00:00Z\n"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	conn2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen after recovery: %v", err)
	}
	defer func() { _ = conn2.Close() }()
	rotated, err := NewStore(conn2).EventLogEpoch()
	if err != nil {
		t.Fatalf("EventLogEpoch after recovery: %v", err)
	}
	if rotated == epoch {
		t.Error("epoch did not rotate after WAL recovery marker")
	}
	if _, err := os.Stat(dbPath + ".wal-recovered"); !os.IsNotExist(err) {
		t.Error("recovery marker was not consumed")
	}
}

func TestV12toV13EventsMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "migrate1213.db")

	conn, err := sqlx.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open v12 db: %v", err)
	}
	v12 := `
CREATE TABLE schema_version (version INTEGER NOT NULL);
INSERT INTO schema_version VALUES (12);
CREATE TABLE jobs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	deployment_id TEXT NOT NULL,
	agent TEXT NOT NULL DEFAULT 'autopilot',
	name TEXT NOT NULL,
	owner TEXT NOT NULL DEFAULT '',
	repo TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'queued'
);
INSERT INTO jobs (deployment_id, agent, name, status)
VALUES ('d1', 'autopilot', 'autopilot-issue-1', 'queued');
`
	if _, err := conn.Exec(v12); err != nil {
		t.Fatalf("create v12 db: %v", err)
	}
	_ = conn.Close()

	c2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (migration): %v", err)
	}
	defer func() { _ = c2.Close() }()

	var version int
	_ = c2.Get(&version, "SELECT version FROM schema_version")
	if version != schemaVersion {
		t.Errorf("got schema version %d, want %d", version, schemaVersion)
	}

	// Pre-existing rows survive, the event log is usable, and the epoch was
	// bootstrapped by the migration path.
	store := NewStore(c2)
	var status string
	if err := c2.Get(&status, "SELECT status FROM jobs WHERE id = 1"); err != nil || status != "queued" {
		t.Errorf("pre-existing job after migration = (%q, %v), want (queued, nil)", status, err)
	}
	if epoch, err := store.EventLogEpoch(); err != nil || epoch == "" {
		t.Errorf("migrated epoch = (%q, %v), want non-empty", epoch, err)
	}
	if floor, err := store.EventsTruncatedThrough(); err != nil || floor != 0 {
		t.Errorf("migrated floor = (%d, %v), want (0, nil)", floor, err)
	}
	e := appendTestEvent(t, store, "d1", "post-migration append")
	if e.ID != 1 {
		t.Errorf("first event id on migrated db = %d, want 1", e.ID)
	}
}

func TestFreshSchemaHasEventLog(t *testing.T) {
	s := testStore(t)

	for _, table := range []string{"events", "event_log_meta"} {
		var name sql.NullString
		err := s.DB().Get(&name, "SELECT name FROM sqlite_master WHERE type='table' AND name = ?", table)
		if err != nil || !name.Valid {
			t.Errorf("fresh schema is missing table %q (err %v)", table, err)
		}
	}
	if epoch, err := s.EventLogEpoch(); err != nil || epoch == "" {
		t.Errorf("fresh epoch = (%q, %v), want non-empty", epoch, err)
	}
}
