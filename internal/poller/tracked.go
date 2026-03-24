package poller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/dustinlange/agent-minder/internal/db"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	ghpkg "github.com/dustinlange/agent-minder/internal/github"
)

// GitHubRepo holds a deduplicated owner/repo pair from enrolled repos.
type GitHubRepo struct {
	Owner string
	Repo  string
}

// GitHubRepos returns deduplicated GitHub owner/repo pairs from enrolled repos.
func (p *Poller) GitHubRepos() []GitHubRepo {
	repos, err := p.store.GetRepos(p.project.ID)
	if err != nil || len(repos) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var result []GitHubRepo
	for _, r := range repos {
		remote := gitpkg.RemoteURL(r.Path)
		o, rp := gitpkg.ParseGitHubRemote(remote)
		if o == "" {
			continue
		}
		key := o + "/" + rp
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, GitHubRepo{Owner: o, Repo: rp})
	}
	return result
}

// SearchGitHubIssues searches for issues in a GitHub repo matching the given filter.
func (p *Poller) SearchGitHubIssues(ctx context.Context, owner, repo string, filterType ghpkg.FilterType, filterValue string) (*ghpkg.SearchResult, error) {
	token := config.GetIntegrationToken("github")
	if token == "" {
		return nil, fmt.Errorf("no GitHub token configured")
	}
	gh := ghpkg.NewClient(token)
	return gh.SearchIssues(ctx, owner, repo, filterType, filterValue)
}

// FetchFilterChoices returns available choices for a filter type from GitHub.
func (p *Poller) FetchFilterChoices(ctx context.Context, owner, repo string, filterType ghpkg.FilterType) ([]ghpkg.RepoChoice, error) {
	token := config.GetIntegrationToken("github")
	if token == "" {
		return nil, fmt.Errorf("no GitHub token configured")
	}
	gh := ghpkg.NewClient(token)

	switch filterType {
	case ghpkg.FilterLabel:
		return gh.ListLabels(ctx, owner, repo)
	case ghpkg.FilterMilestone:
		return gh.ListMilestones(ctx, owner, repo)
	case ghpkg.FilterAssignee:
		return gh.ListAssignees(ctx, owner, repo)
	default:
		return nil, nil
	}
}

// SearchIssuesByMilestone searches for issues using a milestone number via the Issues API.
func (p *Poller) SearchIssuesByMilestone(ctx context.Context, owner, repo string, milestoneNumber int) (*ghpkg.SearchResult, error) {
	token := config.GetIntegrationToken("github")
	if token == "" {
		return nil, fmt.Errorf("no GitHub token configured")
	}
	gh := ghpkg.NewClient(token)
	return gh.ListIssuesByMilestone(ctx, owner, repo, milestoneNumber)
}

// BulkAddTrackedItems converts ItemStatus results to TrackedItems and bulk-inserts them.
// Returns the number of items actually added.
func (p *Poller) BulkAddTrackedItems(ctx context.Context, items []ghpkg.ItemStatus, owner, repo string) (int, error) {
	dbItems := make([]*db.TrackedItem, 0, len(items))
	now := time.Now().UTC().Format(time.RFC3339)
	for _, s := range items {
		dbItems = append(dbItems, &db.TrackedItem{
			ProjectID:     p.project.ID,
			Source:        "github",
			Owner:         owner,
			Repo:          repo,
			Number:        s.Number,
			ItemType:      s.ItemType,
			Title:         s.Title,
			State:         s.State,
			Labels:        strings.Join(s.Labels, ","),
			LastStatus:    s.CompactStatus(),
			LastCheckedAt: now,
		})
	}

	added, err := p.store.BulkAddTrackedItems(dbItems)
	if err != nil {
		return 0, err
	}
	if added > 0 {
		p.emit("tracked", fmt.Sprintf("Bulk added %d items from %s/%s", added, owner, repo), nil)
	}
	return added, nil
}

// ClearAndBulkAddTrackedItems clears existing tracked items then bulk-adds new ones.
func (p *Poller) ClearAndBulkAddTrackedItems(ctx context.Context, items []ghpkg.ItemStatus, owner, repo string) (int, error) {
	if err := p.store.ClearTrackedItems(p.project.ID); err != nil {
		return 0, fmt.Errorf("clear tracked items: %w", err)
	}
	p.emit("tracked", "Cleared all tracked items", nil)
	return p.BulkAddTrackedItems(ctx, items, owner, repo)
}

