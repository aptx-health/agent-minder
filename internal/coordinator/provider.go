package coordinator

import (
	"database/sql"
	"errors"

	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/eventbus"
	"github.com/aptx-health/agent-minder/internal/logresolve"
	"github.com/aptx-health/agent-minder/internal/supervisor"
)

// RunInfo and Envelope are the Coordinator-owned names for the live-status and
// event types handlers consume. They alias the supervisor types today; the
// aliases exist so handler code never spells the supervisor package.
type (
	RunInfo  = supervisor.RunInfo
	Envelope = supervisor.Envelope
)

// SnapshotMarker identifies the durable state reflected by an API snapshot.
// Incarnation is added by the transport envelope because it scopes live state,
// not the SQLite transaction.
type SnapshotMarker struct {
	Watermark int64
	LogEpoch  string
}

// DeploymentRead is an endpoint-shaped deployment snapshot. Endpoint-shaped
// reads keep future handlers from composing a state read with a later
// EventWatermark call. Their durable fields and Marker always come from one
// SQLite read transaction.
type DeploymentRead struct {
	Deployment *db.Deployment
	Marker     SnapshotMarker
}

type AutomationsRead struct {
	Automations []Automation
	Marker      SnapshotMarker
}

type JobsRead struct {
	Deployment *db.Deployment
	Jobs       []*db.Job
	Marker     SnapshotMarker
}

type JobRead struct {
	Deployment *db.Deployment
	Job        *db.Job
	Marker     SnapshotMarker
}

type AgentRunsRead struct {
	Runs   []*db.AgentRun
	Marker SnapshotMarker
}

// LogRead couples a run identity with whether the shared resolver can address
// its log. The local path intentionally does not cross the provider boundary.
type LogRead struct {
	Run       *db.AgentRun
	Available bool
}

type LogsRead struct {
	Logs   []LogRead
	Marker SnapshotMarker
}

// ErrNotFound is returned by scoped reads when the requested row does not
// exist within this Coordinator's deployment.
var ErrNotFound = errors.New("not found in this deployment")

// StateProvider is the control surface API handlers consume (Expedition I
// DM-5): the lifecycle trio (stop, budget-resume, budget-paused), live run
// info, the automations snapshot, event subscription, and deployment-scoped
// store reads. Handlers depend on this interface only; the Coordinator is its
// sole implementation and the only component that sees the *Supervisor behind
// it. Per Expedition II §9 the interface is served in-worker — the M2
// aggregator consumes worker APIs as a client and never links this interface.
type StateProvider interface {
	// DeploymentID identifies the deployment this provider serves.
	DeploymentID() string
	// WorkerIncarnation scopes advisory live fields to this Coordinator.
	WorkerIncarnation() string
	// SnapshotMarker atomically reads the durable watermark and log epoch.
	SnapshotMarker() (SnapshotMarker, error)
	// ConfigRevision and ActivationPolicy expose Coordinator-owned startup
	// identity without requiring handlers to inspect its internals.
	ConfigRevision() ConfigRevision
	ActivationPolicy() db.ActivationPolicy

	// Stop halts the deployment: cancels scheduler and supervisor work and
	// drains in-flight jobs.
	Stop()
	// ResumeBudget clears the budget-paused state.
	ResumeBudget()
	// IsBudgetPaused reports whether launches are paused at the budget ceiling.
	IsBudgetPaused() bool

	// RunningJobs lists currently executing jobs with live status.
	RunningJobs() []RunInfo
	// Snapshot returns the active watch, trigger, and cron automations.
	Snapshot() []Automation
	// SubscribeEvents returns supervisor events strictly after afterCursor,
	// followed by live events. The cursor is in-process only and never crosses
	// an API boundary (Expedition IV R-3).
	SubscribeEvents(afterCursor uint64) (*eventbus.Subscription[Envelope], error)

	// Deployment-scoped store reads. Job returns ErrNotFound for rows that
	// exist but belong to another deployment, so handlers cannot leak across
	// deployments sharing the host DB.
	Deployment() (*db.Deployment, error)
	Jobs() ([]*db.Job, error)
	Job(id int64) (*db.Job, error)
	TotalSpend() (float64, error)
	DepGraph() (*db.DepGraph, error)
	ActiveLessons() ([]*db.Lesson, error)

	// Atomic, endpoint-shaped v1 snapshot reads.
	ReadDeploymentSnapshot() (DeploymentRead, error)
	ReadAutomationsSnapshot() (AutomationsRead, error)
	ReadJobsSnapshot() (JobsRead, error)
	ReadJobSnapshot(id int64) (JobRead, error)
	ReadAgentRunsSnapshot(jobID int64) (AgentRunsRead, error)
	ReadLogsSnapshot(jobID int64) (LogsRead, error)
}

