package scheduler

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aptx-health/agent-minder/internal/runtime"
	"go.yaml.in/yaml/v3"
)

// Config represents the top-level jobs.yaml file.
type Config struct {
	Jobs  map[string]JobDef `yaml:"jobs"`
	Sinks []SinkDef         `yaml:"sinks"`
}

// SinkDef declares an event sink: a subscription to a set of event-type
// patterns delivered to exactly one destination (a webhook or a local
// command). See internal/eventsink for the pattern matching and delivery
// machinery; this type is just the jobs.yaml wire shape.
type SinkDef struct {
	Events  []string `yaml:"events"`  // glob patterns matched against the event type, e.g. "completed", "review*"
	Webhook string   `yaml:"webhook"` // POST target; mutually exclusive with Exec
	Exec    string   `yaml:"exec"`    // local command; event JSON delivered on stdin; mutually exclusive with Webhook
}

// JobDef defines a scheduled, triggered, or one-shot job.
type JobDef struct {
	// Exactly one of Schedule, Trigger, At, or In is required.
	Schedule string `yaml:"schedule"` // cron expression (5-field)
	Trigger  string `yaml:"trigger"`  // event trigger: "label:<name>" or "milestone:<name>"
	At       string `yaml:"at"`       // one-shot: RFC3339 timestamp, e.g. "2026-08-09T15:00:00Z"
	In       string `yaml:"in"`       // one-shot: duration from load time, e.g. "30s", "5m", "2h", "1d"

	Agent       string  `yaml:"agent"`       // agent def name (required)
	Runtime     string  `yaml:"runtime"`     // optional doer runtime override
	Model       string  `yaml:"model"`       // optional runtime-native model override
	Description string  `yaml:"description"` // human-readable description
	Budget      float64 `yaml:"budget"`      // per-run budget override (0 = use deployment default)
	MaxTurns    int     `yaml:"max_turns"`   // per-run turn limit override (0 = use default)

	// resolvedAt holds the absolute fire time for a one-shot (At or In) job,
	// set once during validation. In particular, an `in:` duration is resolved
	// relative to load time here — reloading jobs.yaml (e.g. across a daemon
	// restart) re-resolves it to a fresh future time. The scheduler guards
	// against refiring an already-fired one-shot by skipping the resync when
	// its persisted job_schedules row shows a prior run.
	resolvedAt time.Time
}

// IsScheduled returns true if this job is cron-scheduled.
func (j *JobDef) IsScheduled() bool {
	return j.Schedule != ""
}

// IsTrigger returns true if this job is event-triggered.
func (j *JobDef) IsTrigger() bool {
	return j.Trigger != ""
}

// IsOneShot returns true if this job fires once at a resolved absolute time
// (declared via `at:` or `in:`). Only valid after validation (ParseConfig /
// LoadConfig) has run.
func (j *JobDef) IsOneShot() bool {
	return !j.resolvedAt.IsZero()
}

// ResolvedAt returns the absolute fire time for a one-shot job, or the zero
// time if this is not a one-shot job or validation has not run yet.
func (j *JobDef) ResolvedAt() time.Time {
	return j.resolvedAt
}

// TriggerLabels returns the label names if this is a label trigger (comma-separated = AND logic).
// Returns nil for non-label triggers.
func (j *JobDef) TriggerLabels() []string {
	if !j.IsTrigger() {
		return nil
	}
	parts := strings.SplitN(j.Trigger, ":", 2)
	if len(parts) == 2 && parts[0] == "label" {
		return strings.Split(parts[1], ",")
	}
	return nil
}

// TriggerMilestone returns the milestone name if this is a milestone trigger.
// Returns "" for non-milestone triggers.
func (j *JobDef) TriggerMilestone() string {
	if !j.IsTrigger() {
		return ""
	}
	parts := strings.SplitN(j.Trigger, ":", 2)
	if len(parts) == 2 && parts[0] == "milestone" {
		return parts[1]
	}
	return ""
}

// ParsedSchedule returns the parsed cron expression, or nil if not scheduled.
func (j *JobDef) ParsedSchedule() (*CronExpr, error) {
	if j.Schedule == "" {
		return nil, nil
	}
	return ParseCron(j.Schedule)
}

// ConfigPath returns the path to jobs.yaml for a given repo dir.
func ConfigPath(repoDir string) string {
	return filepath.Join(repoDir, ".agent-minder", "jobs.yaml")
}

// LoadConfig reads and validates a jobs.yaml file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read jobs.yaml: %w", err)
	}
	return ParseConfig(data)
}

