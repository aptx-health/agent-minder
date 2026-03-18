package autopilot

import (
	"encoding/json"
	"fmt"

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
func classifyOutcome(result *AgentResult, maxTurns int, maxBudget float64) (string, string, string) {
	if result == nil {
		return "", "", ""
	}

	// 1. Permission denials (non-empty array).
	if len(result.PermissionDenials) > 0 {
		detail, _ := json.Marshal(result.PermissionDenials)
		return "failed", "permissions", string(detail)
	}

	// 2. Turn limit exhaustion.
	if maxTurns > 0 && result.NumTurns >= maxTurns {
		return "failed", "max_turns", fmt.Sprintf("used %d of %d turns", result.NumTurns, maxTurns)
	}

	// 3. Budget exhaustion (>= 95% of limit).
	if maxBudget > 0 && result.TotalCost >= maxBudget*0.95 {
		return "failed", "max_budget", fmt.Sprintf("spent $%.2f of $%.2f budget", result.TotalCost, maxBudget)
	}

	// 4. Explicit error flag.
	if result.IsError {
		detail := result.Result
		if len(detail) > 500 {
			detail = detail[:500] + "..."
		}
		return "failed", "error", detail
	}

	// No failure detected.
	return "", "", ""
}
