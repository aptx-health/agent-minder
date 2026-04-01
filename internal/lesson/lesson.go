// Package lesson provides the feedback/learning system for agent-minder.
// It selects relevant lessons, formats them for prompt injection, tracks effectiveness,
// and provides grooming utilities.
package lesson

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aptx-health/agent-minder/internal/claudecli"
	"github.com/aptx-health/agent-minder/internal/db"
)

// MaxPromptTokens is the approximate token budget for lesson injection.
const MaxPromptTokens = 2000

// ApproxTokensPerChar is a rough estimate for token counting.
const ApproxTokensPerChar = 4

// SelectLessons returns relevant lessons for a task, respecting the token budget.
// Selection tiers: pinned first, then repo-scoped, then global.
// Within each tier, sorted by effectiveness ratio descending.
func SelectLessons(store *db.Store, owner, repo string) ([]*db.Lesson, error) {
	scope := owner + "/" + repo
	lessons, err := store.GetActiveLessons(scope)
	if err != nil {
		return nil, err
	}

	// Budget allocation: pinned ~500 tokens, repo-scoped ~1000, global ~500.
	var selected []*db.Lesson
	var totalChars int
	maxChars := MaxPromptTokens * ApproxTokensPerChar

	// Tier 1: Pinned lessons (always included).
	for _, l := range lessons {
		if !l.Pinned {
			continue
		}
		if totalChars+len(l.Content) > maxChars {
			break
		}
		selected = append(selected, l)
		totalChars += len(l.Content)
	}

	// Tier 2: Repo-scoped lessons (sorted by effectiveness).
	for _, l := range lessons {
		if l.Pinned || !l.RepoScope.Valid {
			continue
		}
		if totalChars+len(l.Content) > maxChars {
			break
		}
		selected = append(selected, l)
		totalChars += len(l.Content)
	}

	// Tier 3: Global lessons.
	for _, l := range lessons {
		if l.Pinned || l.RepoScope.Valid {
			continue
		}
		if totalChars+len(l.Content) > maxChars {
			break
		}
		selected = append(selected, l)
		totalChars += len(l.Content)
	}

	return selected, nil
}

// FormatForPrompt renders selected lessons as a markdown section for the system prompt.
func FormatForPrompt(lessons []*db.Lesson) string {
	if len(lessons) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Lessons from Previous Work\n\n")
	b.WriteString("Follow these lessons learned from prior agent runs in this project:\n\n")

	for i, l := range lessons {
		source := ""
		if l.Source == "review" {
			source = " (from review)"
		}
		fmt.Fprintf(&b, "%d. %s%s\n", i+1, l.Content, source)
	}

	b.WriteString("\n")
	return b.String()
}

// RecordInjection tracks which lessons were injected into a job.
func RecordInjection(store *db.Store, jobID int64, lessons []*db.Lesson) error {
	if len(lessons) == 0 {
		return nil
	}

	ids := make([]int64, len(lessons))
	for i, l := range lessons {
		ids[i] = l.ID
	}

	if err := store.IncrementLessonInjected(ids); err != nil {
		return err
	}
	return store.RecordJobLessons(jobID, ids)
}

// RecordOutcome updates lesson effectiveness based on job outcome.
// Call after a job reaches a terminal state (done = helpful, bailed = unhelpful).
func RecordOutcome(store *db.Store, jobID int64, success bool) error {
	return store.UpdateLessonOutcome(jobID, success)
}

// CaptureFromReview extracts lessons from a review agent's findings.
// reviewResult is the structured output from the review agent.
func CaptureFromReview(store *db.Store, owner, repo, reviewResult string) ([]*db.Lesson, error) {
	// Look for patterns in the review that indicate learnable issues.
	// The review agent produces risk assessments and findings — extract actionable patterns.
	patterns := extractPatterns(reviewResult)
	if len(patterns) == 0 {
		return nil, nil
	}

	var created []*db.Lesson
	for _, pattern := range patterns {
		// Check for duplicates.
		existing, err := store.GetActiveLessons(owner + "/" + repo)
		if err != nil {
			continue
		}
		if IsDuplicate(pattern, existing) {
			continue
		}

		l := &db.Lesson{
			RepoScope: sql.NullString{String: owner + "/" + repo, Valid: true},
			Content:   pattern,
			Source:    "review",
			Active:    true,
		}
		if err := store.CreateLesson(l); err != nil {
			continue
		}
		created = append(created, l)
	}

	return created, nil
}

// extractPatterns pulls actionable patterns from review text.
func extractPatterns(reviewResult string) []string {
	var patterns []string

	// Look for lines that start with common review patterns.
	lines := strings.Split(reviewResult, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)

		// Pattern indicators.
		if strings.HasPrefix(lower, "- fix:") ||
			strings.HasPrefix(lower, "- issue:") ||
			strings.HasPrefix(lower, "- missing:") ||
			strings.HasPrefix(lower, "- always ") ||
			strings.HasPrefix(lower, "- never ") ||
			strings.HasPrefix(lower, "- ensure ") ||
			strings.Contains(lower, "should always") ||
			strings.Contains(lower, "should never") ||
			strings.Contains(lower, "must ") {

			// Clean up the pattern.
			cleaned := strings.TrimPrefix(line, "- ")
			cleaned = strings.TrimPrefix(cleaned, "Fix: ")
			cleaned = strings.TrimPrefix(cleaned, "Issue: ")
			cleaned = strings.TrimPrefix(cleaned, "Missing: ")
			if len(cleaned) > 10 && len(cleaned) < 500 {
				patterns = append(patterns, cleaned)
			}
		}
	}

	return patterns
}