// ParseConfig parses and validates jobs.yaml content.
func ParseConfig(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse jobs.yaml: %w", err)
	}

	if len(cfg.Jobs) == 0 {
		return nil, fmt.Errorf("jobs.yaml: no jobs defined")
	}

	for name, job := range cfg.Jobs {
		if err := validateJobDef(name, &job); err != nil {
			return nil, err
		}
		// Write back normalized values.
		cfg.Jobs[name] = job
	}

	for i := range cfg.Sinks {
		if err := validateSinkDef(i, &cfg.Sinks[i]); err != nil {
			return nil, err
		}
	}

	return &cfg, nil
}

func validateJobDef(name string, job *JobDef) error {
	if job.Agent == "" {
		return fmt.Errorf("job %q: agent is required", name)
	}
	if job.Runtime != "" {
		if err := runtime.Validate(job.Runtime); err != nil {
			return fmt.Errorf("job %q: %w", name, err)
		}
	}
	job.Model = runtime.NormalizeModelName(job.Model)
	if err := runtime.ValidateModelName(job.Model); err != nil {
		return fmt.Errorf("job %q: %w", name, err)
	}

	kindsSet := 0
	if job.Schedule != "" {
		kindsSet++
	}
	if job.Trigger != "" {
		kindsSet++
	}
	if job.At != "" {
		kindsSet++
	}
	if job.In != "" {
		kindsSet++
	}
	if kindsSet == 0 {
		return fmt.Errorf("job %q: exactly one of schedule, trigger, at, or in is required", name)
	}
	if kindsSet > 1 {
		return fmt.Errorf("job %q: only one of schedule, trigger, at, or in may be set", name)
	}

	switch {
	case job.Schedule != "":
		// Validate cron expression.
		if _, err := ParseCron(job.Schedule); err != nil {
			return fmt.Errorf("job %q: %w", name, err)
		}

	case job.Trigger != "":
		// Validate trigger format.
		parts := strings.SplitN(job.Trigger, ":", 2)
		if len(parts) != 2 || parts[1] == "" {
			return fmt.Errorf("job %q: invalid trigger %q (expected label:<name> or milestone:<name>)", name, job.Trigger)
		}
		typ := strings.ToLower(parts[0])
		if typ != "label" && typ != "milestone" {
			return fmt.Errorf("job %q: unsupported trigger type %q", name, typ)
		}

	case job.At != "":
		at, err := parseOneShotTimestamp(job.At)
		if err != nil {
			return fmt.Errorf("job %q: %w", name, err)
		}
		if !at.After(time.Now().UTC()) {
			return fmt.Errorf("job %q: at %q is in the past", name, job.At)
		}
		job.resolvedAt = at

	case job.In != "":
		d, err := parseOneShotDuration(job.In)
		if err != nil {
			return fmt.Errorf("job %q: %w", name, err)
		}
		if d <= 0 {
			return fmt.Errorf("job %q: in %q must be a positive duration", name, job.In)
		}
		job.resolvedAt = time.Now().UTC().Add(d)
	}

	if job.Budget < 0 {
		return fmt.Errorf("job %q: budget cannot be negative", name)
	}

	if job.MaxTurns < 0 {
		return fmt.Errorf("job %q: max_turns cannot be negative", name)
	}

	return nil
}

func validateSinkDef(index int, sink *SinkDef) error {
	label := fmt.Sprintf("sink[%d]", index)
	if len(sink.Events) == 0 {
		return fmt.Errorf("%s: events is required", label)
	}
	for _, pattern := range sink.Events {
		if pattern == "" {
			return fmt.Errorf("%s: event pattern cannot be empty", label)
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("%s: invalid event pattern %q: %w", label, pattern, err)
		}
	}

	hasWebhook := sink.Webhook != ""
	hasExec := sink.Exec != ""
	switch {
	case hasWebhook && hasExec:
		return fmt.Errorf("%s: cannot have both webhook and exec", label)
	case !hasWebhook && !hasExec:
		return fmt.Errorf("%s: one of webhook or exec is required", label)
	}

	return nil
}

// parseOneShotTimestamp parses an `at:` value as an RFC3339 timestamp.
func parseOneShotTimestamp(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid at timestamp %q (expected RFC3339, e.g. 2026-08-09T15:00:00Z): %w", s, err)
	}
	return t.UTC(), nil
}

// parseOneShotDuration parses an `in:` value: standard Go duration syntax
// (30s, 5m, 2h, combinations like 1h30m) plus a "d" (day) suffix that
// time.ParseDuration doesn't support.
func parseOneShotDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		numPart := strings.TrimSuffix(s, "d")
		n, err := strconv.Atoi(numPart)
		if err != nil {
			return 0, fmt.Errorf("invalid in duration %q (expected e.g. 30s, 5m, 2h, 1d)", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid in duration %q (expected e.g. 30s, 5m, 2h, 1d): %w", s, err)
	}
	return d, nil
}
