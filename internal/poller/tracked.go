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

// DefaultOwnerRepo derives a default owner/repo from the project's enrolled repos.
// It looks for GitHub remote URLs and returns the first match.
func (p *Poller) DefaultOwnerRepo() (owner, repo string) {
	repos, err := p.store.GetRepos(p.project.ID)
	if err != nil || len(repos) == 0 {
		return "", ""
	}
	for _, r := range repos {
		remote := gitpkg.RemoteURL(r.Path)
		if o, rp := parseGitHubRemote(remote); o != "" {
			return o, rp
		}
	}
	return "", ""
}

// parseGitHubRemote extracts owner/repo from a GitHub remote URL.
// Handles HTTPS (https://github.com/owner/repo.git) and SSH (git@github.com:owner/repo.git).
func parseGitHubRemote(url string) (owner, repo string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return "", ""
	}

	// HTTPS: https://github.com/owner/repo.git
	if strings.Contains(url, "github.com/") {
		idx := strings.Index(url, "github.com/")
		path := url[idx+len("github.com/"):]
		path = strings.TrimSuffix(path, ".git")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1]
		}
	}

	// SSH: git@github.com:owner/repo.git
	if strings.Contains(url, "github.com:") {
		idx := strings.Index(url, "github.com:")
		path := url[idx+len("github.com:"):]
		path = strings.TrimSuffix(path, ".git")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1]
		}
	}

	return "", ""
}

// AddTrackedItemByRef resolves a GitHub item reference and adds it as a tracked item.
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

	p.emit("tracked", fmt.Sprintf("Now tracking %s: %s [%s]", item.DisplayRef(), item.Title, item.LastStatus), nil)
	return item, nil
}

// RemoveTrackedItemByRef removes a tracked item by owner/repo/number.
func (p *Poller) RemoveTrackedItemByRef(ref *ghpkg.ItemRef) error {
	err := p.store.RemoveTrackedItem(p.project.ID, ref.Owner, ref.Repo, ref.Number)
	if err != nil {
		return err
	}
	p.emit("tracked", fmt.Sprintf("Untracked %s/%s#%d", ref.Owner, ref.Repo, ref.Number), nil)
	return nil
}
