// Package logresolve provides the single, shared implementation of "given a
// job, which log file do we show?" It is the resolver behind both `minder
// logs` (local) and the daemon's `GET /jobs/{id}/log` route (remote), so the
// two stop drifting out of sync.
//
// A log is really identified by an agent_runs row: (job, stage, attempt).
// Jobs can span multiple stages and retried attempts, and agent_runs (v11)
// already records a per-run log_path. The job-level default — no stage or
// attempt given — resolves to the latest run's log, which matches prior
// behavior for single-stage, single-attempt jobs.
//
// For jobs predating agent_runs (or where the runs table has no matching
// row), Resolve falls back to the job's own agent_log column, and then to
// the on-disk naming convention the supervisor uses when writing logs.
package logresolve

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aptx-health/agent-minder/internal/db"
)

// ErrNotFound is returned when no log path could be resolved for the
// requested job/selector combination.
var ErrNotFound = errors.New("logresolve: log not found")

// Selector narrows which agent_runs row to resolve. The zero value selects
// the latest run overall (any stage, highest attempt).
type Selector struct {
	// Stage restricts resolution to runs of this stage name. Empty matches
	// any stage.
	Stage string
	// Attempt restricts resolution to this attempt number for the stage.
	// Zero (or negative) selects the latest attempt.
	Attempt int
}

// IsZero reports whether the selector requests default ("latest") behavior.
func (s Selector) IsZero() bool {
	return s.Stage == "" && s.Attempt <= 0
}

// Resolve returns the log file path for job under sel, along with the
// agent_runs row it came from (nil if resolved via the legacy job-level
// path instead of a run row).
//
// Resolution order:
//  1. agent_runs rows for the job, filtered/matched by sel; the row with
//     the highest ID among matches wins (runs are inserted in execution
//     order, so highest ID is latest).
//  2. If sel is the zero value (no explicit stage/attempt) and no run
//     matched, fall back to job.AgentLog.
//  3. If that's also empty, fall back to the on-disk naming convention
//     used by the supervisor when it creates a job's log file.
//
// A non-zero (explicit) selector that matches no run returns ErrNotFound —
// callers that ask for a specific stage/attempt should not be silently
// redirected to an unrelated log.
func Resolve(store *db.Store, job *db.Job, sel Selector) (string, *db.AgentRun, error) {
	if job == nil {
		return "", nil, fmt.Errorf("logresolve: nil job")
	}

	runs, err := store.GetAgentRuns(job.ID)
	if err != nil {
		return "", nil, fmt.Errorf("logresolve: get agent runs: %w", err)
	}

	if run := pickRun(runs, sel); run != nil {
		if run.LogPath.Valid && run.LogPath.String != "" {
			return run.LogPath.String, run, nil
		}
		// Matched a run, but it has no recorded log path — nothing else to
		// try for an explicit selector; fall through to legacy paths only
		// for the default (zero) selector, same as "no run matched".
	}

	if !sel.IsZero() {
		return "", nil, ErrNotFound
	}

	if job.AgentLog.Valid && job.AgentLog.String != "" {
		return job.AgentLog.String, nil, nil
	}

	for _, candidate := range ConventionPaths(job) {
		if fileExists(candidate) {
			return candidate, nil, nil
		}
	}

	return "", nil, ErrNotFound
}

// pickRun returns the matching run with the highest ID (i.e., most
// recently started), or nil if none match. runs is expected in ascending
// ID order, as returned by Store.GetAgentRuns.
func pickRun(runs []*db.AgentRun, sel Selector) *db.AgentRun {
	var best *db.AgentRun
	for _, r := range runs {
		if sel.Stage != "" && r.Stage != sel.Stage {
			continue
		}
		if sel.Attempt > 0 && r.Attempt != sel.Attempt {
			continue
		}
		best = r
	}
	return best
}

// ConventionPaths returns the on-disk paths the supervisor may have written
// job's log to, in the same order `minder logs` has historically tried
// them: newest naming convention first, then legacy ones.
func ConventionPaths(job *db.Job) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	base := filepath.Join(home, ".agent-minder", "agents")

	paths := []string{
		filepath.Join(base, fmt.Sprintf("%s-%s.log", job.DeploymentID, job.Name)),
	}
	if job.IssueNumber > 0 {
		paths = append(paths,
			filepath.Join(base, fmt.Sprintf("%s-issue-%d.log", job.DeploymentID, job.IssueNumber)),
		)
	}
	return paths
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
