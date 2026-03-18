package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dustinlange/agent-minder/internal/agentutil"
)

const (
	// testMaxTurns is the max turns for the validation test agent.
	testMaxTurns = 5
	// testMaxBudget is the max budget (USD) for the validation test agent.
	testMaxBudget = 0.25
	// MaxAttempts is the maximum number of refinement attempts.
	MaxAttempts = 3
)

// ValidateConfig holds parameters for running a test-task validation.
type ValidateConfig struct {
	RepoDir      string   // Target repository directory
	AllowedTools []string // Permission list from onboarding
	TestCommand  string   // e.g., "go test ./..."
	LintCommand  string   // e.g., "golangci-lint run"
	LogDir       string   // Directory for agent log files
	Model        string   // LLM model for the test agent (e.g., "claude-haiku-4-5")
}

// ValidateResult holds the outcome of the validation run.
type ValidateResult struct {
	Passed      bool     // Whether the test agent completed successfully
	Attempts    int      // Number of attempts made
	DeniedTools []string // Unique tool patterns that were denied across all attempts
	Failures    []string // Human-readable failure descriptions
	FinalTools  []string // The final allowed tools list (after refinements)
}

// CommandRunner abstracts execution of the claude CLI for testing.
type CommandRunner interface {
	// Run executes the claude command with the given args in dir, writing
	// all output to logPath. Returns the process exit code.
	Run(ctx context.Context, args []string, dir string, logPath string) (int, error)
}

// ExecRunner is the default CommandRunner that shells out to the claude CLI.
type ExecRunner struct{}

// Run executes `claude` with the given arguments, capturing output to logPath.
func (r *ExecRunner) Run(ctx context.Context, args []string, dir string, logPath string) (int, error) {
	logFile, err := os.Create(logPath)
	if err != nil {
		return -1, fmt.Errorf("create log file: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	err = cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return -1, fmt.Errorf("run claude: %w", err)
		}
	}
	return exitCode, nil
}

// Validate runs a test agent against the repo with the given permissions
// and retries up to MaxAttempts times if permission failures are detected.
// If runner is nil, the default exec-based runner is used.
func Validate(ctx context.Context, cfg ValidateConfig, runner CommandRunner) (*ValidateResult, error) {
	if runner == nil {
		runner = &ExecRunner{}
	}

	if cfg.LogDir == "" {
		cfg.LogDir = os.TempDir()
	}
	if err := os.MkdirAll(cfg.LogDir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	currentTools := make([]string, len(cfg.AllowedTools))
	copy(currentTools, cfg.AllowedTools)

	result := &ValidateResult{
		FinalTools: currentTools,
	}
	allDenied := make(map[string]bool)

	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		result.Attempts = attempt

		logPath := filepath.Join(cfg.LogDir, fmt.Sprintf("validation-attempt-%d.log", attempt))
		args := buildTestArgs(currentTools, cfg.TestCommand, cfg.LintCommand, cfg.Model)

		exitCode, err := runner.Run(ctx, args, cfg.RepoDir, logPath)
		if err != nil {
			result.Failures = append(result.Failures,
				fmt.Sprintf("attempt %d: %v", attempt, err))
			break
		}

		parsed, parseErr := agentutil.ParseAgentLog(logPath)
		if parseErr != nil {
			result.Failures = append(result.Failures,
				fmt.Sprintf("attempt %d: parse log: %v", attempt, parseErr))
			break
		}

		failReason := classifyValidation(parsed, exitCode)
		if failReason == "" {
			// Success.
			result.Passed = true
			result.FinalTools = currentTools
			for tool := range allDenied {
				result.DeniedTools = append(result.DeniedTools, tool)
			}
			return result, nil
		}

		if failReason != "permissions" {
			// Non-permission failure — don't retry.
			result.Failures = append(result.Failures,
				fmt.Sprintf("attempt %d: %s", attempt, failReason))
			break
		}

		// Permission failure — try to extract and add denied tools.
		denied := extractDeniedToolPatterns(parsed)
		if len(denied) == 0 {
			result.Failures = append(result.Failures,
				fmt.Sprintf("attempt %d: permission failure but could not identify denied tools", attempt))
			break
		}

		var newTools []string
		for _, tool := range denied {
			allDenied[tool] = true
			if !containsTool(currentTools, tool) {
				newTools = append(newTools, tool)
			}
		}
		if len(newTools) == 0 {
			result.Failures = append(result.Failures,
				fmt.Sprintf("attempt %d: permission failure for already-allowed tools: %s",
					attempt, strings.Join(denied, ", ")))
			break
		}

		currentTools = append(currentTools, newTools...)
		result.FinalTools = currentTools
		result.Failures = append(result.Failures,
			fmt.Sprintf("attempt %d: added tools: %s", attempt, strings.Join(newTools, ", ")))
		// Continue to next attempt with expanded tools.
	}

	// Collect denied tools from all attempts.
	for tool := range allDenied {
		result.DeniedTools = append(result.DeniedTools, tool)
	}
	result.FinalTools = currentTools
	return result, nil
}

