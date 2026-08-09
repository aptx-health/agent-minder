// Package coordinator owns the per-deployment assembly shared by the foreground
// and daemon entry points: it wires a supervisor, an optional jobs.yaml
// scheduler, and the trigger routes derived from that config, then exposes a
// small lifecycle (Start/Run/Snapshot/Stop). Extracting this spine keeps the
// two entry points from drifting apart as they independently re-wired the same
// pieces.
package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/eventsink"
	"github.com/aptx-health/agent-minder/internal/runtime"
	"github.com/aptx-health/agent-minder/internal/scheduler"
	"github.com/aptx-health/agent-minder/internal/supervisor"
	"github.com/google/uuid"
)

// AutomationKind classifies an active subscription in a startup snapshot.
type AutomationKind string

const (
	AutomationWatch   AutomationKind = "watch"
	AutomationTrigger AutomationKind = "trigger"
	AutomationCron    AutomationKind = "cron"
	AutomationOneShot AutomationKind = "one-shot"
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
	// ExecutionKind distinguishes agent automations from script automations
	// (db.JobKindAgent / db.JobKindScript).
	ExecutionKind string
	// Runtime, Model, MaxTurns, and MaxBudget are the configured automation
	// overrides. Keep Runtime's legacy meaning so foreground output does not
	// start printing deployment defaults as explicit overrides.
	Runtime   string
	Model     string
	MaxTurns  int
	MaxBudget float64
	// Effective values include deployment fallbacks and are used by v1 DTOs.
	EffectiveRuntime   string
	EffectiveModel     string
	EffectiveMaxTurns  int
	EffectiveMaxBudget float64
	NextRunAt          time.Time
	HasNextRun         bool

	// Enabled reflects the automation's current activation state. Cron
	// automations are only ever loaded when enabled (GetEnabledSchedules
	// filters), and trigger/watch automations are enabled-by-construction
	// today, so this is always true until a durable table adds per-automation
	// disable support.
	Enabled bool

	// LastActivation is derived from job provenance (jobs.source_type,
	// jobs.source_name), not from a durable automations table: the most
	// recent job created by this automation, if any.
	LastActivationAt    time.Time
	HasLastActivation   bool
	LastActivationJobID int64
}

// ConfigRevision identifies the jobs.yaml a Coordinator loaded: its path, a
// content hash for change detection, when it was loaded, and any validation
// error encountered. Populated even when jobs.yaml is absent or invalid so
// callers can distinguish "no automation" from "automation config is broken."
type ConfigRevision struct {
	Path        string
	SHA256      string
	LoadedAt    time.Time
	HasConfig   bool
	ValidateErr error
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
	// incarnation identifies live, process-lifetime state. It is stable for
	// this Coordinator and intentionally changes after reconstruction.
	incarnation string

	sup    *supervisor.Supervisor
	sched  *scheduler.Scheduler // nil when no jobs.yaml is present
	cfg    *scheduler.Config    // nil when no jobs.yaml is present
	routes []supervisor.TriggerRoute
	sinks  *eventsink.Manager // nil when jobs.yaml declares no sinks

	cancel      context.CancelFunc // cancels the context Start derived; nil before Start
	syncWarning error
	configRev   ConfigRevision
}

