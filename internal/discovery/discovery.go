package discovery

import (
	"os"
	"path/filepath"
	"strings"

	gitpkg "github.com/dustinlange/agent-minder/internal/git"
	"github.com/dustinlange/agent-minder/internal/onboarding"
)

// LegacyWorktree is kept for backward compat with v1 callers.
type LegacyWorktree struct {
	Path   string `yaml:"path"`
	Branch string `yaml:"branch"`
}

// LegacyRepo is kept for backward compat with v1 callers.
type LegacyRepo struct {
	Path      string           `yaml:"path"`
	ShortName string           `yaml:"short_name"`
	Worktrees []LegacyWorktree `yaml:"worktrees,omitempty"`
}

// RepoInfo holds everything discovered about a single repo directory.
type RepoInfo struct {
	Path       string
	Name       string
	RemoteURL  string
	Branch     string
	ShortName  string
	Readme     string
	ClaudeMD   string
	Worktrees  []LegacyWorktree
	RecentLogs []gitpkg.LogEntry
	Branches   []gitpkg.BranchInfo
	Inventory  onboarding.Inventory // Mechanical inventory of the repo
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
			info.Worktrees = append(info.Worktrees, LegacyWorktree{
				Path:   wt.Path,
				Branch: wt.Branch,
			})
		}
	}

	info.Inventory = ScanInventory(absDir)

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
	// Trim back to a clean word boundary. The common prefix may end
	// mid-word (e.g., "agent-m" from "agent-minder"/"agent-msg").
	// We want to snap back to "agent".
	prefix = trimToWordBoundary(prefix, names)

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

// BuildRepoConfigs converts scanned RepoInfo into LegacyRepo entries.
func BuildRepoConfigs(repos []*RepoInfo) []LegacyRepo {
	var out []LegacyRepo
	for _, r := range repos {
		out = append(out, LegacyRepo{
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

// trimToWordBoundary ensures the prefix ends at a separator boundary
// relative to the original names. If the prefix lands mid-segment
// (e.g., "agent-m" from "agent-minder"/"agent-msg"), it trims back
// to the last separator ("agent").
func trimToWordBoundary(prefix string, names []string) string {
	prefix = strings.TrimRight(prefix, "-_")
	if prefix == "" {
		return ""
	}

	// Check if the prefix is at a clean boundary in all names:
	// either the name equals the prefix, or the next char is a separator.
	atBoundary := true
	for _, name := range names {
		if len(name) > len(prefix) {
			next := name[len(prefix)]
			if next != '-' && next != '_' {
				atBoundary = false
				break
			}
		}
	}

	if atBoundary {
		return prefix
	}

	// Not at a boundary — trim back to the last separator.
	lastSep := strings.LastIndexAny(prefix, "-_")
	if lastSep < 0 {
		return prefix // single segment, no separator to trim to
	}
	return prefix[:lastSep]
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
