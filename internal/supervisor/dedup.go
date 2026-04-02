package supervisor

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	gitpkg "github.com/aptx-health/agent-minder/internal/git"
)

// DedupResult holds the outcome of a dedup evaluation.
type DedupResult struct {
	Skip   bool   // true if the job should be skipped
	Reason string // human-readable reason for skipping
}

// EvaluateDedup checks all dedup strategies for a job.
// Strategies are stackable — if ANY strategy triggers, the job is skipped.
func EvaluateDedup(ctx context.Context, sc *SlotContext, strategies []string) DedupResult {
	for _, strategy := range strategies {
		result := evaluateStrategy(ctx, sc, strategy)
		if result.Skip {
			return result
		}
	}
	return DedupResult{}
}

func evaluateStrategy(ctx context.Context, sc *SlotContext, strategy string) DedupResult {
	// Parse strategy: "branch_exists", "open_pr_with_label:<label>", "recent_run:<hours>"
	switch {
	case strategy == "branch_exists":
		return dedupBranchExists(sc)
	case strings.HasPrefix(strategy, "open_pr_with_label:"):
		label := strings.TrimPrefix(strategy, "open_pr_with_label:")
		return dedupOpenPRWithLabel(ctx, sc, label)
	case strings.HasPrefix(strategy, "recent_run:"):
		hoursStr := strings.TrimPrefix(strategy, "recent_run:")
		hours, err := strconv.Atoi(hoursStr)
		if err != nil || hours < 1 {
			return DedupResult{} // invalid strategy, don't skip
		}
		return dedupRecentRun(sc, hours)
	default:
		debugLog("unknown dedup strategy", "strategy", strategy)
		return DedupResult{} // unknown strategy, don't skip
	}
}

// dedupBranchExists skips if the job's branch already exists on the remote.
func dedupBranchExists(sc *SlotContext) DedupResult {
	branch := sc.Branch
	if branch == "" {
		return DedupResult{}
	}

	branches, err := gitpkg.Branches(sc.RepoDir)
	if err != nil {
		return DedupResult{}
	}

	for _, b := range branches {
		if b.IsRemote && (b.Name == "origin/"+branch || b.Name == branch) {
			return DedupResult{
				Skip:   true,
				Reason: fmt.Sprintf("branch %q already exists on remote", branch),
			}
		}
	}
	return DedupResult{}
}

// dedupOpenPRWithLabel skips if there's an open PR with the given label.
func dedupOpenPRWithLabel(ctx context.Context, sc *SlotContext, label string) DedupResult {
	ghClient := sc.NewGHClient()
	// Search for open PRs with the label using GitHub search API.
	// SearchIssues uses "is:issue" so we search PRs by listing with label filter.
	prs, err := ghClient.ListIssuesByLabel(ctx, sc.Owner, sc.Repo, label)
	if err != nil {
		return DedupResult{}
	}

	for _, item := range prs.Items {
		if item.ItemType == "pull_request" && item.State == "open" {
			return DedupResult{
				Skip:   true,
				Reason: fmt.Sprintf("open PR #%d already has label %q", item.Number, label),
			}
		}
	}
	return DedupResult{}
}

// dedupRecentRun skips if a job with the same agent ran within the last N hours.
func dedupRecentRun(sc *SlotContext, hours int) DedupResult {
	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)

	// Check completed jobs with the same agent in this deployment.
	jobs, err := sc.Store.GetJobs(sc.Deploy.ID)
	if err != nil {
		return DedupResult{}
	}

	for _, j := range jobs {
		if j.Agent != sc.Job.Agent {
			continue
		}
		if j.ID == sc.Job.ID {
			continue // don't match self
		}
		// Check if it completed recently.
		if j.CompletedAt.Valid && j.CompletedAt.Time.After(cutoff) {
			return DedupResult{
				Skip: true,
				Reason: fmt.Sprintf("agent %q ran %s ago (within %dh window)", j.Agent,
					time.Since(j.CompletedAt.Time).Truncate(time.Minute), hours),
			}
		}
		// Also check if it started recently (still running).
		if j.StartedAt.Valid && j.StartedAt.Time.After(cutoff) &&
			(j.Status == "running" || j.Status == "queued" || j.Status == "reviewing") {
			return DedupResult{
				Skip: true,
				Reason: fmt.Sprintf("agent %q is already active (started %s ago)", j.Agent,
					time.Since(j.StartedAt.Time).Truncate(time.Minute)),
			}
		}
	}

	return DedupResult{}
}
