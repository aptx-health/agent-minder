package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// SessionResult holds the output from a claude CLI invocation.
type SessionResult struct {
	SessionID string
	Output    string
	IsResume  bool
}

// jsonOutput is the structured output from claude --json.
type jsonOutput struct {
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	Type      string `json:"type"`
}

// OneShot runs `claude -p` with the given prompt and returns the result.
func OneShot(prompt string, workDir string) (*SessionResult, error) {
	args := []string{"-p", prompt, "--output-format", "json"}
	return run(args, workDir)
}

// Resume continues a previous session with `claude --resume`.
func Resume(sessionID string, prompt string, workDir string) (*SessionResult, error) {
	args := []string{"--resume", sessionID, "-p", prompt, "--output-format", "json"}
	result, err := run(args, workDir)
	if err != nil {
		return nil, err
	}
	result.IsResume = true
	return result, nil
}

// ContinueLast continues the most recent session with `claude -c`.
func ContinueLast(prompt string, workDir string) (*SessionResult, error) {
	args := []string{"-c", "-p", prompt, "--output-format", "json"}
	return run(args, workDir)
}

// Start launches a new monitoring session. Returns the session result
// including session ID for later resume.
func Start(prompt string, workDir string, allowedTools []string) (*SessionResult, error) {
	args := []string{"-p", prompt, "--output-format", "json"}
	for _, tool := range allowedTools {
		args = append(args, "--allowedTools", tool)
	}
	return run(args, workDir)
}

// Verbose controls whether claude's stderr is streamed to the terminal.
var Verbose = true

// run executes the claude CLI and parses the result.
func run(args []string, workDir string) (*SessionResult, error) {
	cmd := exec.Command("claude", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	// Stream stderr to terminal so the user sees progress,
	// while also capturing it for error reporting.
	var stderr bytes.Buffer
	if Verbose {
		cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
	} else {
		cmd.Stderr = &stderr
	}

	if err := cmd.Run(); err != nil {
		stderrStr := stderr.String()
		if stderrStr != "" {
			return nil, fmt.Errorf("claude CLI: %s", stderrStr)
		}
		return nil, fmt.Errorf("claude CLI: %w", err)
	}

	output := strings.TrimSpace(stdout.String())
	result := &SessionResult{Output: output}

	// Try to parse JSON output for session ID.
	var parsed jsonOutput
	if err := json.Unmarshal([]byte(output), &parsed); err == nil {
		result.SessionID = parsed.SessionID
		result.Output = parsed.Result
	}

	return result, nil
}

// IsAvailable checks if the claude CLI is installed and reachable.
func IsAvailable() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// Version returns the claude CLI version string.
func Version() (string, error) {
	cmd := exec.Command("claude", "--version")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude --version: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
