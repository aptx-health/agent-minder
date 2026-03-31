// Package github provides a client for fetching issue and PR status from GitHub.
package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v72/github"
)

// ItemStatus holds the fetched status of a GitHub issue or PR.
type ItemStatus struct {
	Number      int
	Title       string
	Body        string // issue/PR body text
	State       string // "open", "closed", "merged"
	Labels      []string
	ItemType    string // "issue" or "pull_request"
	Draft       bool   // PR only: true if draft PR
	ReviewState string // PR only: "approved", "changes_requested", "pending", or ""
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
	case s.Draft:
		return "Draft"
	case s.ReviewState == "approved":
		return "Appvd"
	case s.ReviewState == "changes_requested":
		return "ChReq"
	case hasLabel(s.Labels, "needs-review"), hasLabel(s.Labels, "needs review"):
		return "Revew"
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
// Uses a 30-second HTTP timeout to prevent indefinite hangs on network issues.
// Wraps the transport with ETag caching so conditional requests (304 Not Modified)
// don't count against the GitHub API rate limit.
func NewClient(token string) *Client {
	httpClient := &http.Client{
		Timeout:   30 * time.Second,
		Transport: newETagTransport(http.DefaultTransport),
	}
	return &Client{
		gh: github.NewClient(httpClient).WithAuthToken(token),
	}
}

// NewClientWithBaseURL creates a GitHub client pointing at a custom API base URL.
// Used for testing with httptest servers.
func NewClientWithBaseURL(token, baseURL string) *Client {
	c := github.NewClient(nil).WithAuthToken(token)
	c.BaseURL, _ = c.BaseURL.Parse(baseURL)
	return &Client{gh: c}
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
				Body:     pr.GetBody(),
				ItemType: "pull_request",
				Labels:   extractLabels(pr.Labels),
				Draft:    pr.GetDraft(),
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
		Body:     issue.GetBody(),
		State:    issue.GetState(),
		ItemType: "issue",
		Labels:   extractLabels(issue.Labels),
	}, nil
}

