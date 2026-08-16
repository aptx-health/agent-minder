package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// DefaultEventRetention is the global count cap on durable events (Expedition
// IV R-7, retention decision assigned to M1-18). The cap is host-global rather
// than per-deployment: a chatty deployment can age out a quiet one's history,
// but the floor signal below stays correct per-stream either way, and the
// simpler policy avoids per-deployment sequence bookkeeping. Revisit if a real
// starvation case appears.
const DefaultEventRetention = 10000

// ErrEventsTruncated is the typed refusal for a replay cursor at or below the
// durable retention floor (R-7). The documented client response is
// snapshot-then-resubscribe from the snapshot's watermark.
var ErrEventsTruncated = errors.New("events truncated below requested cursor; resync from a snapshot")

// Event is one durable event row (Expedition IV §5). Its id is the only cursor
// that ever crosses a process or API boundary (ID-1, R-3).
type Event struct {
	ID           int64          `db:"id"`
	Time         time.Time      `db:"time"`
	DeploymentID string         `db:"deployment_id"`
	JobID        int64          `db:"job_id"`
	RunID        int64          `db:"run_id"`
	Type         string         `db:"type"`
	Severity     string         `db:"severity"`
	Summary      string         `db:"summary"`
	Data         sql.NullString `db:"data"`
}

// SnapshotMarker identifies the durable event history reflected by a snapshot.
// Both fields are read in the same SQLite transaction as the snapshot state.
type SnapshotMarker struct {
	Watermark int64
	LogEpoch  string
}

// ControlSnapshot is the deployment-scoped durable state needed by the v1
// control API. It is deliberately a database model rather than a wire model:
// internal/controlapi owns the JSON contract and maps these values through the
// Coordinator boundary.
type ControlSnapshot struct {
	Deployment *Deployment
	Jobs       []*Job
	Runs       []*AgentRun
	Schedules  []*JobSchedule
	Marker     SnapshotMarker
}