// New assembles the supervisor, optional scheduler, and trigger routes for a
// deployment and applies the derived daemon-mode flag. It never prints; any
// non-fatal schedule-sync problem is surfaced via SyncWarning so the caller
// controls stdout. A returned error is fatal to the deployment.
func New(opts Options) (*Coordinator, error) {
	c := &Coordinator{
		store:       opts.Store,
		deploy:      opts.Deploy,
		incarnation: uuid.NewString(),
	}

	c.sup = supervisor.New(opts.Store, opts.Deploy, opts.Deploy.RepoDir, opts.Deploy.Owner, opts.Deploy.Repo, opts.GHToken)
	if opts.Runtime != nil {
		c.sup.SetRuntime(opts.Runtime)
	}

	// Load jobs.yaml scheduler and trigger routes only for deployments whose
	// persisted activation policy calls for automation. Explicit deployments
	// (only the issues/proactive job the operator named) must not
	// auto-subscribe to jobs.yaml triggers or cron schedules.
	if opts.Deploy.ActivationPolicy != db.ActivationExplicit {
		rev, cfg, err := LoadConfigRevision(opts.Deploy.RepoDir)
		c.configRev = rev
		if err != nil {
			if rev.HasConfig {
				// jobs.yaml exists but is invalid, and automation was
				// requested (implicitly or explicitly) — surface the error
				// rather than silently running without automation.
				return nil, fmt.Errorf("load jobs.yaml: %w", err)
			}
			// No jobs.yaml present; nothing to load.
		} else if cfg != nil {
			c.cfg = cfg
			c.sched = scheduler.New(opts.Store, opts.Deploy.ID, opts.Deploy.Owner, opts.Deploy.Repo, cfg)
			if err := c.sched.SyncSchedules(); err != nil {
				c.syncWarning = err
			}
			c.routes = TriggerRoutesFromConfig(cfg)
			if len(c.routes) > 0 {
				c.sup.SetTriggerRoutes(c.routes)
			}
			if len(cfg.Sinks) > 0 {
				sinks, err := eventsink.NewManager(c.sup.EventBus(), SinkConfigsFromConfig(cfg), eventsink.Options{})
				if err != nil {
					// jobs.yaml passed scheduler-side validation but the
					// sink manager rejects it; treat like the invalid-config
					// case above rather than silently running without sinks.
					return nil, fmt.Errorf("load jobs.yaml sinks: %w", err)
				}
				c.sinks = sinks
			}
		}
	}

	hasTriggers := len(c.routes) > 0
	c.sup.SetDaemonMode(opts.Deploy.Mode == "watch" || c.sched != nil || hasTriggers)

	return c, nil
}

// LoadConfigRevision reads and parses the jobs.yaml for repoDir, returning the
// computed ConfigRevision alongside the parsed config (nil if absent or
// invalid) and any parse/validation error. Split out from New so the
// config-identity computation is directly testable without a full
// Coordinator/deployment fixture.
func LoadConfigRevision(repoDir string) (ConfigRevision, *scheduler.Config, error) {
	cfgPath := scheduler.ConfigPath(repoDir)
	data, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		// No jobs.yaml present; not an error, just no config to report.
		return ConfigRevision{Path: cfgPath}, nil, nil
	}
	rev := ConfigRevision{
		Path:      cfgPath,
		SHA256:    fmt.Sprintf("%x", sha256.Sum256(data)),
		LoadedAt:  time.Now(),
		HasConfig: true,
	}
	cfg, err := scheduler.ParseConfig(data)
	if err != nil {
		rev.ValidateErr = err
		return rev, nil, err
	}
	return rev, cfg, nil
}

// ConfigRevision returns the identity of the jobs.yaml this Coordinator
// loaded: path, content hash, load time, and any validation error.
func (c *Coordinator) ConfigRevision() ConfigRevision { return c.configRev }

// WorkerIncarnation identifies live state owned by this Coordinator instance.
func (c *Coordinator) WorkerIncarnation() string { return c.incarnation }

// Scheduler returns the loaded scheduler, or nil when no jobs.yaml is present.
func (c *Coordinator) Scheduler() *scheduler.Scheduler { return c.sched }

// Routes returns the trigger routes installed in the supervisor.
func (c *Coordinator) Routes() []supervisor.TriggerRoute { return c.routes }

// SyncWarning returns the non-fatal error from schedule synchronization, if any.
func (c *Coordinator) SyncWarning() error { return c.syncWarning }

