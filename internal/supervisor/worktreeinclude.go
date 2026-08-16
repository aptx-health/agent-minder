package supervisor

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const worktreeIncludePath = ".agent-minder/worktreeinclude"

type worktreeIncludeResult struct {
	Copied   []string
	Warnings []string
}

type worktreeIncludeRule struct {
	pattern  string
	negated  bool
	anchored bool
	dirOnly  bool
}

func copyWorktreeIncludes(repoDir, worktreePath string) (worktreeIncludeResult, error) {
	var result worktreeIncludeResult

	tracked, err := gitPathTracked(repoDir, worktreeIncludePath)
	if err != nil {
		return result, err
	}
	if !tracked {
		return result, nil
	}

	includeFile := filepath.Join(repoDir, filepath.FromSlash(worktreeIncludePath))
	data, err := os.ReadFile(includeFile)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("read %s: %v", worktreeIncludePath, err))
		return result, nil
	}
	rules := parseWorktreeIncludeRules(string(data))
	if len(rules) == 0 {
		return result, nil
	}

	candidates, err := untrackedRegularCandidates(repoDir)
	if err != nil {
		return result, err
	}

	matchedByRule := make([]bool, len(rules))
	for _, rel := range candidates {
		included, matchedRule := worktreeIncludeMatch(rel, rules)
		if matchedRule >= 0 && !rules[matchedRule].negated {
			matchedByRule[matchedRule] = true
		}
		if !included {
			continue
		}

		src := filepath.Join(repoDir, filepath.FromSlash(rel))
		dst := filepath.Join(worktreePath, filepath.FromSlash(rel))
		copied, warning := copyIncludedFile(src, dst, rel)
		if warning != "" {
			result.Warnings = append(result.Warnings, warning)
			continue
		}
		if copied {
			result.Copied = append(result.Copied, rel)
		}
	}

	for i, rule := range rules {
		if !rule.negated && !matchedByRule[i] {
			result.Warnings = append(result.Warnings, fmt.Sprintf("pattern %q matched no untracked files", rule.originalPattern()))
		}
	}

	sort.Strings(result.Copied)
	sort.Strings(result.Warnings)
	return result, nil
}

func parseWorktreeIncludeRules(contents string) []worktreeIncludeRule {
	var rules []worktreeIncludeRule
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		rule := worktreeIncludeRule{}
		if strings.HasPrefix(line, "!") {
			rule.negated = true
			line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
			if line == "" {
				continue
			}
		}
		rule.dirOnly = strings.HasSuffix(line, "/")
		if strings.HasPrefix(line, "/") {
			rule.anchored = true
			line = strings.TrimPrefix(line, "/")
		}
		line = strings.TrimSuffix(line, "/")
		line = path.Clean(strings.ReplaceAll(line, "\\", "/"))
		if line == "." {
			continue
		}
		rule.pattern = line
		rules = append(rules, rule)
	}
	return rules
}

func (r worktreeIncludeRule) originalPattern() string {
	prefix := ""
	if r.negated {
		prefix = "!"
	}
	if r.anchored {
		prefix += "/"
	}
	suffix := ""
	if r.dirOnly {
		suffix = "/"
	}
	return prefix + r.pattern + suffix
}

func worktreeIncludeMatch(rel string, rules []worktreeIncludeRule) (bool, int) {
	rel = path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	included := false
	matchedRule := -1
	for i, rule := range rules {
		if rule.matches(rel) {
			included = !rule.negated
			matchedRule = i
		}
	}
	return included, matchedRule
}

func (r worktreeIncludeRule) matches(rel string) bool {
	pattern := r.pattern
	if pattern == "" {
		return false
	}

	if !strings.Contains(pattern, "/") && !r.anchored {
		parts := strings.Split(rel, "/")
		for i, part := range parts {
			if globMatch(pattern, part) {
				if !r.dirOnly || i < len(parts)-1 {
					return true
				}
			}
		}
		return false
	}

	if globMatch(pattern, rel) {
		return !r.dirOnly
	}
	if strings.HasPrefix(rel, pattern+"/") {
		return true
	}
	return false
}

func globMatch(pattern, name string) bool {
	if pattern == name {
		return true
	}
	if strings.Contains(pattern, "**") {
		return doublestarMatch(pattern, name)
	}
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

func doublestarMatch(pattern, name string) bool {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")
	ok, err := regexp.MatchString(b.String(), name)
	return err == nil && ok
}

func untrackedRegularCandidates(repoDir string) ([]string, error) {
	tracked, err := gitListZ(repoDir, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	trackedSet := make(map[string]bool, len(tracked))
	for _, rel := range tracked {
		trackedSet[rel] = true
	}

	var rels []string
	err = filepath.WalkDir(repoDir, func(p string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p == repoDir {
			return nil
		}
		rel, err := filepath.Rel(repoDir, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" || strings.HasPrefix(rel, ".git/") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if trackedSet[rel] {
			return nil
		}
		rels = append(rels, rel)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk untracked files: %w", err)
	}
	sort.Strings(rels)
	return rels, nil
}

func copyIncludedFile(src, dst, rel string) (bool, string) {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return false, fmt.Sprintf("%s: stat source: %v", rel, err)
	}
	if srcInfo.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Sprintf("%s: skipped symlink", rel)
	}
	if !srcInfo.Mode().IsRegular() {
		return false, fmt.Sprintf("%s: skipped non-regular file", rel)
	}
	if _, err := os.Lstat(dst); err == nil {
		return false, fmt.Sprintf("%s: destination already exists", rel)
	} else if !os.IsNotExist(err) {
		return false, fmt.Sprintf("%s: stat destination: %v", rel, err)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, fmt.Sprintf("%s: create destination directory: %v", rel, err)
	}
	in, err := os.Open(src)
	if err != nil {
		return false, fmt.Sprintf("%s: open source: %v", rel, err)
	}
	defer func() { _ = in.Close() }()

	openedInfo, err := in.Stat()
	if err != nil {
		return false, fmt.Sprintf("%s: stat opened source: %v", rel, err)
	}
	if !os.SameFile(srcInfo, openedInfo) {
		return false, fmt.Sprintf("%s: skipped changing source", rel)
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, srcInfo.Mode().Perm())
	if err != nil {
		return false, fmt.Sprintf("%s: create destination: %v", rel, err)
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return false, fmt.Sprintf("%s: copy: %v", rel, err)
	}
	return true, ""
}

func gitPathTracked(repoDir, rel string) (bool, error) {
	cmd := exec.Command("git", "-C", repoDir, "ls-files", "--error-unmatch", "--", rel)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return false, nil
		}
		return false, fmt.Errorf("git ls-files %s: %w", rel, err)
	}
	return true, nil
}

func gitListZ(repoDir string, args ...string) ([]string, error) {
	cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	var paths []string
	for _, part := range bytes.Split(out, []byte{0}) {
		if len(part) == 0 {
			continue
		}
		paths = append(paths, filepath.ToSlash(string(part)))
	}
	return paths, nil
}