// ApplyResult updates the onboarding file's Validation section based on
// the validation result. If refinement added new tools, permissions are
// also updated.
func ApplyResult(f *File, result *ValidateResult) {
	now := time.Now().UTC()
	f.Validation.LastRun = &now

	if result.Passed {
		f.Validation.Status = "pass"
		f.Validation.Failures = []string{}
	} else {
		f.Validation.Status = "fail"
		f.Validation.Failures = result.Failures
	}

	// Update permissions with any tools added during refinement.
	if len(result.FinalTools) > 0 {
		f.Permissions.AllowedTools = result.FinalTools
	}
}

// buildTestArgs constructs the claude CLI arguments for the test agent.
func buildTestArgs(allowedTools []string, testCmd, lintCmd, model string) []string {
	prompt := buildTestPrompt(testCmd, lintCmd)

	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--max-turns", fmt.Sprintf("%d", testMaxTurns),
		"--max-budget-usd", fmt.Sprintf("%.2f", testMaxBudget),
	}

	if model != "" {
		args = append(args, "--model", model)
	}

	for _, tool := range allowedTools {
		args = append(args, "--allowedTools", tool)
	}

	args = append(args, prompt)
	return args
}

// buildTestPrompt creates the prompt for the test agent.
func buildTestPrompt(testCmd, lintCmd string) string {
	var parts []string
	parts = append(parts,
		"Explore this repository and summarize the architecture, build system, and test framework.")

	if testCmd != "" {
		parts = append(parts, fmt.Sprintf("Run the test command: `%s`.", testCmd))
	}
	if lintCmd != "" {
		parts = append(parts, fmt.Sprintf("Run the lint command: `%s`.", lintCmd))
	}

	parts = append(parts, "Report what happened.")
	return strings.Join(parts, " ")
}

// classifyValidation determines the failure reason from the result event.
// Returns empty string for success, "permissions" for permission denials,
// or a human-readable description for other failures.
func classifyValidation(result *agentutil.AgentResult, exitCode int) string {
	if result == nil {
		if exitCode != 0 {
			return fmt.Sprintf("agent exited with code %d (no result event)", exitCode)
		}
		return "no result event in agent output"
	}

	// Permission denials take priority.
	if len(result.PermissionDenials) > 0 {
		return "permissions"
	}

	// Explicit error flag.
	if result.IsError {
		detail := result.Result
		if len(detail) > 500 {
			detail = detail[:500] + "..."
		}
		return fmt.Sprintf("agent error: %s", detail)
	}

	// Non-zero exit without structured failure.
	if exitCode != 0 {
		return fmt.Sprintf("agent exited with code %d", exitCode)
	}

	return ""
}

// extractDeniedToolPatterns parses the permission_denials array and returns
// unique tool patterns suitable for --allowedTools. For Bash denials that
// include a command, it derives a "Bash(<cmd> *)" pattern.
func extractDeniedToolPatterns(result *agentutil.AgentResult) []string {
	if result == nil || len(result.PermissionDenials) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	var patterns []string

	for _, raw := range result.PermissionDenials {
		pattern := denialToPattern(raw)
		if pattern != "" && !seen[pattern] {
			patterns = append(patterns, pattern)
			seen[pattern] = true
		}
	}

	return patterns
}

// denialToPattern converts a single permission denial entry into an
// --allowedTools pattern. Handles both object ({"tool_name": "...", ...})
// and plain string formats.
func denialToPattern(raw json.RawMessage) string {
	// Try as object with tool_name field.
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		name, _ := obj["tool_name"].(string)
		if name == "" {
			return ""
		}

		// For Bash tools, try to extract a more specific command pattern.
		if name == "Bash" {
			if cmd := extractDeniedCommand(obj); cmd != "" {
				parts := strings.Fields(cmd)
				if len(parts) > 0 {
					return fmt.Sprintf("Bash(%s *)", parts[0])
				}
			}
			return name // fallback to generic "Bash"
		}

		return name
	}

	// Try as plain string.
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}

	return ""
}

// extractDeniedCommand tries to find the denied command string from a
// permission denial object. Checks "command" at top level and nested
// inside "tool_input".
func extractDeniedCommand(obj map[string]any) string {
	if cmd, ok := obj["command"].(string); ok && cmd != "" {
		return cmd
	}
	if input, ok := obj["tool_input"].(map[string]any); ok {
		if cmd, ok := input["command"].(string); ok && cmd != "" {
			return cmd
		}
	}
	return ""
}

// containsTool checks if a tool pattern is already in the allowed list
// (exact string match).
func containsTool(tools []string, tool string) bool {
	for _, t := range tools {
		if t == tool {
			return true
		}
	}
	return false
}