// ActivationPolicy returns the persisted activation policy for this
// deployment (explicit, automated, or hybrid). Exposed so deployment-detail
// surfaces can distinguish an ad-hoc explicit batch from a long-lived
// automation worker.
func (c *Coordinator) ActivationPolicy() db.ActivationPolicy { return c.deploy.ActivationPolicy }

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
	if c.sinks != nil {
		if err := c.sinks.Start(ctx); err != nil {
			// Best-effort: a sink subscription failure never blocks the
			// deployment, only the (already best-effort) notifications.
			c.syncWarning = errors.Join(c.syncWarning, fmt.Errorf("start event sinks: %w", err))
		}
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
	if c.sinks != nil {
		c.sinks.Stop()
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
		envJSON := ""
		if len(def.Env) > 0 {
			data, err := json.Marshal(def.Env)
			if err == nil {
				envJSON = string(data)
			}
		}
		switch {
		case len(def.TriggerLabels()) > 0:
			routes = append(routes, supervisor.TriggerRoute{
				Name:     name,
				Labels:   def.TriggerLabels(),
				Kind:     def.EffectiveKind(),
				Agent:    routeAgent(def),
				Runtime:  def.Runtime,
				Model:    def.Model,
				Budget:   def.Budget,
				MaxTurns: def.MaxTurns,
				Command:  def.Command,
				Timeout:  def.Timeout,
				EnvJSON:  envJSON,
				WorkDir:  def.ScriptWorkDir(),
			})
		case def.TriggerMilestone() != "":
			routes = append(routes, supervisor.TriggerRoute{
				Name:      name,
				Milestone: def.TriggerMilestone(),
				Kind:      def.EffectiveKind(),
				Agent:     routeAgent(def),
				Runtime:   def.Runtime,
				Model:     def.Model,
				Budget:    def.Budget,
				MaxTurns:  def.MaxTurns,
				Command:   def.Command,
				Timeout:   def.Timeout,
				EnvJSON:   envJSON,
				WorkDir:   def.ScriptWorkDir(),
			})
		}
	}
	return routes
}

// SinkConfigsFromConfig converts the jobs.yaml sink declarations into the
// shape eventsink.Manager consumes.
func SinkConfigsFromConfig(cfg *scheduler.Config) []eventsink.Config {
	if cfg == nil {
		return nil
	}
	configs := make([]eventsink.Config, 0, len(cfg.Sinks))
	for _, sink := range cfg.Sinks {
		configs = append(configs, eventsink.Config{
			Events:  sink.Events,
			Webhook: sink.Webhook,
			Exec:    sink.Exec,
		})
	}
	return configs
}

