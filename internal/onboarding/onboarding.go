// Package onboarding defines the onboarding file schema and provides
// parsing and writing utilities for .agent-minder/onboarding.yaml files.
package onboarding

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"
)

// OnboardingFileName is the path relative to a repository root.
const OnboardingFileName = ".agent-minder/onboarding.yaml"

// File represents the complete onboarding file schema.
type File struct {
	Version   int       `yaml:"version"`
	ScannedAt time.Time `yaml:"scanned_at"`

	Inventory   Inventory   `yaml:"inventory"`
	Context     Context     `yaml:"context"`
	Permissions Permissions `yaml:"permissions"`
	Validation  Validation  `yaml:"validation"`
}

// Inventory captures the mechanical scan results from a repository.
type Inventory struct {
	Languages       []string             `yaml:"languages"`
	PackageManagers []string             `yaml:"package_managers"`
	BuildFiles      []string             `yaml:"build_files"`
	CI              []string             `yaml:"ci"`
	Tooling         Tooling              `yaml:"tooling"`
	ExistingClaude  ExistingClaudeConfig `yaml:"existing_claude_config"`
}

// Tooling captures detected development tooling.
type Tooling struct {
	Secrets    string   `yaml:"secrets"`    // e.g., "doppler", or empty
	Process    string   `yaml:"process"`    // e.g., "overmind", or empty
	Containers string   `yaml:"containers"` // e.g., "docker-compose", or empty
	Env        []string `yaml:"env"`        // e.g., ["direnv", ".tool-versions"]
}

// ExistingClaudeConfig tracks what Claude Code configuration already exists.
type ExistingClaudeConfig struct {
	SettingsJSON bool `yaml:"settings_json"`
	AgentDef     bool `yaml:"agent_def"`
	ClaudeMD     bool `yaml:"claude_md"`
}

// Context holds user-provided context populated by the onboarding agent.
type Context struct {
	BuildCommand        string   `yaml:"build_command"`
	TestCommand         string   `yaml:"test_command"`
	LintCommand         string   `yaml:"lint_command"`
	SpecialInstructions string   `yaml:"special_instructions"`
	ToolsNeeded         []string `yaml:"tools_needed"`
}

// Permissions holds the generated permission list derived from inventory + context.
type Permissions struct {
	AllowedTools []string `yaml:"allowed_tools"`
}

// Validation holds the results of the test-task validation run.
type Validation struct {
	LastRun  *time.Time `yaml:"last_run,omitempty"`
	Status   string     `yaml:"status"`   // "pass", "fail", "untested"
	Failures []string   `yaml:"failures"` // list of failure descriptions
}

// New creates a new onboarding file with version 1 and the given inventory.
func New(inv Inventory) *File {
	return &File{
		Version:   1,
		ScannedAt: time.Now().UTC(),
		Inventory: inv,
		Validation: Validation{
			Status:   "untested",
			Failures: []string{},
		},
	}
}

// Parse reads and parses an onboarding file from the given path.
func Parse(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read onboarding file: %w", err)
	}
	return ParseBytes(data)
}

// ParseBytes parses onboarding YAML from raw bytes.
func ParseBytes(data []byte) (*File, error) {
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse onboarding yaml: %w", err)
	}
	if f.Version == 0 {
		return nil, fmt.Errorf("onboarding file missing or invalid version")
	}
	return &f, nil
}

// Write serializes the onboarding file to the given path, creating parent
// directories as needed.
func Write(path string, f *File) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create onboarding dir: %w", err)
	}

	data, err := Marshal(f)
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write onboarding file: %w", err)
	}
	return nil
}

// Marshal serializes the onboarding file to YAML bytes.
func Marshal(f *File) ([]byte, error) {
	data, err := yaml.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("marshal onboarding yaml: %w", err)
	}
	return data, nil
}

// FilePath returns the onboarding file path for a given repo root directory.
func FilePath(repoDir string) string {
	return filepath.Join(repoDir, OnboardingFileName)
}

// Exists returns true if an onboarding file exists at the expected location
// within the given repo directory.
func Exists(repoDir string) bool {
	_, err := os.Stat(FilePath(repoDir))
	return err == nil
}
