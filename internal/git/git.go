package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// LogEntry represents a single git log entry.
type LogEntry struct {
	Hash    string
	Subject string
	Author  string
	Date    time.Time
}

// WorktreeEntry represents a git worktree.
type WorktreeEntry struct {
	Path   string
	Branch string
	IsMain bool
}

// BranchInfo represents a git branch.
type BranchInfo struct {
	Name      string
	IsRemote  bool
	IsCurrent bool
}

// run executes a git command in the given directory and returns trimmed stdout.
func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), string(exitErr.Stderr))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// IsRepo checks if the directory is inside a git repository.
func IsRepo(dir string) bool {
	_, err := run(dir, "rev-parse", "--git-dir")
	return err == nil
}

// TopLevel returns the root of the git repository.
func TopLevel(dir string) (string, error) {
	return run(dir, "rev-parse", "--show-toplevel")
}

// RepoName returns the basename of the git repo root.
func RepoName(dir string) (string, error) {
	top, err := TopLevel(dir)
	if err != nil {
		return "", err
	}
	return filepath.Base(top), nil
}

// RemoteURL returns the origin remote URL, or empty string if none.
func RemoteURL(dir string) string {
	url, _ := run(dir, "remote", "get-url", "origin")
	return url
}

// CurrentBranch returns the current branch name.
func CurrentBranch(dir string) (string, error) {
	return run(dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// Log returns the most recent n log entries.
func Log(dir string, n int) ([]LogEntry, error) {
	// Format: hash|subject|author|ISO date
	format := "%h|%s|%an|%aI"
	out, err := run(dir, "log", fmt.Sprintf("-%d", n), fmt.Sprintf("--format=%s", format))
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	var entries []LogEntry
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		date, _ := time.Parse(time.RFC3339, parts[3])
		entries = append(entries, LogEntry{
			Hash:    parts[0],
			Subject: parts[1],
			Author:  parts[2],
			Date:    date,
		})
	}
	return entries, nil
}

// LogSince returns log entries since the given time.
func LogSince(dir string, since time.Time) ([]LogEntry, error) {
	format := "%h|%s|%an|%aI"
	out, err := run(dir, "log", fmt.Sprintf("--since=%s", since.Format(time.RFC3339)), fmt.Sprintf("--format=%s", format))
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	var entries []LogEntry
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		date, _ := time.Parse(time.RFC3339, parts[3])
		entries = append(entries, LogEntry{
			Hash:    parts[0],
			Subject: parts[1],
			Author:  parts[2],
			Date:    date,
		})
	}
	return entries, nil
}

// Branches returns all branches (local and remote).
func Branches(dir string) ([]BranchInfo, error) {
	out, err := run(dir, "branch", "-a", "--format=%(refname:short)|%(HEAD)")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	var branches []BranchInfo
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		b := BranchInfo{
			Name:      name,
			IsRemote:  strings.HasPrefix(name, "origin/"),
			IsCurrent: strings.TrimSpace(parts[1]) == "*",
		}
		branches = append(branches, b)
	}
	return branches, nil
}

// Worktrees returns all git worktrees for the repo.
func Worktrees(dir string) ([]WorktreeEntry, error) {
	out, err := run(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	var worktrees []WorktreeEntry
	var current WorktreeEntry

	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			if current.Path != "" {
				worktrees = append(worktrees, current)
			}
			current = WorktreeEntry{Path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			// Strip refs/heads/ prefix.
			current.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "":
			// Blank line separates entries; the last entry gets flushed below.
		}
	}
	if current.Path != "" {
		worktrees = append(worktrees, current)
	}

	// Mark the first worktree as main (it's the primary worktree).
	if len(worktrees) > 0 {
		worktrees[0].IsMain = true
	}

	return worktrees, nil
}

// LogGrep returns recent log entries whose subject matches a pattern (e.g., "#42").
// Searches the last 200 commits to keep it bounded.
func LogGrep(dir string, pattern string) ([]LogEntry, error) {
	format := "%h|%s|%an|%aI"
	out, err := run(dir, "log", "-200", fmt.Sprintf("--format=%s", format), "--grep="+pattern, "--fixed-strings")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	var entries []LogEntry
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		date, _ := time.Parse(time.RFC3339, parts[3])
		entries = append(entries, LogEntry{
			Hash:    parts[0],
			Subject: parts[1],
			Author:  parts[2],
			Date:    date,
		})
	}
	return entries, nil
}

// Diff returns the diff between two refs (e.g., "main...feature/auth").
func Diff(dir, spec string) (string, error) {
	return run(dir, "diff", spec)
}

// DiffStat returns a stat summary of the diff between two refs.
func DiffStat(dir, spec string) (string, error) {
	return run(dir, "diff", "--stat", spec)
}

// WorktreeAdd creates a new git worktree at the given path on a new branch.
// If startPoint is non-empty, the branch is created from that ref instead of HEAD.
func WorktreeAdd(repoDir, worktreePath, branch string, startPoint ...string) error {
	args := []string{"worktree", "add", worktreePath, "-b", branch}
	if len(startPoint) > 0 && startPoint[0] != "" {
		args = append(args, startPoint[0])
	}
	_, err := run(repoDir, args...)
	return err
}

// WorktreeAddExisting creates a new git worktree at the given path from an existing branch.
// Unlike WorktreeAdd, this does not create a new branch (-b).
func WorktreeAddExisting(repoDir, worktreePath, branch string) error {
	_, err := run(repoDir, "worktree", "add", worktreePath, branch)
	return err
}

// WorktreeRemove removes a git worktree.
func WorktreeRemove(repoDir, worktreePath string) error {
	_, err := run(repoDir, "worktree", "remove", "--force", worktreePath)
	return err
}

// DeleteBranch deletes a local branch.
func DeleteBranch(repoDir, branch string) error {
	_, err := run(repoDir, "branch", "-D", branch)
	return err
}

// Fetch fetches from origin.
func Fetch(dir string) error {
	_, err := run(dir, "fetch", "origin")
	return err
}

// BranchExists checks if a branch exists locally or as a remote tracking branch.
func BranchExists(dir, branch string) bool {
	// Check local branch.
	if _, err := run(dir, "rev-parse", "--verify", "refs/heads/"+branch); err == nil {
		return true
	}
	// Check remote tracking branch.
	if _, err := run(dir, "rev-parse", "--verify", "refs/remotes/origin/"+branch); err == nil {
		return true
	}
	return false
}

// DirDiskUsage returns the total disk usage of a directory in bytes.
// Returns 0 if the directory does not exist or cannot be measured.
func DirDiskUsage(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// FormatBytes formats a byte count as a human-readable string (KB, MB, GB).
func FormatBytes(b int64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.0f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.0f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// DefaultBranch returns the default branch name (main, master, etc.).
func DefaultBranch(dir string) (string, error) {
	out, err := run(dir, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		// Returns something like "refs/remotes/origin/main".
		parts := strings.Split(out, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1], nil
		}
	}
	return "main", nil
}

// CommitsSince returns the number of commits since a given ref.
func CommitsSince(dir, ref string) (int, error) {
	out, err := run(dir, "rev-list", "--count", ref+"..HEAD")
	if err != nil {
		return 0, err
	}
	var count int
	_, _ = fmt.Sscanf(out, "%d", &count)
	return count, nil
}
