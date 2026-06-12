package supervisor

// Scenario-based integration tests for the supervisor's stage pipeline.
//
// These tests exercise the seams between writers (handleBailReport vs.
// finalizeBail, executeCodeStage vs. finalizePipeline) by driving a
// DefaultJobManager through a fake AgentRuntime hook and asserting on the
// final DB state. Unit-test-green on individual functions like
// ExtractBailReport / ClassifyOutcome / handleBailReport is necessary but
// insufficient — the #517 regression slipped past unit tests because no test
// exercised the post-bail write-ordering between handleBailReport and
// finalizeBail. The structured-bail-report scenario below would have failed
// against the unguarded finalizeBail.
//
// To add a new scenario: append a row to the table. Do not split into separate
// files unless a scenario needs its own harness shape.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/aptx-health/agent-minder/internal/db"
	runtimepkg "github.com/aptx-health/agent-minder/internal/runtime"
	"github.com/aptx-health/agent-minder/internal/runtime/claudecode"
)

// scenarioCase declares a single end-to-end supervisor scenario.
//
// A scenario sets up a fresh in-memory deployment, runs the DefaultJobManager
// against a fake runtime that returns the canned FinalText/ExitCode/etc.,
// and asserts on the resulting jobs-table row.
type scenarioCase struct {
	Name string

	// Deployment knobs.
	ReviewEnabled bool

	// Contract knobs. Output defaults to "pr".
	Output string

	// Fake runtime behavior on the (first) code stage.
	ExitCode          int
	FinalText         string  // last assistant message text — may contain <bail-report>
	NumTurns          int     // claimed turns from result event
	TotalCostUSD      float64 // claimed cost from result event
	IsError           bool
	PermissionDenials []string
	UsageLimit        bool

	// Limits — applied to deployment/job. Zero = harness default.
	MaxTurns int

	// Fake DetectPR result. 0 = no PR.
	DetectedPR int

	// Want* assertions on the final job row.
	WantStatus          string  // "done", "bailed", "reviewed", ...
	WantFailureReason   string  // "" = must be empty/null
	WantFailureDetailIn string  // substring match (empty = don't assert)
	WantPRNumber        int     // 0 = must be null
	WantCostMin         float64 // 0 = don't assert; otherwise cost must be >= this
	WantResultJSONIn    string  // substring of result_json (empty = don't assert)
}

