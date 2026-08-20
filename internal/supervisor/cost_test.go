package supervisor

// Regression coverage for Exp V item 11 (docs/research/fable-expedition/
// 05-runtime-conformance-and-resolution.md §7 L1/L2): jobs.cost_usd must come
// from the runtime's own Result.TotalCostUSD, not from scraping the agent log
// for the literal string "total_cost_usd" — a key only claude-code ever
// writes. Before this fix, codex and opencode jobs always recorded cost_usd
// = 0, so --total-budget never stopped a codex or opencode fleet (TotalSpend
// sums jobs.cost_usd; see internal/db/queries.go TotalSpend and
// internal/supervisor/supervisor.go checkBudgetCeiling).

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/aptx-health/agent-minder/internal/db"
	runtimepkg "github.com/aptx-health/agent-minder/internal/runtime"
)

// legacyParseCostFromLog reimplements the deleted supervisor.parseCostFromLog
// exactly (it scanned the raw agent log for `"total_cost_usd"` and took the
// largest value found). It exists only in this test file, to prove that the
// new runtime-native cost path records the identical number for claude-code
// that the old log-scrape would have produced, and to demonstrate that the
// old approach really did miss codex/opencode costs (they never emit the
// key).
func legacyParseCostFromLog(logPath string) float64 {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}
	lines := strings.Split(string(data), "\n")
	var cost float64
	for _, line := range lines {
		if idx := strings.Index(line, `"total_cost_usd"`); idx >= 0 {
			rest := line[idx+len(`"total_cost_usd"`):]
			rest = strings.TrimLeft(rest, `: `)
			end := strings.IndexAny(rest, ",}")
			if end > 0 {
				if v, err := strconv.ParseFloat(strings.TrimSpace(rest[:end]), 64); err == nil && v > cost {
					cost = v
				}
			}
		}
	}
	return cost
}

// singleStageContract builds a minimal one-stage, non-PR contract so the
// pipeline doesn't need a fake GitHub PR to reach finalizePipeline.
func singleStageContract() *AgentContract {
	contract := &AgentContract{
		Name:   "autopilot",
		Output: "issue",
		Stages: []StageContract{{Name: "run", Agent: "autopilot"}},
	}
	applyContractDefaults(contract)
	return contract
}

// TestJobCostMatchesLegacyLogScrapeForClaudeCode proves the CRITICAL
// constraint from Exp V item 11: for a claude-code-shaped log (one that does
// contain "total_cost_usd", exactly as the real CLI writes it), the cost now
// recorded on the job via Result.TotalCostUSD is identical to what the
// deleted log-scrape would have produced by reading the same log file.
func TestJobCostMatchesLegacyLogScrapeForClaudeCode(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) { d.ReviewEnabled = false })

	const wantCost = 0.4321
	h.hooks.RunStageFn = func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
		// Exactly how claude-code's stream-json result event looks on disk.
		if logFile != nil {
			_, _ = logFile.Write([]byte(`{"type":"result","subtype":"success","is_error":false,"num_turns":4,"total_cost_usd":0.4321,"session_id":"sess-cc","result":"done"}` + "\n"))
		}
		return 0, &runtimepkg.Result{
			SessionID:    "sess-cc",
			NumTurns:     4,
			TotalCostUSD: wantCost,
			FinalText:    "done",
		}, false, nil
	}

	job := testJob(t, h.store, h.deploy)
	if err := h.run(context.Background(), job, singleStageContract()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.CostUSD != wantCost {
		t.Fatalf("jobs.cost_usd = %v, want %v (from Result.TotalCostUSD)", got.CostUSD, wantCost)
	}

	legacy := legacyParseCostFromLog(h.logPathForJob(job.ID))
	if legacy != got.CostUSD {
		t.Fatalf("recorded cost %v diverges from what the old log-scrape would have produced (%v) — "+
			"claude-code's recorded cost must stay identical", got.CostUSD, legacy)
	}
}

// TestJobCostNonZeroForCodexRuntime is the §13 verification-checklist item:
// a fake codex run must leave jobs.cost_usd non-zero. Codex logs never
// contain "total_cost_usd" (its cost is an estimate from a static price
// table, computed by internal/runtime/codex and returned via
// Result.TotalCostUSD) — the log content below mirrors that shape and
// deliberately omits the key the old scrape depended on.
func TestJobCostNonZeroForCodexRuntime(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = false
		d.Runtime = runtimepkg.NameCodex
	})

	const wantCost = 1.23
	h.hooks.RunStageFn = func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
		if logFile != nil {
			_, _ = logFile.Write([]byte(`{"type":"thread.started","thread_id":"thread-1"}` + "\n"))
			_, _ = logFile.Write([]byte(`{"type":"turn.completed","thread_id":"thread-1"}` + "\n"))
		}
		return 0, &runtimepkg.Result{
			SessionID:    "thread-1",
			NumTurns:     1,
			TotalCostUSD: wantCost, // estimated, from codex's static price table
			FinalText:    "done",
		}, false, nil
	}

	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.Runtime = toNullStr(runtimepkg.NameCodex)
	})
	if err := h.run(context.Background(), job, singleStageContract()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.CostUSD == 0 {
		t.Fatalf("jobs.cost_usd = 0 for codex run — L1 leak not fixed (--total-budget would never stop a codex fleet)")
	}
	if got.CostUSD != wantCost {
		t.Fatalf("jobs.cost_usd = %v, want %v (from Result.TotalCostUSD)", got.CostUSD, wantCost)
	}

	// Confirm the log genuinely lacks the key the deleted scrape needed —
	// this is what made the old code record 0 for codex.
	if legacy := legacyParseCostFromLog(h.logPathForJob(job.ID)); legacy != 0 {
		t.Fatalf("test log unexpectedly contains a scrapeable cost (%v); fixture no longer represents the codex leak", legacy)
	}
}

// TestJobCostNonZeroForOpencodeRuntime is the §13 verification-checklist
// item for opencode: its exact cost lives at `info.cost` in the SDK
// response, never as a bare "total_cost_usd" string in the log.
func TestJobCostNonZeroForOpencodeRuntime(t *testing.T) {
	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = false
		d.Runtime = runtimepkg.NameOpenCode
	})

	const wantCost = 0.0123
	h.hooks.RunStageFn = func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
		if logFile != nil {
			_, _ = logFile.Write([]byte(`{"type":"message.updated","info":{"cost":0.0123}}` + "\n"))
		}
		return 0, &runtimepkg.Result{
			SessionID:    "opencode-session",
			NumTurns:     1,
			TotalCostUSD: wantCost, // exact, from opencode's info.cost
			FinalText:    "done",
		}, false, nil
	}

	job := testJob(t, h.store, h.deploy, func(j *db.Job) {
		j.Runtime = toNullStr(runtimepkg.NameOpenCode)
	})
	if err := h.run(context.Background(), job, singleStageContract()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.CostUSD == 0 {
		t.Fatalf("jobs.cost_usd = 0 for opencode run — L1 leak not fixed (--total-budget would never stop an opencode fleet)")
	}
	if got.CostUSD != wantCost {
		t.Fatalf("jobs.cost_usd = %v, want %v (from Result.TotalCostUSD)", got.CostUSD, wantCost)
	}

	if legacy := legacyParseCostFromLog(h.logPathForJob(job.ID)); legacy != 0 {
		t.Fatalf("test log unexpectedly contains a scrapeable cost (%v); fixture no longer represents the opencode leak", legacy)
	}
}