// ComputeAutomations snapshots the active watch, trigger, and cron automations.
// Database read errors intentionally produce no cron entries, matching the
// historical best-effort startup-summary behavior.
func ComputeAutomations(deploy *db.Deployment, routes []supervisor.TriggerRoute, store *db.Store, deployID string) []Automation {
	automations := make([]Automation, 0, len(routes)+1)
	if deploy.WatchFilter.Valid && deploy.WatchFilter.String != "" {
		automations = append(automations, Automation{
			Kind:               AutomationWatch,
			Expression:         deploy.WatchFilter.String,
			Agent:              "autopilot",
			EffectiveRuntime:   deploy.Runtime,
			EffectiveMaxTurns:  deploy.MaxTurns,
			EffectiveMaxBudget: deploy.MaxBudgetUSD,
			Enabled:            true,
		})
	}
	for _, route := range routes {
		a := Automation{
			Kind:               AutomationTrigger,
			Name:               route.Name,
			Expression:         route.FilterString(),
			Labels:             append([]string(nil), route.Labels...),
			Agent:              route.Agent,
			ExecutionKind:      route.Kind,
			Runtime:            route.Runtime,
			Model:              route.Model,
			MaxTurns:           route.MaxTurns,
			MaxBudget:          route.Budget,
			EffectiveRuntime:   effectiveString(route.Runtime, deploy.Runtime),
			EffectiveModel:     route.Model,
			EffectiveMaxTurns:  effectiveInt(route.MaxTurns, deploy.MaxTurns),
			EffectiveMaxBudget: effectiveFloat(route.Budget, deploy.MaxBudgetUSD),
			Enabled:            true,
		}
		applyLastActivation(&a, store, deployID, "trigger", route.Name)
		automations = append(automations, a)
	}

	schedules, _ := store.GetEnabledSchedules(deployID)
	for _, schedule := range schedules {
		switch {
		case schedule.CronExpr.Valid:
			a := Automation{
				Kind:               AutomationCron,
				Name:               schedule.Name,
				Expression:         schedule.CronExpr.String,
				Agent:              schedule.Agent,
				ExecutionKind:      schedule.Kind,
				Runtime:            schedule.Runtime.String,
				Model:              schedule.Model.String,
				MaxTurns:           int(schedule.MaxTurns.Int64),
				MaxBudget:          schedule.Budget.Float64,
				EffectiveRuntime:   effectiveString(schedule.Runtime.String, deploy.Runtime),
				EffectiveModel:     schedule.Model.String,
				EffectiveMaxTurns:  effectiveInt(int(schedule.MaxTurns.Int64), deploy.MaxTurns),
				EffectiveMaxBudget: effectiveFloat(schedule.Budget.Float64, deploy.MaxBudgetUSD),
				NextRunAt:          schedule.NextRunAt.Time,
				HasNextRun:         schedule.NextRunAt.Valid,
				Enabled:            true,
			}
			applyLastActivation(&a, store, deployID, "cron", schedule.Name)
			automations = append(automations, a)
		case schedule.IsOneShot():
			a := Automation{
				Kind:               AutomationOneShot,
				Name:               schedule.Name,
				Expression:         schedule.AtTime.Time.Format(time.RFC3339),
				Agent:              schedule.Agent,
				ExecutionKind:      schedule.Kind,
				Runtime:            schedule.Runtime.String,
				Model:              schedule.Model.String,
				MaxTurns:           int(schedule.MaxTurns.Int64),
				MaxBudget:          schedule.Budget.Float64,
				EffectiveRuntime:   effectiveString(schedule.Runtime.String, deploy.Runtime),
				EffectiveModel:     schedule.Model.String,
				EffectiveMaxTurns:  effectiveInt(int(schedule.MaxTurns.Int64), deploy.MaxTurns),
				EffectiveMaxBudget: effectiveFloat(schedule.Budget.Float64, deploy.MaxBudgetUSD),
				NextRunAt:          schedule.AtTime.Time,
				HasNextRun:         true,
				Enabled:            true,
			}
			applyLastActivation(&a, store, deployID, "at", schedule.Name)
			automations = append(automations, a)
		}
	}
	return automations
}

