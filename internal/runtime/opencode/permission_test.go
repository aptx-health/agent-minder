package opencode

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDefaultHeadlessPermissionValid asserts the baked-in policy is valid JSON
// and pins the headless-critical decisions so an accidental edit can't silently
// re-enable interactive gating.
func TestDefaultHeadlessPermissionValid(t *testing.T) {
	var policy map[string]any
	if err := json.Unmarshal([]byte(defaultHeadlessPermission), &policy); err != nil {
		t.Fatalf("defaultHeadlessPermission is not valid JSON: %v", err)
	}
	want := map[string]string{
		"edit":               "allow",
		"write":              "allow",
		"bash":               "allow",
		"webfetch":           "allow",
		"external_directory": "deny", // worktree boundary
		"doom_loop":          "allow",
		"question":           "deny", // no interactive approver
	}
	for k, v := range want {
		if policy[k] != v {
			t.Errorf("policy[%q] = %v, want %q", k, policy[k], v)
		}
	}
}

func TestWithHeadlessPermissionAppendsWhenAbsent(t *testing.T) {
	env := []string{"PATH=/bin", "GITHUB_TOKEN=x"}
	got := withHeadlessPermission(env)
	var found string
	for _, kv := range got {
		if strings.HasPrefix(kv, permissionEnvKey+"=") {
			found = strings.TrimPrefix(kv, permissionEnvKey+"=")
		}
	}
	if found != defaultHeadlessPermission {
		t.Fatalf("OPENCODE_PERMISSION = %q, want default policy", found)
	}
}

func TestWithHeadlessPermissionRespectsOperatorOverride(t *testing.T) {
	override := permissionEnvKey + `={"bash":"deny"}`
	env := []string{"PATH=/bin", override}
	got := withHeadlessPermission(env)

	var vals []string
	for _, kv := range got {
		if strings.HasPrefix(kv, permissionEnvKey+"=") {
			vals = append(vals, kv)
		}
	}
	if len(vals) != 1 {
		t.Fatalf("expected exactly one OPENCODE_PERMISSION entry, got %v", vals)
	}
	if vals[0] != override {
		t.Fatalf("operator override was modified: got %q, want %q", vals[0], override)
	}
}
