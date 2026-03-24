package autopilot

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/dustinlange/agent-minder/internal/agentutil"
)

// AgentResult is an alias for the shared type, kept for backward compatibility
// within the autopilot package.
type AgentResult = agentutil.AgentResult

// parseAgentLog delegates to the shared parser.
func parseAgentLog(logPath string) (*AgentResult, error) {
	return agentutil.ParseAgentLog(logPath)
}

// classifyOutcome determines failure reason from the result event + config.
// Returns (status, failureReason, failureDetail).
// Empty status means no failure detected — caller should continue with PR check.
// "warning" status means a non-fatal issue was detected (e.g., permission denials
// that the agent may have worked around) — caller should still check for a PR.
func classifyOutcome(result *AgentResult, maxTurns int, maxBudget float64) (string, string, string) {
	if result == nil {
		return "", "", ""
	}

	// 1. Turn limit exhaustion.
	if maxTurns > 0 && result.NumTurns >= maxTurns {
		return "failed", "max_turns", fmt.Sprintf("used %d of %d turns", result.NumTurns, maxTurns)
	}

	// 2. Budget exhaustion (>= 95% of limit).
	if maxBudget > 0 && result.TotalCost >= maxBudget*0.95 {
		return "failed", "max_budget", fmt.Sprintf("spent $%.2f of $%.2f budget", result.TotalCost, maxBudget)
	}

	// 3. Explicit error flag.
	if result.IsError {
		detail := result.Result
		if len(detail) > 500 {
			detail = detail[:500] + "..."
		}
		return "failed", "error", detail
	}

	// 4. Permission denials — non-fatal warning. The agent may have worked
	// around denied tools and still completed the task successfully. The
	// caller should proceed to check for a PR before deciding the outcome.
	if len(result.PermissionDenials) > 0 {
		detail, _ := json.Marshal(result.PermissionDenials)
		return "warning", "permissions", string(detail)
	}

	// No failure detected.
	return "", "", ""
}

// countTurnsFromLog scans a stream-json log file and counts the number of
// assistant-type events, which corresponds to the agent's turn count. This is
// used as a fallback when the result event is missing (e.g., the agent process
// was killed or the log was truncated).
func countTurnsFromLog(logPath string) int {
	if logPath == "" {
		return 0
	}
	f, err := os.Open(logPath)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	type eventType struct {
		Type string `json:"type"`
	}

	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		var evt eventType
		if json.Unmarshal(scanner.Bytes(), &evt) == nil && evt.Type == "assistant" {
			count++
		}
	}
	return count
}
