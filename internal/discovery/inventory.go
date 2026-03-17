package discovery

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dustinlange/agent-minder/internal/enrollment"
)

// ScanInventory performs a mechanical scan of a repository directory and returns
// a populated Inventory struct suitable for inclusion in an enrollment file.
func ScanInventory(dir string) enrollment.Inventory {
	inv := enrollment.Inventory{
		Languages:       detectLanguages(dir),
		PackageManagers: detectPackageManagers(dir),
		BuildFiles:      detectBuildFiles(dir),
		CI:              detectCI(dir),
		Tooling:         detectTooling(dir),
		ExistingClaude:  detectClaudeConfig(dir),
	}
	return inv
}

// detectLanguages identifies programming languages by the presence of
// characteristic files at the repo root.
func detectLanguages(dir string) []string {
	indicators := map[string][]string{
		"go":         {"go.mod"},
		"python":     {"setup.py", "pyproject.toml", "requirements.txt", "Pipfile"},
		"javascript": {"package.json"},
		"typescript": {"tsconfig.json"},
		"ruby":       {"Gemfile"},
		"rust":       {"Cargo.toml"},
		"java":       {"pom.xml", "build.gradle", "build.gradle.kts"},
		"csharp":     {"*.csproj", "*.sln"},
		"elixir":     {"mix.exs"},
		"php":        {"composer.json"},
		"swift":      {"Package.swift"},
	}

	var langs []string
	for lang, files := range indicators {
		for _, pattern := range files {
			if strings.Contains(pattern, "*") {
				matches, _ := filepath.Glob(filepath.Join(dir, pattern))
				if len(matches) > 0 {
					langs = append(langs, lang)
					break
				}
			} else if fileExists(filepath.Join(dir, pattern)) {
				langs = append(langs, lang)
				break
			}
		}
	}
	sort.Strings(langs)
	return langs
}

// detectPackageManagers identifies package management tools.
func detectPackageManagers(dir string) []string {
	indicators := map[string][]string{
		"go-modules": {"go.mod"},
		"npm":        {"package-lock.json"},
		"yarn":       {"yarn.lock"},
		"pnpm":       {"pnpm-lock.yaml"},
		"pip":        {"requirements.txt"},
		"poetry":     {"poetry.lock"},
		"pipenv":     {"Pipfile.lock"},
		"bundler":    {"Gemfile.lock"},
		"cargo":      {"Cargo.lock"},
		"maven":      {"pom.xml"},
		"gradle":     {"build.gradle", "build.gradle.kts"},
		"composer":   {"composer.lock"},
		"mix":        {"mix.lock"},
		"spm":        {"Package.resolved"},
	}

	var pkgMgrs []string
	for mgr, files := range indicators {
		for _, f := range files {
			if fileExists(filepath.Join(dir, f)) {
				pkgMgrs = append(pkgMgrs, mgr)
				break
			}
		}
	}
	sort.Strings(pkgMgrs)
	return pkgMgrs
}

// detectBuildFiles identifies build system configuration files.
func detectBuildFiles(dir string) []string {
	candidates := []string{
		"Makefile",
		"go.mod",
		"package.json",
		"Cargo.toml",
		"pom.xml",
		"build.gradle",
		"build.gradle.kts",
		"CMakeLists.txt",
		"Rakefile",
		"Taskfile.yml",
		"justfile",
		"mix.exs",
		"composer.json",
	}

	var found []string
	for _, f := range candidates {
		if fileExists(filepath.Join(dir, f)) {
			found = append(found, f)
		}
	}
	return found
}

// detectCI identifies CI/CD systems.
func detectCI(dir string) []string {
	indicators := map[string][]string{
		"github-actions":  {".github/workflows"},
		"gitlab-ci":       {".gitlab-ci.yml"},
		"circleci":        {".circleci/config.yml"},
		"jenkins":         {"Jenkinsfile"},
		"travis-ci":       {".travis.yml"},
		"azure-pipelines": {"azure-pipelines.yml"},
		"bitbucket":       {"bitbucket-pipelines.yml"},
	}

	var ci []string
	for name, paths := range indicators {
		for _, p := range paths {
			full := filepath.Join(dir, p)
			if fileExists(full) || dirExists(full) {
				ci = append(ci, name)
				break
			}
		}
	}
	sort.Strings(ci)
	return ci
}

// detectTooling identifies development tooling.
func detectTooling(dir string) enrollment.Tooling {
	t := enrollment.Tooling{}

	// Secrets management.
	secretsIndicators := map[string][]string{
		"doppler":   {"doppler.yaml"},
		"vault":     {".vault-token"},
		"sops":      {".sops.yaml"},
		"1password": {".1password"},
	}
	for name, files := range secretsIndicators {
		for _, f := range files {
			if fileExists(filepath.Join(dir, f)) {
				t.Secrets = name
				break
			}
		}
		if t.Secrets != "" {
			break
		}
	}

	// Process managers.
	processIndicators := map[string][]string{
		"overmind": {"Procfile.dev", "Procfile"},
		"foreman":  {"Procfile"},
	}
	for name, files := range processIndicators {
		for _, f := range files {
			if fileExists(filepath.Join(dir, f)) {
				t.Process = name
				break
			}
		}
		if t.Process != "" {
			break
		}
	}

	// Container tooling.
	containerIndicators := map[string][]string{
		"docker-compose": {"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"},
		"docker":         {"Dockerfile"},
	}
	for name, files := range containerIndicators {
		for _, f := range files {
			if fileExists(filepath.Join(dir, f)) {
				t.Containers = name
				break
			}
		}
		if t.Containers != "" {
			break
		}
	}

	// Environment management.
	envIndicators := map[string]string{
		".envrc":          "direnv",
		".tool-versions":  ".tool-versions",
		".mise.toml":      "mise",
		".rtx.toml":       "rtx",
		".node-version":   ".node-version",
		".ruby-version":   ".ruby-version",
		".python-version": ".python-version",
	}
	for file, name := range envIndicators {
		if fileExists(filepath.Join(dir, file)) {
			t.Env = append(t.Env, name)
		}
	}
	sort.Strings(t.Env)

	return t
}

// detectClaudeConfig checks for existing Claude Code configuration.
func detectClaudeConfig(dir string) enrollment.ExistingClaudeConfig {
	cfg := enrollment.ExistingClaudeConfig{}

	cfg.SettingsJSON = fileExists(filepath.Join(dir, ".claude", "settings.json"))
	cfg.ClaudeMD = fileExists(filepath.Join(dir, "CLAUDE.md"))

	// Check for agent definitions.
	agentDir := filepath.Join(dir, ".claude", "agents")
	if dirExists(agentDir) {
		matches, _ := filepath.Glob(filepath.Join(agentDir, "*.md"))
		cfg.AgentDef = len(matches) > 0
	}

	return cfg
}

// fileExists returns true if the path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// dirExists returns true if the path exists and is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