// computeAutomationsFromSnapshot is the transaction-safe counterpart to
// ComputeAutomations. It performs no database reads: schedules, provenance
// jobs, and the deployment were all obtained by Store.SnapshotControl.
func computeAutomationsFromSnapshot(deploy *db.Deployment, routes []supervisor.TriggerRoute, schedules []*db.JobSchedule, jobs []*db.Job) []Automation {
	automations := make([]Automation, 0, len(routes)+len(schedules)+1)
	if deploy.WatchFilter.Valid && deploy.WatchFilter.String != "" {
		automations = append(automations, Automation{
			Kind: AutomationWatch, Expression: deploy.WatchFilter.String,
			Agent: "autopilot", EffectiveRuntime: deploy.Runtime, EffectiveMaxTurns: deploy.MaxTurns,
			EffectiveMaxBudget: deploy.MaxBudgetUSD, Enabled: true,
		})
	}
	for _, route := range routes {
		a := Automation{
			Kind: AutomationTrigger, Name: route.Name, Expression: route.FilterString(),
			Labels: append([]string(nil), route.Labels...), Agent: route.Agent,
			Runtime: route.Runtime, Model: route.Model, MaxTurns: route.MaxTurns, MaxBudget: route.Budget,
			EffectiveRuntime: effectiveString(route.Runtime, deploy.Runtime), EffectiveModel: route.Model,
			EffectiveMaxTurns:  effectiveInt(route.MaxTurns, deploy.MaxTurns),
			EffectiveMaxBudget: effectiveFloat(route.Budget, deploy.MaxBudgetUSD), Enabled: true,
		}
		applyLastActivationFromJobs(&a, jobs, "trigger", route.Name)
		automations = append(automations, a)
	}
	for _, schedule := range schedules {
		if !schedule.Enabled {
			continue
		}
		switch {
		case schedule.CronExpr.Valid:
			a := Automation{
				Kind: AutomationCron, Name: schedule.Name, Expression: schedule.CronExpr.String,
				Agent: schedule.Agent, Runtime: schedule.Runtime.String, Model: schedule.Model.String,
				MaxTurns: int(schedule.MaxTurns.Int64), MaxBudget: schedule.Budget.Float64,
				EffectiveRuntime: effectiveString(schedule.Runtime.String, deploy.Runtime), EffectiveModel: schedule.Model.String,
				EffectiveMaxTurns:  effectiveInt(int(schedule.MaxTurns.Int64), deploy.MaxTurns),
				EffectiveMaxBudget: effectiveFloat(schedule.Budget.Float64, deploy.MaxBudgetUSD),
				NextRunAt:          schedule.NextRunAt.Time, HasNextRun: schedule.NextRunAt.Valid, Enabled: true,
			}
			applyLastActivationFromJobs(&a, jobs, "cron", schedule.Name)
			automations = append(automations, a)
		case schedule.IsOneShot():
			a := Automation{
				Kind: AutomationOneShot, Name: schedule.Name, Expression: schedule.AtTime.Time.Format(time.RFC3339),
				Agent: schedule.Agent, Runtime: schedule.Runtime.String, Model: schedule.Model.String,
				MaxTurns: int(schedule.MaxTurns.Int64), MaxBudget: schedule.Budget.Float64,
				EffectiveRuntime: effectiveString(schedule.Runtime.String, deploy.Runtime), EffectiveModel: schedule.Model.String,
				EffectiveMaxTurns:  effectiveInt(int(schedule.MaxTurns.Int64), deploy.MaxTurns),
				EffectiveMaxBudget: effectiveFloat(schedule.Budget.Float64, deploy.MaxBudgetUSD),
				NextRunAt:          schedule.AtTime.Time, HasNextRun: true, Enabled: true,
			}
			applyLastActivationFromJobs(&a, jobs, "at", schedule.Name)
			automations = append(automations, a)
		}
	}
	return automations
}

func effectiveString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func effectiveInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func effectiveFloat(value, fallback float64) float64 {
	if value > 0 {
		return value
	}
	return fallback
}

func applyLastActivationFromJobs(a *Automation, jobs []*db.Job, sourceType, sourceName string) {
	var latest *db.Job
	for _, job := range jobs {
		if !job.SourceType.Valid || job.SourceType.String != sourceType ||
			!job.SourceName.Valid || job.SourceName.String != sourceName {
			continue
		}
		if latest == nil || job.QueuedAt.Time.After(latest.QueuedAt.Time) ||
			(job.QueuedAt.Time.Equal(latest.QueuedAt.Time) && job.ID > latest.ID) {
			latest = job
		}
	}
	if latest == nil {
		return
	}
	a.LastActivationJobID = latest.ID
	a.HasLastActivation = true
	if latest.QueuedAt.Valid {
		a.LastActivationAt = latest.QueuedAt.Time
	}
}

// applyLastActivation looks up the most recent job created by this automation
// (by source_type/source_name provenance) and, if found, populates the
// automation's last-activation fields. Store errors or a missing job leave
// the automation's last-activation fields at their zero value, matching the
// package's best-effort snapshot behavior.
func applyLastActivation(a *Automation, store *db.Store, deployID, sourceType, sourceName string) {
	if store == nil || sourceName == "" {
		return
	}
	job, err := store.GetLastJobBySource(deployID, sourceType, sourceName)
	if err != nil || job == nil {
		return
	}
	a.LastActivationJobID = job.ID
	a.HasLastActivation = true
	if job.QueuedAt.Valid {
		a.LastActivationAt = job.QueuedAt.Time
	}
}

func routeAgent(def scheduler.JobDef) string {
	if def.EffectiveKind() == scheduler.JobKindScript {
		return db.JobKindScript
	}
	return def.Agent
}
