package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestEnvelopeForegroundLineGolden pins compat C-1: the typed envelope must
// render to the pre-envelope foreground line byte-identically.
func TestEnvelopeForegroundLineGolden(t *testing.T) {
	at := time.Date(2026, 8, 8, 13, 4, 5, 0, time.Local)
	envelope := Envelope{
		Time:         at,
		DeploymentID: "deadbeef",
		JobID:        7,
		Type:         EventInfo,
		Severity:     SeverityInfo,
		Summary:      "Agent started on #42",
	}
	const want = "[13:04:05] info: Agent started on #42"
	if got := envelope.ForegroundLine(); got != want {
		t.Fatalf("ForegroundLine() = %q, want %q", got, want)
	}

	// The legacy formula, applied to every registered type: rendering the
	// envelope and rendering its Legacy() projection must agree byte-for-byte.
	for _, typ := range AllEventTypes() {
		envelope.Type = typ
		legacy := envelope.Legacy()
		want := fmt.Sprintf("[%s] %s: %s", legacy.Time.Format("15:04:05"), legacy.Type, legacy.Summary)
		if got := envelope.ForegroundLine(); got != want {
			t.Fatalf("type %q: ForegroundLine() = %q, legacy rendering = %q", typ, got, want)
		}
	}
}

func TestEnvelopeLegacyProjection(t *testing.T) {
	at := time.Date(2026, 8, 8, 13, 4, 5, 0, time.Local)
	legacy := Envelope{Time: at, JobID: 42, Type: EventBailed, Summary: "gave up"}.Legacy()
	want := Event{Time: at, Type: "bailed", Summary: "gave up", TaskID: 42}
	if legacy != want {
		t.Fatalf("Legacy() = %+v, want %+v", legacy, want)
	}
}

// TestEventEmitSitesUseRegisteredTypes is a grep-style guard (in the style of
// TestCoordinatorNewCallersAreTopologyOwners): emit sites must pass an
// EventType constant, never a raw string literal, so the vocabulary stays
// closed at the point of emission.
func TestEventEmitSitesUseRegisteredTypes(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(source), "\n") {
			if strings.Contains(line, `emitEvent("`) || strings.Contains(line, `EmitEvent("`) {
				t.Errorf("%s:%d: emit site passes a raw string literal; use an EventType constant from event.go", name, i+1)
			}
		}
	}
}

// TestEventTypeEnumIsClosed asserts every EventType constant declared in
// event.go is registered in eventSeverity (via AllEventTypes) and vice versa —
// adding a constant without registering it, or registering a type without
// declaring a constant, fails here.
func TestEventTypeEnumIsClosed(t *testing.T) {
	source, err := os.ReadFile("event.go")
	if err != nil {
		t.Fatalf("read event.go: %v", err)
	}
	declPattern := regexp.MustCompile(`Event[A-Za-z]+\s+EventType = "([a-z]+)"`)
	declared := make(map[EventType]bool)
	for _, match := range declPattern.FindAllStringSubmatch(string(source), -1) {
		declared[EventType(match[1])] = true
	}
	if len(declared) == 0 {
		t.Fatal("no EventType constant declarations found in event.go; guard regex needs updating")
	}

	registered := make(map[EventType]bool)
	for _, typ := range AllEventTypes() {
		registered[typ] = true
		if !declared[typ] {
			t.Errorf("type %q is registered in eventSeverity but has no constant declaration", typ)
		}
		if !typ.Known() {
			t.Errorf("type %q from AllEventTypes is not Known", typ)
		}
	}
	for typ := range declared {
		if !registered[typ] {
			t.Errorf("constant for %q is declared but not registered in eventSeverity", typ)
		}
	}
}
