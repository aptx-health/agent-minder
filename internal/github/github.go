// Package github provides a client for fetching issue and PR status from GitHub.
package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v72/github"
)

// ItemStatus holds the fetched status of a GitHub issue or PR.
type ItemStatus struct {
	Number   int
	Title    string
	State    string // "open", "closed", "merged"
	Labels   []string
	ItemType string // "issue" or "pull_request"
}

// CompactStatus returns a short TUI-friendly status tag.
func (s *ItemStatus) CompactStatus() string {
	switch {
	case s.State == "merged":
		return "Mrgd"
	case s.State == "closed":
		return "Closd"
	case hasLabel(s.Labels, "blocked"):
		return "Blckd"
	case hasLabel(s.Labels, "in progress"), hasLabel(s.Labels, "in-progress"), hasLabel(s.Labels, "wip"):
		return "InProg"
	default:
		return "Open"
	}
}

// Client wraps the GitHub API client.
type Client struct {
	gh *github.Client
}

// NewClient creates a GitHub client authenticated with the given PAT.
func NewClient(token string) *Client {
	return &Client{
		gh: github.NewClient(nil).WithAuthToken(token),
	}
}

// FetchItem fetches the current status of an issue or PR.
// It first tries as a PR (to detect merged state), then falls back to issue.
func (c *Client) FetchItem(ctx context.Context, owner, repo string, number int) (*ItemStatus, error) {
	return c.FetchItemWithHint(ctx, owner, repo, number, "")
}

// FetchItemWithHint fetches status, using itemType hint to skip unnecessary API calls.
// If hint is "issue", skips the PR attempt. If hint is "pull_request", only tries PR.
// If hint is empty, tries PR first then falls back to issue.
func (c *Client) FetchItemWithHint(ctx context.Context, owner, repo string, number int, itemType string) (*ItemStatus, error) {
	if itemType != "issue" {
		// Try as pull request.
		pr, _, err := c.gh.PullRequests.Get(ctx, owner, repo, number)
		if err == nil {
			status := &ItemStatus{
				Number:   number,
				Title:    pr.GetTitle(),
				ItemType: "pull_request",
				Labels:   extractLabels(pr.Labels),
			}
			if pr.GetMerged() {
				status.State = "merged"
			} else if pr.GetState() == "closed" {
				status.State = "closed"
			} else {
				status.State = "open"
			}
			return status, nil
		}
		// If hint was specifically pull_request and it failed, return the error.
		if itemType == "pull_request" {
			return nil, fmt.Errorf("fetch PR %s/%s#%d: %w", owner, repo, number, err)
		}
	}

	// Fetch as issue.
	issue, _, err := c.gh.Issues.Get(ctx, owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("fetch %s/%s#%d: %w", owner, repo, number, err)
	}

	return &ItemStatus{
		Number:   number,
		Title:    issue.GetTitle(),
		State:    issue.GetState(),
		ItemType: "issue",
		Labels:   extractLabels(issue.Labels),
	}, nil
}

// ItemContent holds the body and recent comments for a GitHub issue or PR.
type ItemContent struct {
	Body     string
	Comments []string
}

// FetchItemContent fetches the body and last 10 comments for an issue or PR.
// Uses the Issues API which works for both issues and PRs.
func (c *Client) FetchItemContent(ctx context.Context, owner, repo string, number int, itemType string) (*ItemContent, error) {
	// Get the issue/PR body.
	issue, _, err := c.gh.Issues.Get(ctx, owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("fetch body %s/%s#%d: %w", owner, repo, number, err)
	}

	content := &ItemContent{
		Body: issue.GetBody(),
	}

	// Get last 10 comments (newest first, then reverse to chronological).
	opts := &github.IssueListCommentsOptions{
		Sort:      github.String("created"),
		Direction: github.String("desc"),
		ListOptions: github.ListOptions{
			PerPage: 10,
		},
	}
	comments, _, err := c.gh.Issues.ListComments(ctx, owner, repo, number, opts)
	if err != nil {
		// Non-fatal: return what we have.
		return content, nil
	}

	// Reverse to chronological order.
	for i, j := 0, len(comments)-1; i < j; i, j = i+1, j-1 {
		comments[i], comments[j] = comments[j], comments[i]
	}

	content.Comments = make([]string, 0, len(comments))
	for _, c := range comments {
		content.Comments = append(content.Comments, c.GetBody())
	}

	return content, nil
}

func extractLabels(labels []*github.Label) []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if l.Name != nil {
			out = append(out, *l.Name)
		}
	}
	return out
}

func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, name) {
			return true
		}
	}
	return false
}
