package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// AgentContract describes the expected behavior and pipeline for an agent type.
// Parsed from YAML frontmatter in .claude/agents/*.md files.
type AgentContract struct {
	// Identity (from existing frontmatter).
	Name        string `yaml:"name"`
	Description string `yaml:"description"`

	// Contract fields.
	Mode         string   `yaml:"mode"`          // "reactive" (needs issue#) or "proactive" (scans)
	Output       string   `yaml:"output"`        // "pr", "issue", "comment", "report", "none"
	BranchPrefix string   `yaml:"branch_prefix"` // worktree branch naming (default: "agent/issue")
	Dedup        []string `yaml:"dedup"`         // dedup strategies: "branch_exists", "open_pr_with_label:<label>", "recent_run:<hours>"
	Timeout      string   `yaml:"timeout"`       // overall job timeout (e.g., "2h", "30m")

	// Pipeline stages (optional — default is single-stage: just run the agent).
	Stages []StageContract `yaml:"stages"`
}

// StageContract describes one stage in a multi-stage pipeline.
type StageContract struct {
	Name      string `yaml:"name"`
	Agent     string `yaml:"agent"`      // agent name for this stage (default: parent agent)
	Timeout   string `yaml:"timeout"`    // per-stage timeout
	OnFailure string `yaml:"on_failure"` // "bail", "skip", "retry" (default: "bail")
	Retries   int    `yaml:"retries"`    // number of retries if on_failure=retry
}

// DefaultContract returns the default contract for agents that don't specify one.
func DefaultContract(agentName string) *AgentContract {
	return &AgentContract{
		Name:         agentName,
		Mode:         "reactive",
		Output:       "pr",
		BranchPrefix: "agent/issue",
		Timeout:      "2h",
		Stages: []StageContract{
			{Name: "code", Timeout: "45m", OnFailure: "bail"},
		},
	}
}

// DefaultAutopilotContract returns the full default contract for the autopilot agent.
func DefaultAutopilotContract() *AgentContract {
	return &AgentContract{
		Name:         "autopilot",
		Mode:         "reactive",
		Output:       "pr",
		BranchPrefix: "agent/issue",
		Timeout:      "2h",
		Stages: []StageContract{
			{Name: "code", Timeout: "45m", OnFailure: "bail"},
			{Name: "review", Agent: "reviewer", Timeout: "15m", OnFailure: "skip", Retries: 1},
		},
	}
}

// IsReactive returns true if the agent needs an issue number.
func (c *AgentContract) IsReactive() bool {
	return c.Mode != "proactive"
}

// TimeoutDuration parses the timeout string into a time.Duration.
// Returns 2h as default if parsing fails.
func (c *AgentContract) TimeoutDuration() time.Duration {
	if c.Timeout == "" {
		return 2 * time.Hour
	}
	d, err := time.ParseDuration(c.Timeout)
	if err != nil {
		return 2 * time.Hour
	}
	return d
}

// TimeoutDuration parses a stage timeout string.
// Returns 45m as default.
func (s *StageContract) TimeoutDuration() time.Duration {
	if s.Timeout == "" {
		return 45 * time.Minute
	}
	d, err := time.ParseDuration(s.Timeout)
	if err != nil {
		return 45 * time.Minute
	}
	return d
}

// ParseContract extracts the AgentContract from an agent definition file.
// Returns the default contract if the file has no contract fields.
func ParseContract(agentDefPath string) (*AgentContract, error) {
	data, err := os.ReadFile(agentDefPath)
	if err != nil {
		return nil, fmt.Errorf("read agent def: %w", err)
	}
	return ParseContractFromBytes(data)
}

// ParseContractFromBytes extracts the AgentContract from agent definition content.
func ParseContractFromBytes(data []byte) (*AgentContract, error) {
	frontmatter, err := extractFrontmatter(string(data))
	if err != nil {
		return nil, err
	}

	var contract AgentContract
	if err := yaml.Unmarshal([]byte(frontmatter), &contract); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	// Apply defaults for missing fields.
	applyContractDefaults(&contract)

	return &contract, nil
}

// extractFrontmatter extracts YAML frontmatter between --- markers.
func extractFrontmatter(content string) (string, error) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return "", fmt.Errorf("no frontmatter found (missing opening ---)")
	}

	// Find the closing ---.
	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", fmt.Errorf("no frontmatter found (missing closing ---)")
	}

	return strings.TrimSpace(rest[:idx]), nil
}

// applyContractDefaults fills in missing contract fields with sensible defaults.
func applyContractDefaults(c *AgentContract) {
	if c.Mode == "" {
		c.Mode = "reactive"
	}
	if c.Output == "" {
		c.Output = "pr"
	}
	if c.BranchPrefix == "" {
		c.BranchPrefix = "agent/issue"
	}
	if c.Timeout == "" {
		c.Timeout = "2h"
	}

	// Normalize stage defaults.
	for i := range c.Stages {
		if c.Stages[i].OnFailure == "" {
			c.Stages[i].OnFailure = "bail"
		}
		if c.Stages[i].Agent == "" {
			c.Stages[i].Agent = c.Name
		}
	}
}

// ResolveContract loads and parses the contract for a named agent,
// searching the 3-tier fallback: repo → user → built-in.
func ResolveContract(repoDir string, agentName string) (*AgentContract, error) {
	filename := agentName + ".md"

	// Check repo-level.
	repoPath := filepath.Join(repoDir, ".claude", "agents", filename)
	if _, err := os.Stat(repoPath); err == nil {
		return ParseContract(repoPath)
	}

	// Check user-level.
	home, _ := os.UserHomeDir()
	userPath := filepath.Join(home, ".claude", "agents", filename)
	if _, err := os.Stat(userPath); err == nil {
		return ParseContract(userPath)
	}

	// Built-in defaults.
	switch agentName {
	case "autopilot":
		return ParseContractFromBytes([]byte(defaultAgentDef))
	case "reviewer":
		return ParseContractFromBytes([]byte(defaultReviewerDef))
	default:
		return DefaultContract(agentName), nil
	}
}