var _ StateProvider = (*Coordinator)(nil)

// DeploymentID identifies the deployment this Coordinator serves.
func (c *Coordinator) DeploymentID() string { return c.deploy.ID }

// SnapshotMarker reads both durable snapshot identifiers in one transaction.
func (c *Coordinator) SnapshotMarker() (SnapshotMarker, error) {
	marker, err := c.store.SnapshotMarker()
	return SnapshotMarker{Watermark: marker.Watermark, LogEpoch: marker.LogEpoch}, err
}

// ResumeBudget clears the supervisor's budget-paused state.
func (c *Coordinator) ResumeBudget() { c.sup.ResumeBudget() }

// IsBudgetPaused reports whether launches are paused at the budget ceiling.
func (c *Coordinator) IsBudgetPaused() bool { return c.sup.IsBudgetPaused() }

// RunningJobs lists currently executing jobs with live status.
func (c *Coordinator) RunningJobs() []RunInfo { return c.sup.RunningJobs() }

// SubscribeEvents returns supervisor events strictly after afterCursor,
// followed by live events.
func (c *Coordinator) SubscribeEvents(afterCursor uint64) (*eventbus.Subscription[Envelope], error) {
	return c.sup.Subscribe(afterCursor)
}

// Deployment returns this deployment's record.
func (c *Coordinator) Deployment() (*db.Deployment, error) {
	return c.store.GetDeployment(c.deploy.ID)
}

// Jobs returns all jobs belonging to this deployment.
func (c *Coordinator) Jobs() ([]*db.Job, error) {
	return c.store.GetJobs(c.deploy.ID)
}