// UpdateTrackedItems adds new items to the tracked list (skips duplicates).
// Returns (added, 0, error). Terminal items are kept for analyzer context;
// use the TUI cleanup action (c) to archive them manually.
func (p *Poller) UpdateTrackedItems(ctx context.Context, items []ghpkg.ItemStatus, owner, repo string) (int, int, error) {
	added, err := p.BulkAddTrackedItems(ctx, items, owner, repo)
	if err != nil {
		return 0, 0, err
	}
	return added, 0, nil
}

// DefaultOwnerRepo derives a default owner/repo from the project's enrolled repos.
// It looks for GitHub remote URLs and returns the first match.
func (p *Poller) DefaultOwnerRepo() (owner, repo string) {
	ghRepos := p.GitHubRepos()
	if len(ghRepos) == 0 {
		return "", ""
	}
	return ghRepos[0].Owner, ghRepos[0].Repo
}

// FetchItemStatus fetches the status of a GitHub item without adding it to tracking.
func (p *Poller) FetchItemStatus(ctx context.Context, ref *ghpkg.ItemRef) (*ghpkg.ItemStatus, error) {
	token := config.GetIntegrationToken("github")
	if token == "" {
		return nil, fmt.Errorf("no GitHub token configured")
	}

	gh := ghpkg.NewClient(token)
	status, err := gh.FetchItem(ctx, ref.Owner, ref.Repo, ref.Number)
	if err != nil {
		return nil, fmt.Errorf("fetching %s/%s#%d: %w", ref.Owner, ref.Repo, ref.Number, err)
	}
	return status, nil
}

// AddTrackedItemByRef resolves a GitHub item reference and adds it as a tracked item.
// Also creates an autopilot_task so the item is immediately visible in Operations.
// Returns the added item on success.
func (p *Poller) AddTrackedItemByRef(ctx context.Context, ref *ghpkg.ItemRef) (*db.TrackedItem, error) {
	token := config.GetIntegrationToken("github")
	if token == "" {
		return nil, fmt.Errorf("no GitHub token configured")
	}

	gh := ghpkg.NewClient(token)
	status, err := gh.FetchItem(ctx, ref.Owner, ref.Repo, ref.Number)
	if err != nil {
		return nil, fmt.Errorf("fetching %s/%s#%d: %w", ref.Owner, ref.Repo, ref.Number, err)
	}

	item := &db.TrackedItem{
		ProjectID:     p.project.ID,
		Source:        "github",
		Owner:         ref.Owner,
		Repo:          ref.Repo,
		Number:        ref.Number,
		ItemType:      status.ItemType,
		Title:         status.Title,
		State:         status.State,
		Labels:        strings.Join(status.Labels, ","),
		LastStatus:    status.CompactStatus(),
		LastCheckedAt: time.Now().UTC().Format(time.RFC3339),
	}

	if err := p.store.AddTrackedItem(item); err != nil {
		return nil, err
	}

	// Also create an autopilot_task for open issues so it shows in Operations
	// and is ready for autopilot without a separate conversion step.
	if status.ItemType == "issue" && status.State == "open" {
		body := ""
		if content, fetchErr := gh.FetchItemContent(ctx, ref.Owner, ref.Repo, ref.Number, "issue"); fetchErr == nil {
			body = content.Body
		}
		task := &db.AutopilotTask{
			ProjectID:    p.project.ID,
			Owner:        ref.Owner,
			Repo:         ref.Repo,
			IssueNumber:  ref.Number,
			IssueTitle:   status.Title,
			IssueBody:    body,
			Dependencies: "[]",
			Status:       "queued",
		}
		// Ignore UNIQUE constraint errors — task may already exist from a previous session.
		_ = p.store.CreateAutopilotTask(task)
	}

	p.emit("tracked", fmt.Sprintf("Now tracking %s: %s [%s]", item.DisplayRef(), item.Title, item.LastStatus), nil)
	return item, nil
}

// RemoveTrackedItemByRef removes a tracked item by owner/repo/number.
// Also marks the corresponding autopilot task as "removed" so it won't
// reappear in dependency graphs or be re-discovered by watch polling.
func (p *Poller) RemoveTrackedItemByRef(ref *ghpkg.ItemRef) error {
	err := p.store.RemoveTrackedItem(p.project.ID, ref.Owner, ref.Repo, ref.Number)
	if err != nil {
		return err
	}
	// Mark the autopilot task as removed (no-op if no matching task exists).
	_ = p.store.RemoveAutopilotTaskByIssue(p.project.ID, ref.Number)
	p.emit("tracked", fmt.Sprintf("Untracked %s/%s#%d", ref.Owner, ref.Repo, ref.Number), nil)
	return nil
}
