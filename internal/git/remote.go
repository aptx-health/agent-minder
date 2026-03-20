package git

import "strings"

// ParseGitHubRemote extracts owner/repo from a GitHub remote URL.
// Handles HTTPS (https://github.com/owner/repo.git) and SSH (git@github.com:owner/repo.git).
func ParseGitHubRemote(url string) (owner, repo string) {
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
