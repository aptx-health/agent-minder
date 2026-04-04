package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Config represents the top-level jobs.yaml file.
type Config struct {
	Jobs map[string]JobDef `yaml:"jobs"`
}

// JobDef defines a scheduled or triggered job.
type JobDef struct {
	// One of schedule or trigger is required.
	Schedule string `yaml:"schedule"` // cron expression (5-field)
	Trigger  string `yaml:"trigger"`  // event trigger: "label:<name>" or "milestone:<name>"

	Agent       string  `yaml:"agent"`       // agent def name (required)
	Description string  `yaml:"description"` // human-readable description
	Budget      float64 `yaml:"budget"`      // per-run budget override (0 = use deployment default)
	MaxTurns    int     `yaml:"max_turns"`   // per-run turn limit override (0 = use default)
}

// IsScheduled returns true if this job is cron-scheduled.
func (j *JobDef) IsScheduled() bool {
	return j.Schedule != ""
}

// IsTrigger returns true if this job is event-triggered.
func (j *JobDef) IsTrigger() bool {
	return j.Trigger != ""
}

// TriggerLabel returns the label name if this is a label trigger, or empty string.
func (j *JobDef) TriggerLabel() string {
	if !j.IsTrigger() {
		return ""
	}
	parts := strings.SplitN(j.Trigger, ":", 2)
	if len(parts) == 2 && parts[0] == "label" {
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

	return &cfg, nil
}

func validateJobDef(name string, job *JobDef) error {
	if job.Agent == "" {
		return fmt.Errorf("job %q: agent is required", name)
	}

	if job.Schedule == "" && job.Trigger == "" {
		return fmt.Errorf("job %q: schedule or trigger is required", name)
	}

	if job.Schedule != "" && job.Trigger != "" {
		return fmt.Errorf("job %q: cannot have both schedule and trigger", name)
	}

	// Validate cron expression.
	if job.Schedule != "" {
		if _, err := ParseCron(job.Schedule); err != nil {
			return fmt.Errorf("job %q: %w", name, err)
		}
	}

	// Validate trigger format.
	if job.Trigger != "" {
		parts := strings.SplitN(job.Trigger, ":", 2)
		if len(parts) != 2 || parts[1] == "" {
			return fmt.Errorf("job %q: invalid trigger %q (expected label:<name> or milestone:<name>)", name, job.Trigger)
		}
		typ := strings.ToLower(parts[0])
		if typ != "label" && typ != "milestone" {
			return fmt.Errorf("job %q: unsupported trigger type %q", name, typ)
		}
	}

	if job.Budget < 0 {
		return fmt.Errorf("job %q: budget cannot be negative", name)
	}

	if job.MaxTurns < 0 {
		return fmt.Errorf("job %q: max_turns cannot be negative", name)
	}

	return nil
}