// FetchPRReviewState returns the aggregate review state for a pull request.
// It examines the most recent review from each reviewer and returns:
// "approved" if at least one approval and no outstanding changes_requested,
// "changes_requested" if any reviewer has requested changes,
// "pending" if there are no decisive reviews yet, or "" on error.
func (c *Client) FetchPRReviewState(ctx context.Context, owner, repo string, number int) string {
	reviews, _, err := c.gh.PullRequests.ListReviews(ctx, owner, repo, number, &github.ListOptions{PerPage: 30})
	if err != nil || len(reviews) == 0 {
		return ""
	}

	// Keep only the latest review per user.
	latest := make(map[int64]*github.PullRequestReview)
	for _, r := range reviews {
		uid := r.GetUser().GetID()
		state := r.GetState()
		// Skip COMMENTED and DISMISSED — they don't represent a decision.
		if state == "COMMENTED" || state == "DISMISSED" || state == "PENDING" {
			continue
		}
		if existing, ok := latest[uid]; !ok || r.GetSubmittedAt().After(existing.GetSubmittedAt().Time) {
			latest[uid] = r
		}
	}

	if len(latest) == 0 {
		return "pending"
	}

	hasApproval := false
	for _, r := range latest {
		switch r.GetState() {
		case "CHANGES_REQUESTED":
			return "changes_requested"
		case "APPROVED":
			hasApproval = true
		}
	}
	if hasApproval {
		return "approved"
	}
	return "pending"
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
		Sort:      github.Ptr("created"),
		Direction: github.Ptr("desc"),
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

// FilterType represents the type of filter for searching issues.
type FilterType string

const (
	FilterLabel     FilterType = "label"
	FilterMilestone FilterType = "milestone"
	FilterProject   FilterType = "project"
	FilterAssignee  FilterType = "assignee"
)

// SearchResult holds the results of a GitHub issue search.
type SearchResult struct {
	Items      []ItemStatus
	TotalCount int
}

// SearchIssues searches for open issues in a repo matching the given filter.
// Requests up to 21 results to detect overflow beyond the 20-item cap.
func (c *Client) SearchIssues(ctx context.Context, owner, repo string, filterType FilterType, filterValue string) (*SearchResult, error) {
	query := fmt.Sprintf("repo:%s/%s is:issue is:open", owner, repo)

	switch filterType {
	case FilterLabel:
		query += fmt.Sprintf(" label:\"%s\"", filterValue)
	case FilterMilestone:
		query += fmt.Sprintf(" milestone:\"%s\"", filterValue)
	case FilterProject:
		query += fmt.Sprintf(" project:%s", filterValue)
	case FilterAssignee:
		query += fmt.Sprintf(" assignee:%s", filterValue)
	default:
		return nil, fmt.Errorf("unknown filter type: %s", filterType)
	}

	opts := &github.SearchOptions{
		Sort:  "created",
		Order: "desc",
		ListOptions: github.ListOptions{
			PerPage: 21,
		},
	}

	result, _, err := c.gh.Search.Issues(ctx, query, opts)
	if err != nil {
		return nil, fmt.Errorf("search issues: %w", err)
	}

	items := make([]ItemStatus, 0, len(result.Issues))
	for _, issue := range result.Issues {
		items = append(items, ItemStatus{
			Number:   issue.GetNumber(),
			Title:    issue.GetTitle(),
			Body:     issue.GetBody(),
			State:    issue.GetState(),
			Labels:   extractLabels(issue.Labels),
			ItemType: "issue",
		})
	}

	return &SearchResult{
		Items:      items,
		TotalCount: result.GetTotal(),
	}, nil
}

// RepoChoice represents a selectable option fetched from a GitHub repo.
type RepoChoice struct {
	Value       string // The value to use in the filter (label name, milestone title, username)
	Description string // Optional extra info (e.g., milestone due date)
	ID          int    // Numeric ID (e.g., milestone number) for API calls that need it
}

// ListLabels returns all labels for a repo.
func (c *Client) ListLabels(ctx context.Context, owner, repo string) ([]RepoChoice, error) {
	var all []RepoChoice
	opts := &github.ListOptions{PerPage: 100}
	for {
		labels, resp, err := c.gh.Issues.ListLabels(ctx, owner, repo, opts)
		if err != nil {
			return nil, fmt.Errorf("list labels: %w", err)
		}
		for _, l := range labels {
			all = append(all, RepoChoice{
				Value:       l.GetName(),
				Description: l.GetDescription(),
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

// AddLabel adds a label to an issue/PR. Creates the label if it doesn't exist.
func (c *Client) AddLabel(ctx context.Context, owner, repo string, number int, label string) error {
	_, _, err := c.gh.Issues.AddLabelsToIssue(ctx, owner, repo, number, []string{label})
	return err
}

// RemoveLabel removes a label from an issue/PR. Ignores errors if label isn't present.
func (c *Client) RemoveLabel(ctx context.Context, owner, repo string, number int, label string) {
	_, _ = c.gh.Issues.RemoveLabelForIssue(ctx, owner, repo, number, label)
}

// CreateComment posts a comment on an issue or PR and returns the comment ID.
func (c *Client) CreateComment(ctx context.Context, owner, repo string, number int, body string) (int64, error) {
	comment, _, err := c.gh.Issues.CreateComment(ctx, owner, repo, number, &github.IssueComment{
		Body: github.Ptr(body),
	})
	if err != nil {
		return 0, fmt.Errorf("create comment: %w", err)
	}
	return comment.GetID(), nil
}

// MergePR merges a pull request using the specified method (merge, squash, rebase).
// Returns nil on success, or an error describing why the merge failed.
func (c *Client) MergePR(ctx context.Context, owner, repo string, number int, method string, commitMsg string) error {
	opts := &github.PullRequestOptions{
		MergeMethod: method,
		CommitTitle: commitMsg,
	}
	_, _, err := c.gh.PullRequests.Merge(ctx, owner, repo, number, "", opts)
	if err != nil {
		return fmt.Errorf("merge PR #%d: %w", number, err)
	}
	return nil
}

// ListMilestones returns open milestones for a repo.
func (c *Client) ListMilestones(ctx context.Context, owner, repo string) ([]RepoChoice, error) {
	var all []RepoChoice
	opts := &github.MilestoneListOptions{
		State:       "open",
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		milestones, resp, err := c.gh.Issues.ListMilestones(ctx, owner, repo, opts)
		if err != nil {
			return nil, fmt.Errorf("list milestones: %w", err)
		}
		for _, ms := range milestones {
			desc := ""
			if ms.DueOn != nil {
				desc = "due " + ms.GetDueOn().Format("2006-01-02")
			}
			all = append(all, RepoChoice{
				Value:       ms.GetTitle(),
				Description: desc,
				ID:          ms.GetNumber(),
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

// ListAssignees returns available assignees for a repo.
func (c *Client) ListAssignees(ctx context.Context, owner, repo string) ([]RepoChoice, error) {
	var all []RepoChoice
	opts := &github.ListOptions{PerPage: 100}
	for {
		users, resp, err := c.gh.Issues.ListAssignees(ctx, owner, repo, opts)
		if err != nil {
			return nil, fmt.Errorf("list assignees: %w", err)
		}
		for _, u := range users {
			all = append(all, RepoChoice{
				Value:       u.GetLogin(),
				Description: u.GetName(),
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

// ListIssuesByMilestone returns open issues for a specific milestone number.
// Uses the Issues list API instead of the Search API to avoid query syntax issues
// with special characters in milestone titles.
func (c *Client) ListIssuesByMilestone(ctx context.Context, owner, repo string, milestoneNumber int) (*SearchResult, error) {
	var allItems []ItemStatus
	opts := &github.IssueListByRepoOptions{
		Milestone: fmt.Sprintf("%d", milestoneNumber),
		State:     "open",
		ListOptions: github.ListOptions{
			PerPage: 21,
		},
	}

	issues, _, err := c.gh.Issues.ListByRepo(ctx, owner, repo, opts)
	if err != nil {
		return nil, fmt.Errorf("list issues by milestone: %w", err)
	}

	for _, issue := range issues {
		// Skip pull requests (Issues API returns PRs too).
		if issue.PullRequestLinks != nil {
			continue
		}
		allItems = append(allItems, ItemStatus{
			Number:   issue.GetNumber(),
			Title:    issue.GetTitle(),
			Body:     issue.GetBody(),
			State:    issue.GetState(),
			Labels:   extractLabels(issue.Labels),
			ItemType: "issue",
		})
	}

	return &SearchResult{
		Items:      allItems,
		TotalCount: len(allItems),
	}, nil
}

// ListIssuesByLabel returns open issues for a specific label.
// Uses the Issues list API instead of the Search API to avoid permission issues
// with tokens that lack search scope.
func (c *Client) ListIssuesByLabel(ctx context.Context, owner, repo, label string) (*SearchResult, error) {
	var allItems []ItemStatus
	opts := &github.IssueListByRepoOptions{
		Labels:    []string{label},
		State:     "open",
		Sort:      "created",
		Direction: "desc",
		ListOptions: github.ListOptions{
			PerPage: 21,
		},
	}

	issues, _, err := c.gh.Issues.ListByRepo(ctx, owner, repo, opts)
	if err != nil {
		return nil, fmt.Errorf("list issues by label: %w", err)
	}

	for _, issue := range issues {
		// Skip pull requests (Issues API returns PRs too).
		if issue.PullRequestLinks != nil {
			continue
		}
		allItems = append(allItems, ItemStatus{
			Number:   issue.GetNumber(),
			Title:    issue.GetTitle(),
			Body:     issue.GetBody(),
			State:    issue.GetState(),
			Labels:   extractLabels(issue.Labels),
			ItemType: "issue",
		})
	}

	return &SearchResult{
		Items:      allItems,
		TotalCount: len(allItems),
	}, nil
}

// BranchPRStatus holds the PR info found for a worktree branch.
type BranchPRStatus struct {
	Number      int
	Title       string
	State       string // "open", "closed", "merged"
	Draft       bool
	ReviewState string // "approved", "changes_requested", "pending", or ""
}

// FetchPRForBranch looks up an open PR whose head branch matches the given branch name.
// Returns nil (no error) if no PR exists for that branch.
func (c *Client) FetchPRForBranch(ctx context.Context, owner, repo, branch string) (*BranchPRStatus, error) {
	opts := &github.PullRequestListOptions{
		Head:  owner + ":" + branch,
		State: "open",
		ListOptions: github.ListOptions{
			PerPage: 1,
		},
	}
	prs, _, err := c.gh.PullRequests.List(ctx, owner, repo, opts)
	if err != nil {
		return nil, fmt.Errorf("list PRs for branch %q: %w", branch, err)
	}
	if len(prs) == 0 {
		return nil, nil
	}

	pr := prs[0]
	status := &BranchPRStatus{
		Number: pr.GetNumber(),
		Title:  pr.GetTitle(),
		Draft:  pr.GetDraft(),
	}
	if pr.GetMerged() {
		status.State = "merged"
	} else if pr.GetState() == "closed" {
		status.State = "closed"
	} else {
		status.State = "open"
	}

	// Fetch review state for open PRs.
	if status.State == "open" && !status.Draft {
		status.ReviewState = c.FetchPRReviewState(ctx, owner, repo, status.Number)
	}

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

func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, name) {
			return true
		}
	}
	return false
}
