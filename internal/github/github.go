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
	// Try as pull request first.
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

	// Fall back to issue.
	issue, _, err := c.gh.Issues.Get(ctx, owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("fetch %s/%s#%d: %w", owner, repo, number, err)
	}

	status := &ItemStatus{
		Number:   number,
		Title:    issue.GetTitle(),
		State:    issue.GetState(),
		ItemType: "issue",
		Labels:   extractIssueLabels(issue.Labels),
	}
	// GitHub returns "open" or "closed" for issues.
	return status, nil
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

func extractIssueLabels(labels []*github.Label) []string {
	return extractLabels(labels)
}

func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, name) {
			return true
		}
	}
	return false
}
