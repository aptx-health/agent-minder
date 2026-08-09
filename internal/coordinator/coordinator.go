// Package coordinator owns the per-deployment assembly shared by the foreground
// and daemon entry points: it wires a supervisor, an optional jobs.yaml
// scheduler, and the trigger routes derived from that config, then exposes a
// small lifecycle (Start/Run/Snapshot/Stop). Extracting this spine keeps the
// two entry points from drifting apart as they independently re-wired the same
// pieces.
package coordinator

import (
	"context"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/runtime"
	"github.com/aptx-health/agent-minder/internal/scheduler"
	"github.com/aptx-health/agent-minder/internal/supervisor"
)

// AutomationKind classifies an active subscription in a startup snapshot.
type AutomationKind string

const (
	AutomationWatch   AutomationKind = "watch"
	AutomationTrigger AutomationKind = "trigger"
	AutomationCron    AutomationKind = "cron"
)

// Automation is the computed, printable view of an active automation. Keeping
// this data separate from stdout lets callers reuse and test the loaded
// subscription map without capturing process output.
type Automation struct {
	Kind       AutomationKind
	Name       string
	Expression string
	Labels     []string
	Agent      string
	Runtime    string
	NextRunAt  time.Time
	HasNextRun bool
}

// Options carries the pre-resolved inputs a Coordinator needs. The caller owns
// the store, deployment record, and runtime resolution; the Coordinator owns
// everything assembled from them.
type Options struct {
	Store   *db.Store
	Deploy  *db.Deployment
	GHToken string
	// Runtime is the resolved doer runtime, or nil to keep the supervisor
	// default.
	Runtime runtime.AgentRuntime
}

// Coordinator owns the assembled per-deployment components and their lifecycle.
type Coordinator struct {
	store  *db.Store
	deploy *db.Deployment

	sup    *supervisor.Supervisor
	sched  *scheduler.Scheduler // nil when no jobs.yaml is present
	cfg    *scheduler.Config    // nil when no jobs.yaml is present
	routes []supervisor.TriggerRoute

	cancel      context.CancelFunc // cancels the context Start derived; nil before Start
	syncWarning error
}

// New assembles the supervisor, optional scheduler, and trigger routes for a
// deployment and applies the derived daemon-mode flag. It never prints; any
// non-fatal schedule-sync problem is surfaced via SyncWarning so the caller
// controls stdout. A returned error is fatal to the deployment.
func New(opts Options) (*Coordinator, error) {
	c := &Coordinator{
		store:  opts.Store,
		deploy: opts.Deploy,
	}

	c.sup = supervisor.New(opts.Store, opts.Deploy, opts.Deploy.RepoDir, opts.Deploy.Owner, opts.Deploy.Repo, opts.GHToken)
	if opts.Runtime != nil {
		c.sup.SetRuntime(opts.Runtime)
	}

	// Load jobs.yaml scheduler and trigger routes when present.
	cfgPath := scheduler.ConfigPath(opts.Deploy.RepoDir)
	if cfg, err := scheduler.LoadConfig(cfgPath); err == nil {
		c.cfg = cfg
		c.sched = scheduler.New(opts.Store, opts.Deploy.ID, opts.Deploy.Owner, opts.Deploy.Repo, cfg)
		if err := c.sched.SyncSchedules(); err != nil {
			c.syncWarning = err
		}
		c.routes = TriggerRoutesFromConfig(cfg)
		if len(c.routes) > 0 {
			c.sup.SetTriggerRoutes(c.routes)
		}
	}

	hasTriggers := len(c.routes) > 0
	c.sup.SetDaemonMode(opts.Deploy.Mode == "watch" || c.sched != nil || hasTriggers)

	return c, nil
}

// Scheduler returns the loaded scheduler, or nil when no jobs.yaml is present.
func (c *Coordinator) Scheduler() *scheduler.Scheduler { return c.sched }