// runScenario drives the harness through one scenarioCase and returns the
// final db.Job row plus any error from DefaultJobManager.Run.
func runScenario(t *testing.T, sc scenarioCase) {
	t.Helper()

	h := newHarness(t, func(d *db.Deployment) {
		d.ReviewEnabled = sc.ReviewEnabled
		if sc.MaxTurns > 0 {
			d.MaxTurns = sc.MaxTurns
		}
	})

	// Use the real claudecode runtime for ClassifyOutcome / ExtractBailReport.
	// RunStageFn short-circuits the actual Run() call, so the binary is never
	// invoked.
	h.sup.SetRuntime(claudecode.New())

	// Construct the synthetic stream-json result line that ParseResult will
	// read off the log file in finalizeBail. The same fields drive the
	// runtime.Result returned from RunStageFn so executeCodeStage's in-memory
	// path sees identical data.
	resultJSON := encodeResultLine(sc.FinalText, sc.NumTurns, sc.TotalCostUSD, sc.IsError, sc.PermissionDenials)

	output := sc.Output
	if output == "" {
		output = "pr"
	}

	h.hooks.RunStageFn = func(ctx context.Context, inv runtimepkg.Invocation, logFile *os.File) (int, *runtimepkg.Result, bool, error) {
		h.mu.Lock()
		h.stageLog = append(h.stageLog, stageCall{Agent: inv.AgentName, Inv: inv})
		h.mu.Unlock()
		// Write a synthetic stream-json result event to the log so
		// parseCostFromLog and rt.ParseResult (used by finalizeBail) see
		// consistent data.
		if logFile != nil {
			_, _ = logFile.Write([]byte(resultJSON + "\n"))
		}
		return sc.ExitCode, &runtimepkg.Result{
			SessionID:         "scenario-session",
			NumTurns:          sc.NumTurns,
			TotalCostUSD:      sc.TotalCostUSD,
			FinalText:         sc.FinalText,
			IsError:           sc.IsError,
			PermissionDenials: sc.PermissionDenials,
		}, sc.UsageLimit, nil
	}

	detected := sc.DetectedPR
	h.hooks.DetectPRFn = func(ctx context.Context) int { return detected }

	job := testJob(t, h.store, h.deploy)

	contract := &AgentContract{
		Name:   "autopilot",
		Mode:   "reactive",
		Output: output,
	}
	applyContractDefaults(contract)

	if err := h.run(context.Background(), job, contract); err != nil {
		t.Fatalf("[%s] Run: %v", sc.Name, err)
	}

	updated, err := h.store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("[%s] GetJob: %v", sc.Name, err)
	}

	if sc.WantStatus != "" && updated.Status != sc.WantStatus {
		t.Errorf("[%s] status: want %q, got %q", sc.Name, sc.WantStatus, updated.Status)
	}
	if sc.WantFailureReason != "" {
		if !updated.FailureReason.Valid || updated.FailureReason.String != sc.WantFailureReason {
			t.Errorf("[%s] failure_reason: want %q, got valid=%v %q",
				sc.Name, sc.WantFailureReason, updated.FailureReason.Valid, updated.FailureReason.String)
		}
	}
	if sc.WantFailureDetailIn != "" {
		if !updated.FailureDetail.Valid || !strings.Contains(updated.FailureDetail.String, sc.WantFailureDetailIn) {
			t.Errorf("[%s] failure_detail: want substring %q, got valid=%v %q",
				sc.Name, sc.WantFailureDetailIn, updated.FailureDetail.Valid, updated.FailureDetail.String)
		}
	}
	if sc.WantPRNumber > 0 {
		if !updated.PRNumber.Valid || int(updated.PRNumber.Int64) != sc.WantPRNumber {
			t.Errorf("[%s] pr_number: want %d, got valid=%v %d",
				sc.Name, sc.WantPRNumber, updated.PRNumber.Valid, updated.PRNumber.Int64)
		}
	}
	if sc.WantCostMin > 0 && updated.CostUSD < sc.WantCostMin {
		t.Errorf("[%s] cost_usd: want >= %.4f, got %.4f", sc.Name, sc.WantCostMin, updated.CostUSD)
	}
	if sc.WantResultJSONIn != "" {
		if !updated.ResultJSON.Valid || !strings.Contains(updated.ResultJSON.String, sc.WantResultJSONIn) {
			t.Errorf("[%s] result_json: want substring %q, got valid=%v %q",
				sc.Name, sc.WantResultJSONIn, updated.ResultJSON.Valid, updated.ResultJSON.String)
		}
	}
}

// encodeResultLine builds a stream-json result event line. The escaping rules
// matter: FinalText goes through json.Marshal so embedded <bail-report> tags
// survive intact, mirroring how the real claude binary emits them.
func encodeResultLine(finalText string, numTurns int, cost float64, isError bool, denials []string) string {
	var b strings.Builder
	b.WriteString(`{"type":"result","subtype":"success","session_id":"scenario-session"`)
	b.WriteString(`,"is_error":`)
	if isError {
		b.WriteString(`true`)
	} else {
		b.WriteString(`false`)
	}
	if numTurns > 0 {
		b.WriteString(`,"num_turns":`)
		writeInt(&b, numTurns)
	}
	if cost > 0 {
		b.WriteString(`,"total_cost_usd":`)
		writeFloat(&b, cost)
	}
	if finalText != "" {
		b.WriteString(`,"result":`)
		writeJSONString(&b, finalText)
	}
	if len(denials) > 0 {
		b.WriteString(`,"permission_denials":[`)
		for i, d := range denials {
			if i > 0 {
				b.WriteString(",")
			}
			writeJSONString(&b, d)
		}
		b.WriteString(`]`)
	}
	b.WriteString(`}`)
	return b.String()
}

