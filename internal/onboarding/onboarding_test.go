package onboarding

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewSetsDefaults(t *testing.T) {
	inv := Inventory{
		Languages:       []string{"go"},
		PackageManagers: []string{"go-modules"},
		BuildFiles:      []string{"Makefile", "go.mod"},
	}
	f := New(inv)

	if f.Version != 1 {
		t.Errorf("Version = %d, want 1", f.Version)
	}
	if f.ScannedAt.IsZero() {
		t.Error("ScannedAt should be set")
	}
	if f.Validation.Status != "untested" {
		t.Errorf("Validation.Status = %q, want %q", f.Validation.Status, "untested")
	}
	if len(f.Inventory.Languages) != 1 || f.Inventory.Languages[0] != "go" {
		t.Errorf("Inventory.Languages = %v, want [go]", f.Inventory.Languages)
	}
}

func TestMarshalAndParseRoundTrip(t *testing.T) {
	now := time.Date(2026, 3, 16, 14, 30, 0, 0, time.UTC)
	original := &File{
		Version:   1,
		ScannedAt: now,
		Inventory: Inventory{
			Languages:       []string{"go", "python"},
			PackageManagers: []string{"go-modules", "pip"},
			BuildFiles:      []string{"Makefile", "go.mod"},
			CI:              []string{"github-actions"},
			Tooling: Tooling{
				Secrets:    "doppler",
				Process:    "overmind",
				Containers: "docker-compose",
				Env:        []string{"direnv", ".tool-versions"},
			},
			ExistingClaude: ExistingClaudeConfig{
				SettingsJSON: true,
				AgentDef:     false,
				ClaudeMD:     true,
			},
		},
		Context: Context{
			BuildCommand:        "make build",
			TestCommand:         "go test ./...",
			LintCommand:         "golangci-lint run",
			SpecialInstructions: "Run `doppler run --` prefix for env vars",
			ToolsNeeded:         []string{"go", "git", "gh", "make"},
		},
		Permissions: Permissions{
			AllowedTools: []string{
				"Bash(go *)",
				"Bash(git *)",
				"Bash(gh *)",
			},
		},
		Validation: Validation{
			LastRun:  &now,
			Status:   "pass",
			Failures: []string{},
		},
	}

	data, err := Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	parsed, err := ParseBytes(data)
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}

	if parsed.Version != original.Version {
		t.Errorf("Version = %d, want %d", parsed.Version, original.Version)
	}
	if !parsed.ScannedAt.Equal(original.ScannedAt) {
		t.Errorf("ScannedAt = %v, want %v", parsed.ScannedAt, original.ScannedAt)
	}
	if len(parsed.Inventory.Languages) != 2 {
		t.Errorf("Languages = %v, want 2 items", parsed.Inventory.Languages)
	}
	if parsed.Inventory.Tooling.Secrets != "doppler" {
		t.Errorf("Tooling.Secrets = %q, want %q", parsed.Inventory.Tooling.Secrets, "doppler")
	}
	if !parsed.Inventory.ExistingClaude.SettingsJSON {
		t.Error("ExistingClaude.SettingsJSON should be true")
	}
	if parsed.Inventory.ExistingClaude.AgentDef {
		t.Error("ExistingClaude.AgentDef should be false")
	}
	if parsed.Context.BuildCommand != "make build" {
		t.Errorf("Context.BuildCommand = %q, want %q", parsed.Context.BuildCommand, "make build")
	}
	if len(parsed.Permissions.AllowedTools) != 3 {
		t.Errorf("AllowedTools len = %d, want 3", len(parsed.Permissions.AllowedTools))
	}
	if parsed.Validation.Status != "pass" {
		t.Errorf("Validation.Status = %q, want %q", parsed.Validation.Status, "pass")
	}
}

func TestParseInvalidYAML(t *testing.T) {
	_, err := ParseBytes([]byte("not: valid: yaml: ["))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestParseMissingVersion(t *testing.T) {
	data := []byte("scanned_at: 2026-03-16T14:30:00Z\ninventory:\n  languages: [go]\n")
	_, err := ParseBytes(data)
	if err == nil {
		t.Fatal("expected error for missing version")
	}
}

func TestWriteAndParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".agent-minder", "onboarding.yaml")

	inv := Inventory{
		Languages:       []string{"go"},
		PackageManagers: []string{"go-modules"},
		BuildFiles:      []string{"go.mod"},
		CI:              []string{"github-actions"},
	}
	f := New(inv)
	f.Context = Context{
		BuildCommand: "go build ./...",
		TestCommand:  "go test ./...",
	}

	if err := Write(path, f); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Verify file exists.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}

	parsed, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if parsed.Version != 1 {
		t.Errorf("Version = %d, want 1", parsed.Version)
	}
	if parsed.Context.BuildCommand != "go build ./..." {
		t.Errorf("BuildCommand = %q, want %q", parsed.Context.BuildCommand, "go build ./...")
	}
}

func TestFilePathAndExists(t *testing.T) {
	dir := t.TempDir()
	path := FilePath(dir)
	expected := filepath.Join(dir, ".agent-minder", "onboarding.yaml")
	if path != expected {
		t.Errorf("FilePath = %q, want %q", path, expected)
	}

	if Exists(dir) {
		t.Error("Exists should return false before file is created")
	}

	f := New(Inventory{Languages: []string{"go"}})
	if err := Write(path, f); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if !Exists(dir) {
		t.Error("Exists should return true after file is created")
	}
}

func TestParseNonexistentFile(t *testing.T) {
	_, err := Parse("/nonexistent/path/onboarding.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}
