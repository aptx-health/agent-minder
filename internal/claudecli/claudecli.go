// Package claudecli wraps the Claude Code CLI (`claude -p`) for programmatic LLM calls.
// It replaces the direct Anthropic/OpenAI API usage in internal/llm with CLI invocations,
// eliminating the need for users to configure API keys separately from Claude Code.
package claudecli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Completer runs a single claude -p call and returns the response.
type Completer interface {
	Complete(ctx context.Context, req *Request) (*Response, error)
}

// Request describes a single claude -p invocation.
type Request struct {
	SystemPrompt string  // --system-prompt
	Prompt       string  // positional prompt text
	Model        string  // --model (e.g. "haiku", "sonnet", "opus", or full model ID)
	JSONSchema   string  // --json-schema for structured output
	Tools        string  // "" to disable tools (--tools ""), omit for default
	DisableTools bool    // explicit flag to pass --tools ""
	MaxBudget    float64 // --max-budget-usd
}

// Response holds the parsed output from claude -p --output-format json.
type Response struct {
	Result           string          // from "result" field
	StructuredOutput json.RawMessage // from "structured_output" field (when --json-schema used)
	IsError          bool
	CostUSD          float64
	NumTurns         int
	SessionID        string
	InputTokens      int
	OutputTokens     int
}

// cliOutput is the raw JSON envelope returned by claude -p --output-format json.
type cliOutput struct {
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	IsError          bool            `json:"is_error"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	NumTurns         int             `json:"num_turns"`
	SessionID        string          `json:"session_id"`
	Usage            struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// CLICompleter implements Completer by shelling out to the claude CLI.
type CLICompleter struct {
	// ClaudeBin is the path to the claude binary. Defaults to "claude".
	ClaudeBin string
}

// NewCLICompleter creates a CLICompleter with the default claude binary.
func NewCLICompleter() *CLICompleter {
	return &CLICompleter{ClaudeBin: "claude"}
}

// Complete runs claude -p with the given request and returns the parsed response.
func (c *CLICompleter) Complete(ctx context.Context, req *Request) (*Response, error) {
	args := []string{"-p", "--output-format", "json"}

	if req.SystemPrompt != "" {
		args = append(args, "--system-prompt", req.SystemPrompt)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.JSONSchema != "" {
		args = append(args, "--json-schema", req.JSONSchema)
	}
	if req.DisableTools {
		args = append(args, "--tools", "")
	} else if req.Tools != "" {
		args = append(args, "--tools", req.Tools)
	}
	if req.MaxBudget > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(req.MaxBudget, 'f', -1, 64))
	}

	// The prompt is the last positional argument.
	args = append(args, req.Prompt)

	bin := c.ClaudeBin
	if bin == "" {
		bin = "claude"
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.Output()
	if err != nil {
		// Try to extract stderr for better error messages.
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if stderr != "" {
				return nil, fmt.Errorf("claude CLI failed: %s", stderr)
			}
			// Even on non-zero exit, claude may have written JSON to stdout.
			if len(out) > 0 {
				resp, parseErr := parseOutput(out)
				if parseErr == nil {
					resp.IsError = true
					return resp, nil
				}
			}
		}
		return nil, fmt.Errorf("claude CLI: %w", err)
	}

	return parseOutput(out)
}

// parseOutput parses the JSON envelope from claude -p --output-format json.
func parseOutput(data []byte) (*Response, error) {
	var raw cliOutput
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing claude output: %w (raw: %s)", err, truncate(string(data), 200))
	}

	return &Response{
		Result:           raw.Result,
		StructuredOutput: raw.StructuredOutput,
		IsError:          raw.IsError,
		CostUSD:          raw.TotalCostUSD,
		NumTurns:         raw.NumTurns,
		SessionID:        raw.SessionID,
		InputTokens:      raw.Usage.InputTokens,
		OutputTokens:     raw.Usage.OutputTokens,
	}, nil
}

// truncate shortens a string for error messages.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// Content returns the best available text content from the response.
// When --json-schema is used, the structured output is the primary response;
// otherwise, the result field is used.
func (r *Response) Content() string {
	if len(r.StructuredOutput) > 0 && string(r.StructuredOutput) != "null" {
		return string(r.StructuredOutput)
	}
	return r.Result
}

// CheckVersion verifies that the claude CLI is installed and reachable.
func CheckVersion(bin string) (string, error) {
	if bin == "" {
		bin = "claude"
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("claude CLI not found — install from https://claude.ai/code: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
