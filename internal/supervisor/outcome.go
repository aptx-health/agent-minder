package supervisor

import "github.com/aptx-health/agent-minder/internal/agentutil"

// AgentResult is the legacy stream-json result struct, retained as an alias so
// supervisor code that already speaks in this shape (usage-limit detection,
// review extraction) doesn't have to migrate. Outcome classification and log
// parsing now live on the AgentRuntime — see Supervisor.Runtime().
type AgentResult = agentutil.AgentResult
