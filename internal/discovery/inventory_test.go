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

func TestScanAgentLogsEmpty(t *testing.T) {
	// Non-existent directory returns nil.
	result := ScanAgentLogs("/nonexistent/path", "")
	if result != nil {
		t.Errorf("ScanAgentLogs(nonexistent) = %v, want nil", result)
	}

	// Empty directory returns nil.
	dir := t.TempDir()
	result = ScanAgentLogs(dir, "")
	if result != nil {
		t.Errorf("ScanAgentLogs(empty) = %v, want nil", result)
	}
}

func TestScanAgentLogsWithDenials(t *testing.T) {
	dir := t.TempDir()

	// Create a log file with a result event containing permission denials.
	logContent := `{"type":"system","message":"starting"}
{"type":"result","subtype":"success","is_error":false,"num_turns":3,"total_cost_usd":0.05,"result":"done","permission_denials":[{"tool_name":"Write"},{"tool_name":"Bash","command":"npm install"}],"session_id":"test-123"}
`
	if err := os.WriteFile(filepath.Join(dir, "myproject-issue-1.log"), []byte(logContent), 0644); err != nil {
		t.Fatal(err)
	}

	result := ScanAgentLogs(dir, "myproject")
	if len(result) != 2 {
		t.Fatalf("ScanAgentLogs len = %d, want 2; got %v", len(result), result)
	}

	// Results are sorted.
	if !contains(result, "Write") {
		t.Errorf("result = %v, should contain 'Write'", result)
	}
	if !contains(result, "Bash(npm *)") {
		t.Errorf("result = %v, should contain 'Bash(npm *)'", result)
	}
}

func TestScanAgentLogsNoDenials(t *testing.T) {
	dir := t.TempDir()

	// Log with no permission denials.
	logContent := `{"type":"result","subtype":"success","is_error":false,"num_turns":2,"total_cost_usd":0.03,"result":"ok","permission_denials":[],"session_id":"test-456"}
`
	if err := os.WriteFile(filepath.Join(dir, "myproject-issue-2.log"), []byte(logContent), 0644); err != nil {
		t.Fatal(err)
	}

	result := ScanAgentLogs(dir, "myproject")
	if len(result) != 0 {
		t.Errorf("ScanAgentLogs = %v, want empty", result)
	}
}

func TestScanAgentLogsDeduplicates(t *testing.T) {
	dir := t.TempDir()

	// Two log files with overlapping denials for the same project.
	log1 := `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":0.01,"result":"fail","permission_denials":["Write"],"session_id":"s1"}
`
	log2 := `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":0.01,"result":"fail","permission_denials":["Write","Read"],"session_id":"s2"}
`
	if err := os.WriteFile(filepath.Join(dir, "proj-issue-1.log"), []byte(log1), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proj-issue-2.log"), []byte(log2), 0644); err != nil {
		t.Fatal(err)
	}

	result := ScanAgentLogs(dir, "proj")
	if len(result) != 2 {
		t.Fatalf("ScanAgentLogs len = %d, want 2; got %v", len(result), result)
	}
	if !contains(result, "Read") {
		t.Errorf("result = %v, should contain 'Read'", result)
	}
	if !contains(result, "Write") {
		t.Errorf("result = %v, should contain 'Write'", result)
	}
}

func TestScanAgentLogsScopedByProject(t *testing.T) {
	dir := t.TempDir()

	// Logs from two different projects.
	projA := `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":0.01,"result":"done","permission_denials":["Write"],"session_id":"a1"}
`
	projB := `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":0.01,"result":"done","permission_denials":["Read","Edit"],"session_id":"b1"}
`
	if err := os.WriteFile(filepath.Join(dir, "alpha-issue-1.log"), []byte(projA), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta-issue-1.log"), []byte(projB), 0644); err != nil {
		t.Fatal(err)
	}

	// Scoped to alpha — should only see Write.
	result := ScanAgentLogs(dir, "alpha")
	if len(result) != 1 {
		t.Fatalf("ScanAgentLogs(alpha) len = %d, want 1; got %v", len(result), result)
	}
	if result[0] != "Write" {
		t.Errorf("result[0] = %q, want %q", result[0], "Write")
	}

	// Scoped to beta — should only see Edit, Read.
	result = ScanAgentLogs(dir, "beta")
	if len(result) != 2 {
		t.Fatalf("ScanAgentLogs(beta) len = %d, want 2; got %v", len(result), result)
	}
	if !contains(result, "Edit") {
		t.Errorf("result = %v, should contain 'Edit'", result)
	}
	if !contains(result, "Read") {
		t.Errorf("result = %v, should contain 'Read'", result)
	}

	// Unscoped — should see all three.
	result = ScanAgentLogs(dir, "")
	if len(result) != 3 {
		t.Fatalf("ScanAgentLogs('') len = %d, want 3; got %v", len(result), result)
	}
}

func TestListAgentLogProjects(t *testing.T) {
	dir := t.TempDir()

	// Create log files for different projects.
	for _, name := range []string{
		"alpha-issue-1.log",
		"alpha-issue-2.log",
		"beta-issue-1.log",
		"gamma-issue-10.log",
		"not-a-match.log",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	projects := ListAgentLogProjects(dir)
	if len(projects) != 3 {
		t.Fatalf("ListAgentLogProjects len = %d, want 3; got %v", len(projects), projects)
	}
	if projects[0] != "alpha" || projects[1] != "beta" || projects[2] != "gamma" {
		t.Errorf("projects = %v, want [alpha beta gamma]", projects)
	}
}

func TestListAgentLogProjectsEmpty(t *testing.T) {
	result := ListAgentLogProjects("/nonexistent/path")
	if result != nil {
		t.Errorf("ListAgentLogProjects(nonexistent) = %v, want nil", result)
	}

	dir := t.TempDir()
	result = ListAgentLogProjects(dir)
	if result != nil {
		t.Errorf("ListAgentLogProjects(empty) = %v, want nil", result)
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
