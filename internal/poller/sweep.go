package poller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dustinlange/agent-minder/internal/db"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
	"github.com/dustinlange/agent-minder/internal/llm"
)

// SweepResult holds the outcome of sweeping a single tracked item.
type SweepResult struct {
	Item      *db.TrackedItem
	Changed   bool // status changed (metadata)
	OldStatus string
	NewStatus string
	HaikuRan  bool
	Error     error
}

// ItemSweepResponse is the structured JSON from the Haiku item summarizer.
type ItemSweepResponse struct {
	Objective string `json:"objective"`
	Progress  string `json:"progress"`
}

// computeContentHash returns a SHA-256 hex digest of the normalized content.
// Labels are sorted to ensure order-independence.
// relatedCommits are included so new commits referencing the item invalidate the cache.
func computeContentHash(state string, labels string, body string, comments []string, relatedCommits []string, isDraft bool, reviewState string) string {
	h := sha256.New()

	h.Write([]byte("state:"))
	h.Write([]byte(state))
	h.Write([]byte("\n"))

	if isDraft {
		h.Write([]byte("draft:true\n"))
	}
	if reviewState != "" {
		h.Write([]byte("review:"))
		h.Write([]byte(reviewState))
		h.Write([]byte("\n"))
	}

	// Sort labels for order-independence.
	labelList := strings.Split(labels, ",")
	sort.Strings(labelList)
	h.Write([]byte("labels:"))
	h.Write([]byte(strings.Join(labelList, ",")))
	h.Write([]byte("\n"))

	h.Write([]byte("body:"))
	h.Write([]byte(body))
	h.Write([]byte("\n"))

	for _, c := range comments {
		h.Write([]byte("comment:"))
		h.Write([]byte(c))
		h.Write([]byte("\n"))
	}

	for _, rc := range relatedCommits {
		h.Write([]byte("commit:"))
		h.Write([]byte(rc))
		h.Write([]byte("\n"))
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

func itemSweepSystemPrompt() string {
	return `You are a concise issue/PR summarizer. Given a GitHub issue or PR with its body, recent comments, and related git commits, produce two brief summaries.

Respond with a JSON object (no markdown fences):
{
  "objective": "1-2 sentence summary of what this issue/PR aims to accomplish",
  "progress": "1-2 sentence summary of current progress, blockers, or recent activity"
}

Rules:
- Be terse and factual
- "objective" should capture the goal/purpose regardless of current state
- "progress" should reflect the latest status from comments, state, AND related git commits
- Related git commits are the strongest signal of implementation progress — if commits exist, work is actively happening
- If there are no comments or commits, base progress on the issue body and state alone`
}

func buildItemSweepPrompt(item *db.TrackedItem, content *ghpkg.ItemContent, relatedCommits []gitpkg.LogEntry) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## %s %s/%s#%d: %s\n\n", item.ItemType, item.Owner, item.Repo, item.Number, item.Title)
	fmt.Fprintf(&b, "**State:** %s\n", item.State)
	if item.ItemType == "pull_request" {
		if item.IsDraft {
			b.WriteString("**Draft:** yes\n")
		}
		if item.ReviewState != "" {
			fmt.Fprintf(&b, "**Review:** %s\n", item.ReviewState)
		}
	}
	if item.Labels != "" {
		fmt.Fprintf(&b, "**Labels:** %s\n", item.Labels)
	}

	if content.Body != "" {
		body := content.Body
		if len(body) > 2000 {
			body = body[:2000] + "...[truncated]"
		}
		fmt.Fprintf(&b, "\n### Body\n%s\n", body)
	}

	if len(content.Comments) > 0 {
		b.WriteString("\n### Recent Comments (chronological)\n")
		for i, c := range content.Comments {
			comment := c
			if len(comment) > 500 {
				comment = comment[:500] + "...[truncated]"
			}
			fmt.Fprintf(&b, "\n**Comment %d:**\n%s\n", i+1, comment)
		}
	}

	if len(relatedCommits) > 0 {
		b.WriteString("\n### Related Git Commits (newest first)\n")
		for _, rc := range relatedCommits {
			fmt.Fprintf(&b, "- %s: %s (%s, %s)\n", rc.Hash, rc.Subject, rc.Author, rc.Date.Format("2006-01-02"))
		}
	}

	return b.String()
}

// parseItemSweep parses the Haiku summarizer response.
func parseItemSweep(raw string) *ItemSweepResponse {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	// Try to extract JSON from markdown code fences.
	cleaned := raw
	if idx := strings.Index(raw, "```json"); idx >= 0 {
		start := idx + len("```json")
		if end := strings.Index(raw[start:], "```"); end >= 0 {
			cleaned = strings.TrimSpace(raw[start : start+end])
		}
	} else if idx := strings.Index(raw, "```"); idx >= 0 {
		start := idx + len("```")
		if end := strings.Index(raw[start:], "```"); end >= 0 {
			cleaned = strings.TrimSpace(raw[start : start+end])
		}
	}

	var resp ItemSweepResponse
	if err := json.Unmarshal([]byte(cleaned), &resp); err == nil && (resp.Objective != "" || resp.Progress != "") {
		return &resp
	}

	// Try raw as JSON directly.
	if cleaned != raw {
		if err := json.Unmarshal([]byte(raw), &resp); err == nil && (resp.Objective != "" || resp.Progress != "") {
			return &resp
		}
	}

	// Plain text fallback: use entire text as progress.
	return &ItemSweepResponse{Progress: raw}
}

// sweepTrackedItems runs the Haiku pre-sweep across all tracked items with bounded concurrency.
// repos are used to scan git history for commits referencing each item.
// Returns sweep results and a summary string for the tier 1 prompt.
func (p *Poller) sweepTrackedItems(ctx context.Context, items []db.TrackedItem, gh *ghpkg.Client, repos []db.Repo) ([]SweepResult, string) {
	if len(items) == 0 {
		return nil, ""
	}

	haikuModel := p.project.LLMSummarizerModel
	if haikuModel == "" {
		haikuModel = p.project.LLMModel
	}

	results := make([]SweepResult, len(items))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5) // bounded concurrency

	for i := range items {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release
			results[idx] = p.sweepOneItem(ctx, &items[idx], gh, haikuModel, repos)
		}(i)
	}
	wg.Wait()

	// Build tracked summary from results.
	var trackedSummary strings.Builder
	for _, r := range results {
		if r.Changed {
			fmt.Fprintf(&trackedSummary, "- %s: %s → %s (%s)\n", r.Item.DisplayRef(), r.OldStatus, r.NewStatus, r.Item.Title)
		}
	}

	// Prune old completed items beyond the TTL (default 14 days).
	ttl := p.project.MessageTTLSec
	if ttl <= 0 {
		ttl = 14 * 24 * 3600 // 14 days default
	}
	prunedCompleted, err := p.store.PruneCompletedItems(p.project.ID, ttl)
	if err != nil {
		p.emit("error", fmt.Sprintf("pruning completed items: %v", err), nil)
	} else if prunedCompleted > 0 {
		p.emit("tracked", fmt.Sprintf("Pruned %d expired completed items", prunedCompleted), nil)
	}

	return results, trackedSummary.String()
}

