package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultRefreshInterval is how often the minder polls for changes.
const DefaultRefreshInterval = 5 * time.Minute

// DefaultMessageTTL is how far back to look for messages.
const DefaultMessageTTL = 48 * time.Hour

// BaseDir returns the root config directory (~/.agent-minder).
func BaseDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return filepath.Join(home, ".agent-minder"), nil
}

// ProjectDir returns the config directory for a specific project.
func ProjectDir(project string) (string, error) {
	base, err := BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, project), nil
}

// Worktree represents a git worktree within a repo.
type Worktree struct {
	Path   string `yaml:"path"`
	Branch string `yaml:"branch"`
}

// Repo represents a monitored repository.
type Repo struct {
	Path      string     `yaml:"path"`
	ShortName string     `yaml:"short_name"`
	Worktrees []Worktree `yaml:"worktrees,omitempty"`
}

// Project is the top-level config for a minder project.
type Project struct {
	Name                 string        `yaml:"project"`
	RefreshInterval      time.Duration `yaml:"refresh_interval"`
	MessageTTL           time.Duration `yaml:"message_ttl"`
	AutoEnrollWorktrees  bool          `yaml:"auto_enroll_worktrees"`
	Repos                []Repo        `yaml:"repos"`
	Topics               []string      `yaml:"topics"`
	MinderIdentity       string        `yaml:"minder_identity"`
	ClaudeSessionID      string        `yaml:"claude_session_id,omitempty"`
}

// durationYAML handles marshaling/unmarshaling time.Duration as a human-readable string.
type durationYAML time.Duration

func (d durationYAML) MarshalYAML() (interface{}, error) {
	return time.Duration(d).String(), nil
}

func (d *durationYAML) UnmarshalYAML(value *yaml.Node) error {
	dur, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("parsing duration %q: %w", value.Value, err)
	}
	*d = durationYAML(dur)
	return nil
}

// projectYAML is the on-disk representation with string durations.
type projectYAML struct {
	Name                string   `yaml:"project"`
	RefreshInterval     string   `yaml:"refresh_interval"`
	MessageTTL          string   `yaml:"message_ttl"`
	AutoEnrollWorktrees bool     `yaml:"auto_enroll_worktrees"`
	Repos               []Repo   `yaml:"repos"`
	Topics              []string `yaml:"topics"`
	MinderIdentity      string   `yaml:"minder_identity"`
	ClaudeSessionID     string   `yaml:"claude_session_id,omitempty"`
}

// Load reads a project config from disk.
func Load(project string) (*Project, error) {
	dir, err := ProjectDir(project)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "config.yaml")

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var raw projectYAML
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	p := &Project{
		Name:                raw.Name,
		AutoEnrollWorktrees: raw.AutoEnrollWorktrees,
		Repos:               raw.Repos,
		Topics:              raw.Topics,
		MinderIdentity:      raw.MinderIdentity,
		ClaudeSessionID:     raw.ClaudeSessionID,
	}

	if raw.RefreshInterval != "" {
		p.RefreshInterval, err = time.ParseDuration(raw.RefreshInterval)
		if err != nil {
			return nil, fmt.Errorf("parsing refresh_interval: %w", err)
		}
	} else {
		p.RefreshInterval = DefaultRefreshInterval
	}

	if raw.MessageTTL != "" {
		p.MessageTTL, err = time.ParseDuration(raw.MessageTTL)
		if err != nil {
			return nil, fmt.Errorf("parsing message_ttl: %w", err)
		}
	} else {
		p.MessageTTL = DefaultMessageTTL
	}

	return p, nil
}

// Save writes a project config to disk, creating directories as needed.
func Save(p *Project) error {
	dir, err := ProjectDir(p.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating config directory %s: %w", dir, err)
	}

	raw := projectYAML{
		Name:                p.Name,
		RefreshInterval:     p.RefreshInterval.String(),
		MessageTTL:          p.MessageTTL.String(),
		AutoEnrollWorktrees: p.AutoEnrollWorktrees,
		Repos:               p.Repos,
		Topics:              p.Topics,
		MinderIdentity:      p.MinderIdentity,
		ClaudeSessionID:     p.ClaudeSessionID,
	}

	data, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing config %s: %w", path, err)
	}

	return nil
}

// NewProject creates a Project with sensible defaults.
func NewProject(name string) *Project {
	return &Project{
		Name:                name,
		RefreshInterval:     DefaultRefreshInterval,
		MessageTTL:          DefaultMessageTTL,
		AutoEnrollWorktrees: true,
		MinderIdentity:      name + "/minder",
	}
}