// IsDuplicate checks if a pattern is semantically similar to existing lessons.
func IsDuplicate(pattern string, existing []*db.Lesson) bool {
	patternLower := strings.ToLower(pattern)
	patternWords := strings.Fields(patternLower)

	for _, l := range existing {
		existingLower := strings.ToLower(l.Content)
		// Simple keyword overlap check.
		matches := 0
		for _, word := range patternWords {
			if len(word) > 3 && strings.Contains(existingLower, word) {
				matches++
			}
		}
		// If >60% of significant words match, it's a duplicate.
		significant := 0
		for _, w := range patternWords {
			if len(w) > 3 {
				significant++
			}
		}
		if significant > 0 && float64(matches)/float64(significant) > 0.6 {
			return true
		}
	}
	return false
}

// --- Grooming ---

// GroomResult holds the outcome of a grooming pass.
type GroomResult struct {
	Deactivated int
	Merged      int
	Flagged     int
}

// GroomStale deactivates lessons that haven't been injected in the given duration.
func GroomStale(store *db.Store, staleDuration time.Duration) (int, error) {
	stale, err := store.StaleLessons(staleDuration)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, l := range stale {
		if err := store.UpdateLessonActive(l.ID, false); err == nil {
			count++
		}
	}
	return count, nil
}

// GroomIneffective deactivates lessons that have been unhelpful more often than helpful.
func GroomIneffective(store *db.Store, minInjections int) (int, error) {
	ineffective, err := store.IneffectiveLessons(minInjections)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, l := range ineffective {
		if err := store.UpdateLessonActive(l.ID, false); err == nil {
			count++
		}
	}
	return count, nil
}

// GroomWithLLM runs an LLM-assisted consolidation pass over active lessons.
// It merges duplicates, resolves contradictions, and updates wording.
func GroomWithLLM(ctx context.Context, store *db.Store, completer claudecli.Completer, dryRun bool) (*GroomResult, error) {
	lessons, err := store.GetAllLessons("", false)
	if err != nil {
		return nil, err
	}

	if len(lessons) < 2 {
		return &GroomResult{}, nil
	}

	// Build lesson list for the LLM.
	var lessonList []string
	for _, l := range lessons {
		scope := "global"
		if l.RepoScope.Valid {
			scope = l.RepoScope.String
		}
		lessonList = append(lessonList, fmt.Sprintf("ID:%d [%s] (inj:%d +%d -%d) %s",
			l.ID, scope, l.TimesInjected, l.TimesHelpful, l.TimesUnhelpful, l.Content))
	}

	prompt := fmt.Sprintf(`Review these %d active lessons for an AI agent orchestration system.
Identify:
1. Duplicates that should be merged (keep the better-worded one, deactivate the other)
2. Contradictions that should be resolved (supersede the older/less effective one)
3. Lessons that are too vague to be useful (flag for removal)

Lessons:
%s

Respond with JSON:
{
  "actions": [
    {"type": "merge", "keep_id": 1, "remove_id": 2, "reason": "..."},
    {"type": "deactivate", "id": 3, "reason": "too vague"},
    {"type": "reword", "id": 4, "new_content": "...", "reason": "..."}
  ]
}`, len(lessons), strings.Join(lessonList, "\n"))

	schema := `{
		"type": "object",
		"properties": {
			"actions": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"type": {"type": "string"},
						"id": {"type": "integer"},
						"keep_id": {"type": "integer"},
						"remove_id": {"type": "integer"},
						"new_content": {"type": "string"},
						"reason": {"type": "string"}
					},
					"required": ["type", "reason"]
				}
			}
		},
		"required": ["actions"]
	}`

	resp, err := completer.Complete(ctx, &claudecli.Request{
		SystemPrompt: "You are a lesson grooming assistant. Analyze lessons for duplicates, contradictions, and quality.",
		Prompt:       prompt,
		Model:        "haiku",
		JSONSchema:   schema,
		DisableTools: true,
	})
	if err != nil {
		return nil, fmt.Errorf("LLM groom: %w", err)
	}

	var result struct {
		Actions []struct {
			Type       string `json:"type"`
			ID         int64  `json:"id"`
			KeepID     int64  `json:"keep_id"`
			RemoveID   int64  `json:"remove_id"`
			NewContent string `json:"new_content"`
			Reason     string `json:"reason"`
		} `json:"actions"`
	}

	if err := json.Unmarshal([]byte(resp.Content()), &result); err != nil {
		return nil, fmt.Errorf("parse groom response: %w", err)
	}

	gr := &GroomResult{}
	for _, action := range result.Actions {
		if dryRun {
			fmt.Printf("[dry-run] %s: %s\n", action.Type, action.Reason)
			gr.Flagged++
			continue
		}

		switch action.Type {
		case "merge":
			if action.KeepID > 0 && action.RemoveID > 0 {
				_ = store.SupersedeLesson(action.RemoveID, action.KeepID)
				gr.Merged++
			}
		case "deactivate":
			if action.ID > 0 {
				_ = store.UpdateLessonActive(action.ID, false)
				gr.Deactivated++
			}
		case "reword":
			if action.ID > 0 && action.NewContent != "" {
				_ = store.UpdateLessonContent(action.ID, action.NewContent)
			}
		}
	}

	return gr, nil
}