// WithTx runs fn inside a transaction, committing on nil and rolling back on
// error. It exists so a durable event can be appended in the same transaction
// as the state change it describes (R-1: commit is the publish).
func (s *Store) WithTx(fn func(*sqlx.Tx) error) error {
	tx, err := s.db.Beginx()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// AppendEventTx inserts a durable event inside an existing transaction and
// sets e.ID from the AUTOINCREMENT allocation. Callers pairing the event with
// a state change must run that change in the same transaction.
func AppendEventTx(tx *sqlx.Tx, e *Event) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	res, err := tx.Exec(`INSERT INTO events
		(time, deployment_id, job_id, run_id, type, severity, summary, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Time.UTC(), e.DeploymentID, e.JobID, e.RunID, e.Type, e.Severity, e.Summary, e.Data)
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	return nil
}

// AppendEvent inserts a durable event in its own transaction, for events whose
// only state change is the event itself (a decision with no paired table
// write).
func (s *Store) AppendEvent(e *Event) error {
	return s.WithTx(func(tx *sqlx.Tx) error { return AppendEventTx(tx, e) })
}

// eventWatermark computes the max durable event id visible to the given
// querier. The truncated_through floor keeps the watermark from regressing
// when retention has deleted every row.
func eventWatermark(q sqlx.Queryer) (int64, error) {
	var w int64
	err := sqlx.Get(q, &w, `SELECT MAX(
		COALESCE((SELECT MAX(id) FROM events), 0),
		COALESCE((SELECT truncated_through FROM event_log_meta WHERE id = 1), 0))`)
	return w, err
}

// EventWatermark returns the current max durable event id (Expedition IV §6).
func (s *Store) EventWatermark() (int64, error) {
	return eventWatermark(s.db)
}

// EventLogEpoch returns the identifier of this event history (ID-2). A client
// whose stored epoch mismatches must discard its cursor and resync.
func (s *Store) EventLogEpoch() (string, error) {
	var epoch string
	err := s.db.Get(&epoch, "SELECT epoch FROM event_log_meta WHERE id = 1")
	return epoch, err
}

// SnapshotMarker reads the durable watermark and log epoch atomically.
func (s *Store) SnapshotMarker() (SnapshotMarker, error) {
	var marker SnapshotMarker
	err := s.WithTx(func(tx *sqlx.Tx) error {
		var err error
		marker.Watermark, err = eventWatermark(tx)
		if err != nil {
			return err
		}
		return tx.Get(&marker.LogEpoch, "SELECT epoch FROM event_log_meta WHERE id = 1")
	})
	return marker, err
}

// SnapshotControl reads all durable state exposed by one worker's v1 read API
// together with its watermark and log epoch in one transaction. Every query is
// deployment-scoped, including the agent_runs join, so a Coordinator cannot
// return rows owned by another deployment sharing the database.
func (s *Store) SnapshotControl(deploymentID string) (*ControlSnapshot, error) {
	snapshot := &ControlSnapshot{
		Jobs:      make([]*Job, 0),
		Runs:      make([]*AgentRun, 0),
		Schedules: make([]*JobSchedule, 0),
	}
	err := s.WithTx(func(tx *sqlx.Tx) error {
		var deployment Deployment
		if err := tx.Get(&deployment, "SELECT * FROM deployments WHERE id = ?", deploymentID); err != nil {
			return err
		}
		snapshot.Deployment = &deployment

		if err := tx.Select(&snapshot.Jobs,
			"SELECT * FROM jobs WHERE deployment_id = ? ORDER BY id", deploymentID); err != nil {
			return err
		}
		if err := tx.Select(&snapshot.Runs, `SELECT agent_runs.* FROM agent_runs
			JOIN jobs ON jobs.id = agent_runs.job_id
			WHERE jobs.deployment_id = ? ORDER BY agent_runs.id`, deploymentID); err != nil {
			return err
		}
		if err := tx.Select(&snapshot.Schedules,
			"SELECT * FROM job_schedules WHERE deployment_id = ? ORDER BY name", deploymentID); err != nil {
			return err
		}

		var err error
		snapshot.Marker.Watermark, err = eventWatermark(tx)
		if err != nil {
			return err
		}
		return tx.Get(&snapshot.Marker.LogEpoch, "SELECT epoch FROM event_log_meta WHERE id = 1")
	})
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

// EventsTruncatedThrough returns the durable retention floor: the highest
// event id ever deleted by retention. Replay must start strictly above it.
func (s *Store) EventsTruncatedThrough() (int64, error) {
	var floor int64
	err := s.db.Get(&floor, "SELECT truncated_through FROM event_log_meta WHERE id = 1")
	return floor, err
}

// EventsAfter returns one deployment's durable events with id > afterID in id
// order (R-4/R-9: same scoping filter as live delivery). A cursor below the
// retention floor returns ErrEventsTruncated rather than a silent gap (R-6/
// R-7). limit <= 0 means no limit. The floor check and the read share one
// transaction so a concurrent prune cannot open a gap between them.
func (s *Store) EventsAfter(deploymentID string, afterID int64, limit int) ([]*Event, error) {
	var events []*Event
	err := s.WithTx(func(tx *sqlx.Tx) error {
		var floor int64
		if err := tx.Get(&floor, "SELECT truncated_through FROM event_log_meta WHERE id = 1"); err != nil {
			return err
		}
		if afterID < floor {
			return fmt.Errorf("%w (cursor %d, floor %d)", ErrEventsTruncated, afterID, floor)
		}
		query := "SELECT * FROM events WHERE deployment_id = ? AND id > ? ORDER BY id"
		args := []interface{}{deploymentID, afterID}
		if limit > 0 {
			query += " LIMIT ?"
			args = append(args, limit)
		}
		return tx.Select(&events, query, args...)
	})
	if err != nil {
		return nil, err
	}
	return events, nil
}

// SnapshotJobs returns a deployment's jobs plus the event watermark, read in a
// single transaction (Expedition IV §6): under store-first appends, the
// returned jobs reflect exactly the durable events with id <= watermark.
func (s *Store) SnapshotJobs(deploymentID string) ([]*Job, int64, error) {
	var jobs []*Job
	var watermark int64
	err := s.WithTx(func(tx *sqlx.Tx) error {
		if err := tx.Select(&jobs, "SELECT * FROM jobs WHERE deployment_id = ? ORDER BY id", deploymentID); err != nil {
			return err
		}
		var err error
		watermark, err = eventWatermark(tx)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return jobs, watermark, nil
}

// PruneEvents deletes the oldest durable events beyond maxRetained and
// advances the retention floor in the same transaction (R-7: retention is
// deletion plus an explicit floor, never compaction). Surviving rows are
// never mutated and AUTOINCREMENT guarantees deleted ids are never reused
// (V-6). Returns the number of rows deleted.
func (s *Store) PruneEvents(maxRetained int) (int64, error) {
	if maxRetained < 0 {
		maxRetained = 0
	}
	var deleted int64
	err := s.WithTx(func(tx *sqlx.Tx) error {
		// The newest row falling outside the retained window, if any.
		var cutoff int64
		err := tx.Get(&cutoff, "SELECT id FROM events ORDER BY id DESC LIMIT 1 OFFSET ?", maxRetained)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		res, err := tx.Exec("DELETE FROM events WHERE id <= ?", cutoff)
		if err != nil {
			return err
		}
		deleted, _ = res.RowsAffected()
		_, err = tx.Exec("UPDATE event_log_meta SET truncated_through = MAX(truncated_through, ?) WHERE id = 1", cutoff)
		return err
	})
	return deleted, err
}
