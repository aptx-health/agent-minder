package supervisor

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/aptx-health/agent-minder/internal/db"
)

// Every agent contract tells the agent to read CLAUDE.md for the architecture,
// schema, and command reference. When it drifts, agents act on false facts —
// a model told the schema is v5 will write a v5→v6 migration on top of v8.
// These tests pin the two claims that drift silently and cost the most.

func readClaudeMD(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	return string(data)
}

func TestClaudeMDSchemaVersion(t *testing.T) {
	content := readClaudeMD(t)

	m := regexp.MustCompile(`### DB schema \(internal/db\) — currently v(\d+)`).FindStringSubmatch(content)
	if m == nil {
		t.Fatal("CLAUDE.md is missing the '### DB schema (internal/db) — currently vN' heading")
	}

	want := "v" + m[1]
	got := "v" + strconv.Itoa(db.SchemaVersion())
	if want != got {
		t.Errorf("CLAUDE.md documents schema %s, but internal/db reports %s.\n"+
			"Update the DB schema section in CLAUDE.md (heading, columns, and migration list).", want, got)
	}
}

func TestClaudeMDBuiltInAgentList(t *testing.T) {
	content := readClaudeMD(t)

	idx := strings.Index(content, "- Built-in agents")
	if idx < 0 {
		t.Fatal("CLAUDE.md is missing the '- Built-in agents' bullet")
	}
	line := content[idx:]
	if end := strings.IndexByte(line, '\n'); end >= 0 {
		line = line[:end]
	}

	documented := map[string]bool{}
	for _, name := range regexp.MustCompile("`([a-z-]+)`").FindAllStringSubmatch(line, -1) {
		documented[name[1]] = true
	}

	var missing, stale []string
	registry := map[string]bool{}
	for _, tmpl := range AgentTemplates() {
		registry[tmpl.Name] = true
		if !documented[tmpl.Name] {
			missing = append(missing, tmpl.Name)
		}
	}
	for name := range documented {
		// designer is deliberately documented as living outside the registry.
		if !registry[name] && name != "designer" {
			stale = append(stale, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	if len(missing) > 0 {
		t.Errorf("CLAUDE.md omits built-in agents that exist in AgentTemplates(): %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("CLAUDE.md lists agents that are not in AgentTemplates(): %v", stale)
	}
}
