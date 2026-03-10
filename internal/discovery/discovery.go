package discovery

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/dustinlange/agent-minder/internal/config"
	gitpkg "github.com/dustinlange/agent-minder/internal/git"
)

// RepoInfo holds everything discovered about a single repo directory.
type RepoInfo struct {
	Path       string
	Name       string
	RemoteURL  string
	Branch     string
	ShortName  string
	Readme     string
	ClaudeMD   string
	Worktrees  []config.Worktree
	RecentLogs []gitpkg.LogEntry
	Branches   []gitpkg.BranchInfo
}

// ScanRepo gathers information about a single repository directory.
func ScanRepo(dir string) (*RepoInfo, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	if !gitpkg.IsRepo(absDir) {
		return nil, &NotARepoError{Path: absDir}
	}

	info := &RepoInfo{Path: absDir}

	info.Name, _ = gitpkg.RepoName(absDir)
	info.RemoteURL = gitpkg.RemoteURL(absDir)
	info.Branch, _ = gitpkg.CurrentBranch(absDir)
	info.ShortName = deriveShortName(info.Name)

	info.Readme = readFileIfExists(filepath.Join(absDir, "README.md"))
	info.ClaudeMD = readFileIfExists(filepath.Join(absDir, "CLAUDE.md"))

	info.RecentLogs, _ = gitpkg.Log(absDir, 20)
	info.Branches, _ = gitpkg.Branches(absDir)

	worktrees, err := gitpkg.Worktrees(absDir)
	if err == nil {
		for _, wt := range worktrees {
			info.Worktrees = append(info.Worktrees, config.Worktree{
				Path:   wt.Path,
				Branch: wt.Branch,
			})
		}
	}

	return info, nil
}

// DeriveProjectName guesses a project name from a set of repo directories.
// It looks for a common prefix (e.g., ripit-app + ripit-infra → ripit).
// Falls back to the first repo's name.
func DeriveProjectName(repos []*RepoInfo) string {
	if len(repos) == 0 {
		return "project"
	}
	if len(repos) == 1 {
		return repos[0].ShortName
	}

	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.Name
	}

	prefix := longestCommonPrefix(names)
	// Strip trailing separator (e.g., "ripit-" → "ripit").
	prefix = strings.TrimRight(prefix, "-_")

	if prefix == "" || len(prefix) < 2 {
		return repos[0].ShortName
	}
	return prefix
}

// SuggestTopics generates topic names for a set of repos plus a coord topic.
func SuggestTopics(projectName string, repos []*RepoInfo) []string {
	var topics []string
	for _, r := range repos {
		topics = append(topics, projectName+"/"+r.ShortName)
	}
	topics = append(topics, projectName+"/coord")
	return topics
}

// BuildRepoConfigs converts scanned RepoInfo into config.Repo entries.
func BuildRepoConfigs(repos []*RepoInfo) []config.Repo {
	var out []config.Repo
	for _, r := range repos {
		out = append(out, config.Repo{
			Path:      r.Path,
			ShortName: r.ShortName,
			Worktrees: r.Worktrees,
		})
	}
	return out
}

// deriveShortName strips common prefixes to get a short topic-friendly name.
// "ripit-app" → "app", "my-project" → "my-project" (no common prefix context here).
func deriveShortName(name string) string {
	// Just use the name as-is; project-level prefix stripping happens in SuggestTopics.
	return name
}

func readFileIfExists(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// Truncate very large files.
	const maxLen = 4096
	if len(data) > maxLen {
		return string(data[:maxLen])
	}
	return string(data)
}

func longestCommonPrefix(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	prefix := strs[0]
	for _, s := range strs[1:] {
		for !strings.HasPrefix(s, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}

// NotARepoError is returned when a directory is not a git repository.
type NotARepoError struct {
	Path string
}

func (e *NotARepoError) Error() string {
	return "not a git repository: " + e.Path
}
