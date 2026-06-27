package codex

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/aptx-health/agent-minder/internal/runtime"
)

type bailAssessment struct {
	Reason        string   `json:"reason"`
	FilesExamined []string `json:"files_examined"`
	Plan          string   `json:"plan"`
	SubIssues     []string `json:"sub_issues,omitempty"`
	Complexity    string   `json:"complexity"`
}

const maxLogScanBytes = 200_000

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

func jsonUnescape(s string) string {
	quoted := `"` + s + `"`
	var out string
	if err := json.Unmarshal([]byte(quoted), &out); err == nil {
		return out
	}
	return s
}
