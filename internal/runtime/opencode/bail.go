package opencode

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

// bailAssessment is the wire shape the agent emits inside <bail-report> tags.
// Mirrors supervisor.BailAssessment; kept private to the runtime to avoid a
// supervisor dependency. Shared verbatim with the claudecode and codex runtimes.
type bailAssessment struct {
	Reason        string   `json:"reason"`
	FilesExamined []string `json:"files_examined"`
	Plan          string   `json:"plan"`
	SubIssues     []string `json:"sub_issues,omitempty"`
	Complexity    string   `json:"complexity"`
}

// maxLogScanBytes caps the raw-log fallback scan to the tail of the log.
const maxLogScanBytes = 200_000

// ExtractBailReport finds the <bail-report> JSON the agent embeds when it cannot
// complete the work. It checks the parsed final text first (where opencode's
// answer lands), then falls back to scanning the tail of the raw run log (the
// agent may have piped the report into a tool call instead of its final reply).
func (o *OpencodeRuntime) ExtractBailReport(r *runtime.Result, logPath string) *runtime.BailReport {
	if r != nil && r.FinalText != "" {
		if rep := extractBailReportFromText(r.FinalText); rep != nil {
			return rep
		}
	}
	return extractBailReportFromLog(logPath)
}

// extractBailReportFromText finds the last well-formed <bail-report>…</bail-report>
// block in text. Search proceeds from the end so an earlier quoted mention of
// the tag doesn't shadow the real final block.
func extractBailReportFromText(text string) *runtime.BailReport {
	const openTag = "<bail-report>"
	const closeTag = "</bail-report>"

	searchFrom := len(text)
	for {
		end := strings.LastIndex(text[:searchFrom], closeTag)
		if end < 0 {
			return nil
		}
		start := strings.LastIndex(text[:end], openTag)
		if start < 0 {
			return nil
		}
		body := strings.TrimSpace(text[start+len(openTag) : end])
		if rep := tryParseBail(body); rep != nil {
			return rep
		}
		searchFrom = end
	}
}

// extractBailReportFromLog reads the tail of the run log and searches for a
// <bail-report> block. Returns nil if the log can't be read or has no report.
func extractBailReportFromLog(logPath string) *runtime.BailReport {
	if logPath == "" {
		return nil
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return nil
	}
	content := string(data)
	if len(content) > maxLogScanBytes {
		content = content[len(content)-maxLogScanBytes:]
	}
	return extractBailReportFromText(content)
}

// tryParseBail parses a JSON bail report, unescaping up to three levels of
// JSON-string encoding before giving up (the block may be escaped when it rides
// inside a mirrored event's JSON string).
func tryParseBail(s string) *runtime.BailReport {
	current := s
	for range 3 {
		current = strings.TrimSpace(current)
		var a bailAssessment
		if err := json.Unmarshal([]byte(current), &a); err == nil {
			raw := append(json.RawMessage(nil), []byte(current)...)
			return &runtime.BailReport{Reason: a.Reason, Detail: a.Plan, Native: raw}
		}
		unescaped := jsonUnescape(current)
		if unescaped == current {
			return nil
		}
		current = unescaped
	}
	return nil
}

// jsonUnescape applies one level of JSON-string unescaping by wrapping s in
// quotes and asking encoding/json to parse it. Returns s unchanged if the
// result isn't a valid quoted string.
func jsonUnescape(s string) string {
	quoted := `"` + s + `"`
	var out string
	if err := json.Unmarshal([]byte(quoted), &out); err == nil {
		return out
	}
	return s
}