// Job returns one of this deployment's jobs by id, or ErrNotFound when the id
// is unknown or belongs to another deployment. The scoping lives in
// Store.GetJobForDeployment's query itself, not in a comparison here.
func (c *Coordinator) Job(id int64) (*db.Job, error) {
	job, err := c.store.GetJobForDeployment(c.deploy.ID, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return job, nil
}

// TotalSpend returns the deployment's accumulated cost.
func (c *Coordinator) TotalSpend() (float64, error) {
	return c.store.TotalSpend(c.deploy.ID)
}

// DepGraph returns the deployment's stored dependency graph.
func (c *Coordinator) DepGraph() (*db.DepGraph, error) {
	return c.store.GetDepGraph(c.deploy.ID)
}

// ActiveLessons returns the active lessons scoped to this deployment's repo.
func (c *Coordinator) ActiveLessons() ([]*db.Lesson, error) {
	return c.store.GetActiveLessons(c.deploy.Owner + "/" + c.deploy.Repo)
}

func markerFromDB(marker db.SnapshotMarker) SnapshotMarker {
	return SnapshotMarker{Watermark: marker.Watermark, LogEpoch: marker.LogEpoch}
}

// ReadDeploymentSnapshot returns this deployment and its durable marker from
// the same SQLite transaction.
func (c *Coordinator) ReadDeploymentSnapshot() (DeploymentRead, error) {
	snapshot, err := c.store.SnapshotControl(c.deploy.ID)
	if err != nil {
		return DeploymentRead{}, err
	}
	return DeploymentRead{Deployment: snapshot.Deployment, Marker: markerFromDB(snapshot.Marker)}, nil
}

// ReadAutomationsSnapshot combines Coordinator-owned live configuration with
// deployment-scoped durable schedules/jobs read in one transaction. The live
// portion is advisory and is scoped by WorkerIncarnation in every envelope.
func (c *Coordinator) ReadAutomationsSnapshot() (AutomationsRead, error) {
	snapshot, err := c.store.SnapshotControl(c.deploy.ID)
	if err != nil {
		return AutomationsRead{}, err
	}
	automations := computeAutomationsFromSnapshot(snapshot.Deployment, c.routes, snapshot.Schedules, snapshot.Jobs)
	return AutomationsRead{Automations: automations, Marker: markerFromDB(snapshot.Marker)}, nil
}

// ReadJobsSnapshot returns only jobs owned by this Coordinator's deployment.
func (c *Coordinator) ReadJobsSnapshot() (JobsRead, error) {
	snapshot, err := c.store.SnapshotControl(c.deploy.ID)
	if err != nil {
		return JobsRead{}, err
	}
	return JobsRead{Deployment: snapshot.Deployment, Jobs: snapshot.Jobs, Marker: markerFromDB(snapshot.Marker)}, nil
}

// ReadJobSnapshot returns a deployment-owned job or ErrNotFound.
func (c *Coordinator) ReadJobSnapshot(id int64) (JobRead, error) {
	snapshot, err := c.store.SnapshotControl(c.deploy.ID)
	if err != nil {
		return JobRead{}, err
	}
	for _, job := range snapshot.Jobs {
		if job.ID == id {
			return JobRead{Deployment: snapshot.Deployment, Job: job, Marker: markerFromDB(snapshot.Marker)}, nil
		}
	}
	return JobRead{}, ErrNotFound
}

// ReadAgentRunsSnapshot verifies job ownership before returning its runs.
func (c *Coordinator) ReadAgentRunsSnapshot(jobID int64) (AgentRunsRead, error) {
	snapshot, err := c.store.SnapshotControl(c.deploy.ID)
	if err != nil {
		return AgentRunsRead{}, err
	}
	owned := false
	for _, job := range snapshot.Jobs {
		if job.ID == jobID {
			owned = true
			break
		}
	}
	if !owned {
		return AgentRunsRead{}, ErrNotFound
	}
	runs := make([]*db.AgentRun, 0)
	for _, run := range snapshot.Runs {
		if run.JobID == jobID {
			runs = append(runs, run)
		}
	}
	return AgentRunsRead{Runs: runs, Marker: markerFromDB(snapshot.Marker)}, nil
}

// ReadLogsSnapshot verifies job ownership and resolves each run's log from the
// same preloaded snapshot used to produce the durable marker. Resolution is
// advisory filesystem state and is scoped by the worker incarnation.
func (c *Coordinator) ReadLogsSnapshot(jobID int64) (LogsRead, error) {
	snapshot, err := c.store.SnapshotControl(c.deploy.ID)
	if err != nil {
		return LogsRead{}, err
	}
	var ownedJob *db.Job
	for _, job := range snapshot.Jobs {
		if job.ID == jobID {
			ownedJob = job
			break
		}
	}
	if ownedJob == nil {
		return LogsRead{}, ErrNotFound
	}
	runs := make([]*db.AgentRun, 0)
	for _, run := range snapshot.Runs {
		if run.JobID == jobID {
			runs = append(runs, run)
		}
	}
	logs := make([]LogRead, 0, len(runs))
	for _, run := range runs {
		_, _, resolveErr := logresolve.ResolveFromRuns(ownedJob, runs, logresolve.Selector{
			Stage: run.Stage, Attempt: run.Attempt,
		})
		logs = append(logs, LogRead{Run: run, Available: resolveErr == nil})
	}
	return LogsRead{Logs: logs, Marker: markerFromDB(snapshot.Marker)}, nil
}
