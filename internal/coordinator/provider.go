package coordinator

import (
	"errors"

	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/eventbus"
	"github.com/aptx-health/agent-minder/internal/supervisor"
)

// RunInfo and Envelope are the Coordinator-owned names for the live-status and
// event types handlers consume. They alias the supervisor types today; the
// aliases exist so handler code never spells the supervisor package.
type (
	RunInfo  = supervisor.RunInfo
	Envelope = supervisor.Envelope
)

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
	// ActivationPolicy returns the persisted deployment activation intent.
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
}

var _ StateProvider = (*Coordinator)(nil)

// DeploymentID identifies the deployment this Coordinator serves.
func (c *Coordinator) DeploymentID() string { return c.deploy.ID }

// ActivationPolicy returns the persisted deployment activation intent.
func (c *Coordinator) ActivationPolicy() db.ActivationPolicy { return c.deploy.ActivationPolicy }

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
// is unknown or belongs to another deployment.
func (c *Coordinator) Job(id int64) (*db.Job, error) {
	job, err := c.store.GetJob(id)
	if err != nil {
		return nil, err
	}
	if job.DeploymentID != c.deploy.ID {
		return nil, ErrNotFound
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
