// Package discovery scans repositories for metadata used during enrollment.
package discovery

import (
	"os"
	"path/filepath"

	gitpkg "github.com/aptx-health/agent-minder/internal/git"
	"github.com/aptx-health/agent-minder/internal/onboarding"
)

// RepoInfo holds discovered information about a repository.
type RepoInfo struct {
	Path      string
	Name      string
	RemoteURL string
	Branch    string
	ShortName string
	Inventory onboarding.Inventory
}

// NotARepoError is returned when the directory is not a git repo.
type NotARepoError struct{ Path string }

func (e *NotARepoError) Error() string { return e.Path + " is not a git repository" }

// ScanRepo gathers information about a repository directory.
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
	info.ShortName = info.Name

	info.Inventory = scanInventory(absDir)

	return info, nil
}

// scanInventory detects languages, build files, and CI config.
func scanInventory(repoDir string) onboarding.Inventory {
	inv := onboarding.Inventory{}

	// Detect languages.
	langMarkers := map[string]string{
		"go.mod":         "go",
		"package.json":   "javascript",
		"Cargo.toml":     "rust",
		"pyproject.toml": "python",
		"setup.py":       "python",
		"Gemfile":        "ruby",
		"pom.xml":        "java",
		"build.gradle":   "java",
	}
	seen := map[string]bool{}
	for file, lang := range langMarkers {
		if _, err := os.Stat(filepath.Join(repoDir, file)); err == nil {
			if !seen[lang] {
				inv.Languages = append(inv.Languages, lang)
				seen[lang] = true
			}
		}
	}

	// Detect CI.
	ciPaths := map[string]string{
		".github/workflows": "GitHub Actions",
		".circleci":         "CircleCI",
		".gitlab-ci.yml":    "GitLab CI",
		"Jenkinsfile":       "Jenkins",
	}
	for path, name := range ciPaths {
		if _, err := os.Stat(filepath.Join(repoDir, path)); err == nil {
			inv.CI = append(inv.CI, name)
		}
	}

	// Detect build files.
	buildFiles := []string{"Makefile", "Dockerfile", "docker-compose.yml", "Taskfile.yml"}
	for _, f := range buildFiles {
		if _, err := os.Stat(filepath.Join(repoDir, f)); err == nil {
			inv.BuildFiles = append(inv.BuildFiles, f)
		}
	}

	return inv
}
