package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanInventoryGoRepo(t *testing.T) {
	dir := t.TempDir()

	// Create files that indicate a Go project.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/foo\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("build:\n\tgo build\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create .github/workflows directory for CI detection.
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, "ci.yml"), []byte("name: CI\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create CLAUDE.md for Claude config detection.
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# Instructions\n"), 0644); err != nil {
		t.Fatal(err)
	}

	inv := ScanInventory(dir)

	// Check languages.
	if !contains(inv.Languages, "go") {
		t.Errorf("Languages = %v, should contain 'go'", inv.Languages)
	}

	// Check package managers.
	if !contains(inv.PackageManagers, "go-modules") {
		t.Errorf("PackageManagers = %v, should contain 'go-modules'", inv.PackageManagers)
	}

	// Check build files.
	if !contains(inv.BuildFiles, "Makefile") {
		t.Errorf("BuildFiles = %v, should contain 'Makefile'", inv.BuildFiles)
	}
	if !contains(inv.BuildFiles, "go.mod") {
		t.Errorf("BuildFiles = %v, should contain 'go.mod'", inv.BuildFiles)
	}

	// Check CI.
	if !contains(inv.CI, "github-actions") {
		t.Errorf("CI = %v, should contain 'github-actions'", inv.CI)
	}

	// Check Claude config.
	if !inv.ExistingClaude.ClaudeMD {
		t.Error("ExistingClaude.ClaudeMD should be true")
	}
	if inv.ExistingClaude.SettingsJSON {
		t.Error("ExistingClaude.SettingsJSON should be false (not created)")
	}
}

func TestScanInventoryEmptyDir(t *testing.T) {
	dir := t.TempDir()
	inv := ScanInventory(dir)

	if len(inv.Languages) != 0 {
		t.Errorf("Languages = %v, want empty", inv.Languages)
	}
	if len(inv.PackageManagers) != 0 {
		t.Errorf("PackageManagers = %v, want empty", inv.PackageManagers)
	}
	if len(inv.BuildFiles) != 0 {
		t.Errorf("BuildFiles = %v, want empty", inv.BuildFiles)
	}
	if len(inv.CI) != 0 {
		t.Errorf("CI = %v, want empty", inv.CI)
	}
}

func TestScanInventoryTooling(t *testing.T) {
	dir := t.TempDir()

	// Create direnv config.
	if err := os.WriteFile(filepath.Join(dir, ".envrc"), []byte("use flake\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create docker-compose.
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("version: '3'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create Procfile.dev for overmind.
	if err := os.WriteFile(filepath.Join(dir, "Procfile.dev"), []byte("web: go run .\n"), 0644); err != nil {
		t.Fatal(err)
	}

	inv := ScanInventory(dir)

	if inv.Tooling.Containers != "docker-compose" {
		t.Errorf("Tooling.Containers = %q, want %q", inv.Tooling.Containers, "docker-compose")
	}
	if inv.Tooling.Process != "overmind" {
		t.Errorf("Tooling.Process = %q, want %q", inv.Tooling.Process, "overmind")
	}
	if !contains(inv.Tooling.Env, "direnv") {
		t.Errorf("Tooling.Env = %v, should contain 'direnv'", inv.Tooling.Env)
	}
}

func TestScanInventoryClaudeConfig(t *testing.T) {
	dir := t.TempDir()

	// Create .claude/settings.json.
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create .claude/agents/ with a definition.
	agentsDir := filepath.Join(claudeDir, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "autopilot.md"), []byte("# Agent"), 0644); err != nil {
		t.Fatal(err)
	}

	inv := ScanInventory(dir)

	if !inv.ExistingClaude.SettingsJSON {
		t.Error("ExistingClaude.SettingsJSON should be true")
	}
	if !inv.ExistingClaude.AgentDef {
		t.Error("ExistingClaude.AgentDef should be true")
	}
	if inv.ExistingClaude.ClaudeMD {
		t.Error("ExistingClaude.ClaudeMD should be false")
	}
}

func TestScanRepoIncludesInventory(t *testing.T) {
	dir := setupTestRepo(t, "inv-test")

	// Add go.mod to the test repo.
	goMod := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(goMod, []byte("module example.com/inv-test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := ScanRepo(dir)
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}

	if !contains(info.Inventory.Languages, "go") {
		t.Errorf("Inventory.Languages = %v, should contain 'go'", info.Inventory.Languages)
	}
	if !contains(info.Inventory.PackageManagers, "go-modules") {
		t.Errorf("Inventory.PackageManagers = %v, should contain 'go-modules'", info.Inventory.PackageManagers)
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
