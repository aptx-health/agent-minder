package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aptx-health/agent-minder/internal/claudecli"
	"github.com/aptx-health/agent-minder/internal/db"
)

// ReviewAssessment is the structured JSON output from the review extraction call.
type ReviewAssessment struct {
	Risk           string           `json:"risk"`            // "low-risk", "needs-testing", or "suspect"
	Summary        string           `json:"summary"`         // One-line summary of the review
	Lessons        []string         `json:"lessons"`         // Actionable lessons for future agents
	Issues         []string         `json:"issues"`          // Specific issues found (empty if clean)
	LessonFeedback []LessonFeedback `json:"lesson_feedback"` // Per-lesson effectiveness feedback
}

// LessonFeedback is per-lesson feedback from the reviewer.
// The reviewer reports whether each injected lesson was relevant/helpful for this task.
type LessonFeedback struct {
	ID      int64  `json:"id"`      // Lesson ID
	Helpful bool   `json:"helpful"` // Was this lesson relevant and helpful?
	Reason  string `json:"reason"`  // Brief explanation
}

var reviewAssessmentSchema = `{
	"type": "object",
	"properties": {
		"risk": {
			"type": "string",
			"enum": ["low-risk", "needs-testing", "suspect"],
			"description": "Overall risk assessment of the PR"
		},
		"summary": {
			"type": "string",
			"description": "One-line summary of the review findings"
		},
		"lessons": {
			"type": "array",
			"items": {"type": "string"},
			"description": "Actionable lessons that future agents should follow to avoid the issues found. Each lesson should be a clear, imperative statement. Empty array if no lessons."
		},
		"issues": {
			"type": "array",
			"items": {"type": "string"},
			"description": "Specific issues found in the PR. Empty array if the PR is clean."
		},
		"lesson_feedback": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"id": {"type": "integer", "description": "The lesson ID number"},
					"helpful": {"type": "boolean", "description": "Was this lesson relevant and helpful for this task?"},
					"reason": {"type": "string", "description": "Brief explanation of why this lesson was or was not helpful"}
				},
				"required": ["id", "helpful"]
			},
			"description": "Feedback on each injected lesson. Reference lessons by their ID number. Empty array if no lessons were injected."
		}
	},
	"required": ["risk", "summary", "lessons", "issues", "lesson_feedback"]
}`

// extractReviewAssessment runs a cheap structured LLM call to produce a JSON assessment
// from the review agent's log output.
func (s *Supervisor) extractReviewAssessment(ctx context.Context, job *db.Job, logPath string) ReviewAssessment {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ReviewAssessment{Risk: "needs-testing"}
	}

	content := string(data)
	if idx := strings.LastIndex(content, "--- REVIEW AGENT ---"); idx >= 0 {
		content = content[idx:]
	}
	if len(content) > 8000 {
		content = content[len(content)-8000:]
	}

	prompt := fmt.Sprintf(`Analyze this review agent log for PR #%d (issue #%d) and produce a structured assessment.

The review agent examined the PR, possibly ran tests, and may have made fixes.
Extract the risk level, a one-line summary, any actionable lessons for future agents,
and specific issues found.

Review agent log:
---
%s
---

Produce your assessment as JSON.`, job.PRNumber.Int64, job.IssueNumber, content)

	// Use a 2-minute timeout for the Haiku extraction call.
	// Without this, a hung claude -p (e.g., from a usage limit) blocks the
	// entire review stage goroutine indefinitely — the job never completes.
	extractCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	completer := claudecli.NewCLICompleter()
	resp, err := completer.Complete(extractCtx, &claudecli.Request{
		SystemPrompt: "You extract structured review assessments from agent logs. Be concise and actionable.",
		Prompt:       prompt,
		Model:        "haiku",
		JSONSchema:   reviewAssessmentSchema,
		DisableTools: true,
	})
	if err != nil {
		debugLog("review assessment extraction failed", "issue", job.IssueNumber, "error", err.Error())
		return ReviewAssessment{Risk: "needs-testing"}
	}

	var assessment ReviewAssessment
	if err := json.Unmarshal([]byte(resp.Content()), &assessment); err != nil {
		debugLog("review assessment parse failed", "issue", job.IssueNumber, "error", err.Error())
		return ReviewAssessment{Risk: "needs-testing"}
	}

	debugLog("review assessment extracted",
		"issue", job.IssueNumber,
		"risk", assessment.Risk,
		"lessons", len(assessment.Lessons),
		"issues", len(assessment.Issues),
	)
	return assessment
}
