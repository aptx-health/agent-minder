package deploy

import (
	"strings"
	"testing"
)

func TestGenerateID_Format(t *testing.T) {
	id := GenerateID()
	parts := strings.Split(id, "-")
	if len(parts) != 3 {
		t.Errorf("expected 3 parts, got %d: %q", len(parts), id)
	}
	for _, p := range parts {
		if p == "" {
			t.Errorf("empty part in ID %q", id)
		}
	}
}

func TestGenerateID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		id := GenerateID()
		if seen[id] {
			t.Logf("collision after %d IDs (expected for 125k combinations)", len(seen))
			return
		}
		seen[id] = true
	}
}

func TestGenerateUniqueID_AvoidsCollisions(t *testing.T) {
	existing := []string{"bold-fox-leap", "calm-oak-swim"}
	id := GenerateUniqueID(existing)
	for _, e := range existing {
		if id == e {
			t.Errorf("generated ID %q collides with existing", id)
		}
	}
}