// Routes returns the trigger routes installed in the supervisor.
func (c *Coordinator) Routes() []supervisor.TriggerRoute { return c.routes }

// SyncWarning returns the non-fatal error from schedule synchronization, if any.
func (c *Coordinator) SyncWarning() error { return c.syncWarning }

// Snapshot returns the active watch, trigger, and cron automations for this
// deployment. Database read errors intentionally produce no cron entries,
// matching the historical best-effort startup-summary behavior.
func (c *Coordinator) Snapshot() []Automation {
	return ComputeAutomations(c.deploy, c.routes, c.store, c.deploy.ID)
}

// Start launches the scheduler loop (when present) and the supervisor. It does
// not block. The context handed to both is a cancellable child of ctx so that
// Stop alone halts the whole deployment.
func (c *Coordinator) Start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	if c.sched != nil {
		go c.sched.Run(ctx)
	}
	c.sup.Launch(ctx)
}

// Run starts the deployment and blocks until the supervisor is done.
func (c *Coordinator) Run(ctx context.Context) {
	c.Start(ctx)
	<-c.sup.Done()
}

// Stop halts the deployment: cancels the scheduler and supervisor contexts and
// drains the supervisor's in-flight work. Safe before Start and more than once.
func (c *Coordinator) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.sup.Stop()
}

// TriggerRoutesFromConfig computes the label and milestone routes installed in
// the supervisor from a validated jobs.yaml config.
func TriggerRoutesFromConfig(cfg *scheduler.Config) []supervisor.TriggerRoute {
	var routes []supervisor.TriggerRoute
	if cfg == nil {
		return routes
	}

	for name, def := range cfg.Jobs {
		switch {
		case len(def.TriggerLabels()) > 0:
			routes = append(routes, supervisor.TriggerRoute{
				Name:     name,
				Labels:   def.TriggerLabels(),
				Agent:    def.Agent,
				Runtime:  def.Runtime,
				Model:    def.Model,
				Budget:   def.Budget,
				MaxTurns: def.MaxTurns,
			})
		case def.TriggerMilestone() != "":
			routes = append(routes, supervisor.TriggerRoute{
				Name:      name,
				Milestone: def.TriggerMilestone(),
				Agent:     def.Agent,
				Runtime:   def.Runtime,
				Model:     def.Model,
				Budget:    def.Budget,
				MaxTurns:  def.MaxTurns,
			})
		}
	}
	return routes
}

// ComputeAutomations snapshots the active watch, trigger, and cron automations.
// Database read errors intentionally produce no cron entries, matching the
// historical best-effort startup-summary behavior.
func ComputeAutomations(deploy *db.Deployment, routes []supervisor.TriggerRoute, store *db.Store, deployID string) []Automation {
	automations := make([]Automation, 0, len(routes)+1)
	if deploy.WatchFilter.Valid && deploy.WatchFilter.String != "" {
		automations = append(automations, Automation{
			Kind:       AutomationWatch,
			Expression: deploy.WatchFilter.String,
			Agent:      "autopilot",
		})
	}
	for _, route := range routes {
		automations = append(automations, Automation{
			Kind:       AutomationTrigger,
			Expression: route.FilterString(),
			Labels:     append([]string(nil), route.Labels...),
			Agent:      route.Agent,
			Runtime:    route.Runtime,
		})
	}

	schedules, _ := store.GetEnabledSchedules(deployID)
	for _, schedule := range schedules {
		if !schedule.CronExpr.Valid {
			continue
		}
		automations = append(automations, Automation{
			Kind:       AutomationCron,
			Name:       schedule.Name,
			Expression: schedule.CronExpr.String,
			Agent:      schedule.Agent,
			Runtime:    schedule.Runtime.String,
			NextRunAt:  schedule.NextRunAt.Time,
			HasNextRun: schedule.NextRunAt.Valid,
		})
	}
	return automations
}
