package claudecode

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

// bailAssessment is the wire shape the agent emits inside <bail-report> tags.
// Mirrors `supervisor.BailAssessment`; kept private to the runtime to avoid a
// supervisor dependency.
type bailAssessment struct {
	Reason        string   `json:"reason"`
	FilesExamined []string `json:"files_examined"`
	Plan          string   `json:"plan"`
	SubIssues     []string `json:"sub_issues,omitempty"`
	Complexity    string   `json:"complexity"`
}

// maxLogScanBytes caps the raw-log fallback scan to the tail of the log to
// avoid quadratic strings.LastIndex work on multi-megabyte logs.
const maxLogScanBytes = 200_000

// extractBailReportFromText finds the last well-formed <bail-report>…</bail-report>
// JSON block in text and returns it as a runtime.BailReport.
//
// Search proceeds from the end of the string so a backtick-quoted
// `<bail-report>` mentioned earlier in the message doesn't shadow the real
// final-message block.
//
// Handles up to three levels of JSON-string escaping — bail reports embedded in
// stream-json `assistant.text` blocks can be escaped multiple times depending
// on how the agent emitted them (direct text vs. tool-use argument).
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

// extractBailReportFromLog reads the tail of a stream-json log file and
// searches for a <bail-report> block. Used as a fallback when the report did
// not appear in the agent's final text (e.g., the agent piped the report into
// `gh issue comment --body-file` via a tool call).
//
// Returns nil if the log can't be read or contains no valid report.
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

// tryParseBail attempts to parse a JSON bail report, unescaping up to three
// levels of JSON-string encoding before giving up.
func tryParseBail(s string) *runtime.BailReport {
	current := s
	for range 3 {
		current = strings.TrimSpace(current)
		var a bailAssessment
		if err := json.Unmarshal([]byte(current), &a); err == nil {
			raw := append(json.RawMessage(nil), []byte(current)...)
			return &runtime.BailReport{
				Reason: a.Reason,
				Detail: a.Plan,
				Native: raw,
			}
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
