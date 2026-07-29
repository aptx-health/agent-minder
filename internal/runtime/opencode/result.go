package opencode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

// parsedInfo is the subset of opencode's final AssistantMessage that ParseResult
// consumes. cost is real USD; tokens is the structured breakdown.
type parsedInfo struct {
	Cost   float64 `json:"cost"`
	Tokens struct {
		Total     float64 `json:"total"`
		Input     float64 `json:"input"`
		Output    float64 `json:"output"`
		Reasoning float64 `json:"reasoning"`
	} `json:"tokens"`
	Error struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

// parsedPart is the subset of a message Part ParseResult consumes. Final answer
// text lives in "text" parts; "reasoning" parts are the model's thinking and are
// deliberately excluded from FinalText. "step-start" parts mark agentic turns.
type parsedPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ParseResult reads the run log written by Run/Resume and lifts the normalized
// Result from the canonical result record. Returns (nil, nil) when no record is
// present (e.g., the process died before Run wrote one). The last record wins,
// so a Resume that appends a fresh record supersedes the original.
func (o *OpencodeRuntime) ParseResult(logPath string) (*runtime.Result, error) {
	if logPath == "" {
		return nil, nil
	}
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var rec *logRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 4*1024*1024)
	for scanner.Scan() {
		var candidate logRecord
		if err := json.Unmarshal(scanner.Bytes(), &candidate); err != nil {
			continue
		}
		if candidate.Marker != resultMarker {
			continue
		}
		r := candidate
		rec = &r
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, nil
	}

	return resultFromRecord(rec), nil
}

func resultFromRecord(rec *logRecord) *runtime.Result {
	var info parsedInfo
	if len(rec.Info) > 0 {
		_ = json.Unmarshal(rec.Info, &info)
	}

	finalText, numTurns := digestParts(rec.Parts)

	errName := info.Error.Name
	isError := errName != "" || rec.ExitCode != 0

	stopReason := ""
	if isError {
		stopReason = errName
		if info.Error.Data.Message != "" {
			stopReason = strings.TrimSpace(errName + ": " + info.Error.Data.Message)
		}
	}

	return &runtime.Result{
		SessionID:         rec.SessionID,
		NumTurns:          numTurns,
		TotalCostUSD:      info.Cost,
		FinalText:         finalText,
		IsError:           isError,
		StopReason:        stopReason,
		PermissionDenials: nil,
		Native:            rec.Info,
	}
}

// digestParts joins the assistant's text parts into the final answer (excluding
// reasoning) and counts step-start parts as agentic turns.
func digestParts(raw json.RawMessage) (finalText string, numTurns int) {
	if len(raw) == 0 {
		return "", 0
	}
	var parts []parsedPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", 0
	}
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "text":
			b.WriteString(p.Text)
		case "step-start":
			numTurns++
		}
	}
	return strings.TrimSpace(b.String()), numTurns
}

// ClassifyOutcome maps an opencode Result and the run's Limits to a normalized
// Outcome. Budget uses the real USD cost opencode reports. An empty Status means
// no failure was detected and the caller should proceed (e.g., check for a PR).
func (o *OpencodeRuntime) ClassifyOutcome(r *runtime.Result, limits runtime.Limits) runtime.Outcome {
	if r == nil {
		return runtime.Outcome{}
	}

	if limits.MaxTurns > 0 && r.NumTurns >= limits.MaxTurns {
		return runtime.Outcome{
			Status:        "failed",
			FailureReason: "max_turns",
			FailureDetail: fmt.Sprintf("used %d of %d turns", r.NumTurns, limits.MaxTurns),
		}
	}

	if limits.MaxBudgetUSD > 0 && r.TotalCostUSD >= limits.MaxBudgetUSD*0.95 {
		return runtime.Outcome{
			Status:        "failed",
			FailureReason: "max_budget",
			FailureDetail: fmt.Sprintf("spent $%.2f of $%.2f budget", r.TotalCostUSD, limits.MaxBudgetUSD),
		}
	}

	if r.IsError {
		detail := r.StopReason
		if detail == "" {
			detail = r.FinalText
		}
		if len(detail) > 500 {
			detail = detail[:500] + "..."
		}
		reason := "error"
		if looksLikeUsageLimit(detail) {
			reason = "usage_limit"
		}
		return runtime.Outcome{Status: "failed", FailureReason: reason, FailureDetail: detail}
	}

	return runtime.Outcome{}
}