// sweepOneItem handles a single tracked item: fetch metadata, fetch content, git cross-ref, hash check, optional Haiku call.
func (p *Poller) sweepOneItem(ctx context.Context, item *db.TrackedItem, gh *ghpkg.Client, haikuModel string, repos []db.Repo) SweepResult {
	result := SweepResult{Item: item, OldStatus: item.LastStatus}

	// Step 1: Fetch metadata (status, title, labels).
	status, err := gh.FetchItemWithHint(ctx, item.Owner, item.Repo, item.Number, item.ItemType)
	if err != nil {
		result.Error = fmt.Errorf("fetch metadata %s: %w", item.DisplayRef(), err)
		p.emit("error", fmt.Sprintf("checking %s: %v", item.DisplayRef(), err), nil)
		return result
	}

	// Update metadata.
	item.Title = status.Title
	item.State = status.State
	item.Labels = strings.Join(status.Labels, ",")
	item.IsDraft = status.Draft
	item.LastCheckedAt = time.Now().UTC().Format(time.RFC3339)
	item.ItemType = status.ItemType

	// Step 1b: Fetch review state for PRs (one extra API call).
	if status.ItemType == "pull_request" && status.State == "open" {
		item.ReviewState = gh.FetchPRReviewState(ctx, item.Owner, item.Repo, item.Number)
		status.ReviewState = item.ReviewState
	} else {
		item.ReviewState = ""
	}

	item.LastStatus = status.CompactStatus()
	result.NewStatus = item.LastStatus
	result.Changed = result.OldStatus != result.NewStatus

	// Step 2: Fetch content (body + comments).
	content, err := gh.FetchItemContent(ctx, item.Owner, item.Repo, item.Number, item.ItemType)
	if err != nil {
		// Content fetch failed — update metadata only, skip hash/Haiku.
		result.Error = fmt.Errorf("fetch content %s: %w", item.DisplayRef(), err)
		p.emit("error", fmt.Sprintf("fetching content for %s: %v", item.DisplayRef(), err), nil)
		if dbErr := p.store.UpdateTrackedItem(item); dbErr != nil {
			p.emit("error", fmt.Sprintf("updating %s: %v", item.DisplayRef(), dbErr), nil)
		}
		return result
	}

	// Step 2b: Scan enrolled repos for commits referencing this item (e.g., "#3").
	pattern := fmt.Sprintf("#%d", item.Number)
	var relatedCommits []gitpkg.LogEntry
	var commitHashes []string
	for _, repo := range repos {
		entries, err := gitpkg.LogGrep(repo.Path, pattern)
		if err != nil || len(entries) == 0 {
			continue
		}
		// Cap at 10 most recent per repo.
		if len(entries) > 10 {
			entries = entries[:10]
		}
		relatedCommits = append(relatedCommits, entries...)
		for _, e := range entries {
			commitHashes = append(commitHashes, e.Hash+":"+e.Subject)
		}
	}

	// Step 3: Compute content hash (includes related commits so new commits invalidate cache).
	newHash := computeContentHash(item.State, item.Labels, content.Body, content.Comments, commitHashes, item.IsDraft, item.ReviewState)

	// Step 4: Hash comparison — only run Haiku if content changed or no cached hash.
	if newHash != item.ContentHash || item.ContentHash == "" {
		// Run Haiku summarizer.
		prompt := buildItemSweepPrompt(item, content, relatedCommits)
		sys := itemSweepSystemPrompt()
		debugLog("llm call", "stage", "sweep", "step", "input", "component", "sweep_haiku", "model", haikuModel, "item", item.DisplayRef(),
			"system_prompt", sys, "user_prompt", prompt,
			"est_system_tokens", estimateTokens(sys), "est_user_tokens", estimateTokens(prompt))
		resp, err := p.summarizerProvider.Complete(ctx, &llm.Request{
			Model:     haikuModel,
			System:    sys,
			Messages:  []llm.Message{{Role: "user", Content: prompt}},
			MaxTokens: 256,
		})
		if err != nil {
			// Haiku failed — still update hash to prevent retry loop on same content.
			result.Error = fmt.Errorf("haiku sweep %s: %w", item.DisplayRef(), err)
			p.emit("error", fmt.Sprintf("haiku sweep for %s: %v", item.DisplayRef(), err), nil)
			item.ContentHash = newHash
		} else {
			debugLog("llm response", "stage", "sweep", "step", "output", "component", "sweep_haiku", "item", item.DisplayRef(), "response", resp.Content, "input_tokens", resp.InputToks, "output_tokens", resp.OutputToks)
			result.HaikuRan = true
			parsed := parseItemSweep(resp.Content)
			if parsed != nil {
				item.ObjectiveSummary = parsed.Objective
				item.ProgressSummary = parsed.Progress
			}
			item.ContentHash = newHash
		}
	} else {
		debugLog("cache hit", "stage", "sweep", "step", "skip", "item", item.DisplayRef())
	}
	// If hash matches, keep cached summaries — only metadata updated.

	// Step 5: Persist.
	if dbErr := p.store.UpdateTrackedItem(item); dbErr != nil {
		p.emit("error", fmt.Sprintf("updating %s: %v", item.DisplayRef(), dbErr), nil)
		if result.Error == nil {
			result.Error = dbErr
		}
	}

	return result
}