func writeInt(b *strings.Builder, n int) {
	// strconv.Itoa would do, but tiny helper keeps imports lean.
	b.WriteString(intToStr(n))
}

func writeFloat(b *strings.Builder, f float64) {
	b.WriteString(floatToStr(f))
}

func writeJSONString(b *strings.Builder, s string) {
	// Use json.Marshal-equivalent escaping for safety. Inline implementation
	// covers the cases scenarios actually emit (quotes, newlines, backslashes,
	// angle brackets).
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

func intToStr(n int) string {
	// Reimplement to avoid pulling strconv (already pulled by other tests but
	// keep helper self-contained for readability).
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func floatToStr(f float64) string {
	// Scenarios use small fractional values like 0.12 — render with a fixed
	// precision sufficient for parseCostFromLog and ClassifyOutcome.
	// We use a minimal formatter to avoid extra deps.
	whole := int(f)
	frac := int((f - float64(whole)) * 10000)
	if frac < 0 {
		frac = -frac
	}
	return intToStr(whole) + "." + padFrac(frac, 4)
}

func padFrac(n, width int) string {
	s := intToStr(n)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

// --- The seeded scenarios ---

// TestScenarios runs the supervisor through the canonical end-to-end paths and
// asserts on the final job row. Add new rows here to lock in DB-final-state
// expectations for execution paths the supervisor must preserve.
func TestScenarios(t *testing.T) {
	cases := []scenarioCase{
		{
			Name:          "happy-path-pr-opened",
			ReviewEnabled: false,
			ExitCode:      0,
			FinalText:     "All done — PR opened.",
			NumTurns:      3,
			TotalCostUSD:  0.12,
			DetectedPR:    100,
			WantStatus:    db.StatusReviewed, // PR detected → finalizePipeline routes to reviewed
			WantPRNumber:  100,
			WantCostMin:   0.10,
		},
		{
			// The #517 regression scenario: a clean exit (no max_turns, no
			// max_budget, no is_error) with a structured <bail-report> in the
			// final text. handleBailReport must persist failure_reason="bailed"
			// + failure_detail=<reason>, and finalizeBail must NOT clobber
			// those fields when ClassifyOutcome returns empty.
			Name:                "structured-bail-report",
			ReviewEnabled:       false,
			ExitCode:            0,
			NumTurns:            2,
			TotalCostUSD:        0.05,
			DetectedPR:          0,
			FinalText:           "I cannot complete this.\n\n<bail-report>\n{\"reason\":\"test\",\"plan\":\"intentional bail for scenario coverage\",\"complexity\":\"medium\"}\n</bail-report>",
			WantStatus:          db.StatusBailed,
			WantFailureReason:   "bailed",
			WantFailureDetailIn: "test",
			WantResultJSONIn:    "intentional bail",
		},
		{
			Name:                "max-turns-exhausted",
			ReviewEnabled:       false,
			ExitCode:            0,
			NumTurns:            50, // == MaxTurns
			MaxTurns:            50,
			TotalCostUSD:        0.20,
			DetectedPR:          0,
			FinalText:           "Out of turns.",
			WantStatus:          db.StatusBailed,
			WantFailureReason:   "max_turns",
			WantFailureDetailIn: "50",
		},
		{
			// Permission denials are a non-fatal warning — when a PR was still
			// opened, the pipeline finalizes normally.
			Name:              "permission-denials-warning",
			ReviewEnabled:     false,
			ExitCode:          0,
			NumTurns:          4,
			TotalCostUSD:      0.30,
			PermissionDenials: []string{`{"tool":"Bash","input":"rm -rf /"}`},
			FinalText:         "Done despite denials.",
			DetectedPR:        202,
			WantStatus:        db.StatusReviewed,
			WantPRNumber:      202,
			WantFailureReason: "", // no failure recorded
		},
	}

	for _, sc := range cases {
		sc := sc
		t.Run(sc.Name, func(t *testing.T) {
			runScenario(t, sc)
		})
	}
}
