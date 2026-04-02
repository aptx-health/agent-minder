package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentTemplates(t *testing.T) {
	templates := AgentTemplates()
	if len(templates) < 2 {
		t.Fatalf("expected at least 2 templates, got %d", len(templates))
	}

	// Check required agents exist.
	names := map[string]bool{}
	for _, tmpl := range templates {
		names[tmpl.Name] = true
	}
	for _, required := range []string{"autopilot", "reviewer"} {
		if !names[required] {
			t.Errorf("missing required template: %s", required)
		}
	}
}

func TestAgentTemplates_FrontmatterParseable(t *testing.T) {
	for _, tmpl := range AgentTemplates() {
		t.Run(tmpl.Name, func(t *testing.T) {
			content := "---\n" + tmpl.Frontmatter + "\n---\n\n" + tmpl.DefaultBody
			contract, err := ParseContractFromBytes([]byte(content))
			if err != nil {
				t.Fatalf("template %s frontmatter not parseable: %v", tmpl.Name, err)
			}
			if contract.Name != tmpl.Name {
				t.Errorf("name mismatch: got %q, want %q", contract.Name, tmpl.Name)
			}
		})
	}
}

func TestInstallAgentDef(t *testing.T) {
	dir := t.TempDir()
	tmpl := AgentTemplates()[0] // autopilot

	path, err := InstallAgentDef(dir, tmpl)
	if err != nil {
		t.Fatal(err)
	}

	// File should exist.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("installed file doesn't exist: %v", err)
	}

	// Should be parseable.
	contract, err := ParseContract(path)
	if err != nil {
		t.Fatalf("installed def not parseable: %v", err)
	}
	if contract.Name != tmpl.Name {
		t.Errorf("name = %q, want %q", contract.Name, tmpl.Name)
	}

	// File should contain frontmatter and body.
	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, "---") {
		t.Error("missing frontmatter markers")
	}
	if !strings.Contains(content, tmpl.DefaultBody[:20]) {
		t.Error("missing default body")
	}
}

func TestInstallAgentDef_DoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	tmpl := AgentTemplates()[0]

	// Install once.
	path, _ := InstallAgentDef(dir, tmpl)

	// Write custom content.
	custom := "---\nname: autopilot\n---\n\nCustom instructions here."
	_ = os.WriteFile(path, []byte(custom), 0644)

	// Install again — should overwrite (caller should check existence first).
	_, _ = InstallAgentDef(dir, tmpl)
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "Custom instructions") {
		t.Error("expected overwrite, but custom content persisted")
	}
}

func TestValidateAgentDefs_Valid(t *testing.T) {
	dir := t.TempDir()
	for _, tmpl := range AgentTemplates() {
		_, _ = InstallAgentDef(dir, tmpl)
	}

	errs := ValidateAgentDefs(dir)
	if len(errs) > 0 {
		t.Errorf("unexpected validation errors: %v", errs)
	}
}

func TestValidateAgentDefs_Invalid(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, ".claude", "agents")
	_ = os.MkdirAll(agentsDir, 0755)

	// Write a broken agent def.
	_ = os.WriteFile(filepath.Join(agentsDir, "broken.md"), []byte("no frontmatter here"), 0644)

	errs := ValidateAgentDefs(dir)
	if len(errs) != 1 {
		t.Errorf("expected 1 validation error, got %d: %v", len(errs), errs)
	}
}

func TestValidateAgentDefs_NoDir(t *testing.T) {
	dir := t.TempDir()
	errs := ValidateAgentDefs(dir)
	if len(errs) != 0 {
		t.Errorf("expected no errors for missing dir, got %v", errs)
	}
}
