package supervisor

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aptx-health/agent-minder/internal/db"
	ghpkg "github.com/aptx-health/agent-minder/internal/github"
)

func TestTriggerOverrides_Carried(t *testing.T) {
	store, deploy, route, issue := setupTriggerActivationSpec(t)
	activateTriggerForSpec(t, store, deploy, route, issue)

	var budget sql.NullFloat64
	var turns sql.NullInt64
	if err := store.DB().QueryRow(`
		SELECT max_budget_usd, max_turns
		FROM jobs
		WHERE deployment_id = ? AND name = ?`, deploy.ID, "autopilot-issue-42").Scan(&budget, &turns); err != nil {
		t.Fatalf("query activated job overrides: %v", err)
	}
	if !budget.Valid || budget.Float64 != 2.75 {
		t.Fatalf("activated job max_budget_usd = %#v, want 2.75", budget)
	}
	if !turns.Valid || turns.Int64 != 17 {
		t.Fatalf("activated job max_turns = %#v, want 17", turns)
	}
}

func TestJobProvenance_Recorded(t *testing.T) {
	store, deploy, route, issue := setupTriggerActivationSpec(t)
	activateTriggerForSpec(t, store, deploy, route, issue)

	var sourceType sql.NullString
	var sourceName sql.NullString
	if err := store.DB().QueryRow(`
		SELECT source_type, source_name
		FROM jobs
		WHERE deployment_id = ? AND name = ?`, deploy.ID, "autopilot-issue-42").Scan(&sourceType, &sourceName); err != nil {
		t.Fatalf("query activated job provenance: %v", err)
	}
	if !sourceType.Valid || sourceType.String != "trigger" {
		t.Fatalf("activated job source_type = %#v, want trigger", sourceType)
	}
	if !sourceName.Valid || sourceName.String != "ready-trigger" {
		t.Fatalf("activated job source_name = %#v, want ready-trigger", sourceName)
	}
}

func setupTriggerActivationSpec(t *testing.T) (*db.Store, *db.Deployment, TriggerRoute, ghpkg.ItemStatus) {
	t.Helper()
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)

	// Serialized input keeps this test compiling before the route grows the
	// Name/Budget/MaxTurns fields driven by M1-07 and M1-11. Unknown fields are
	// ignored today and will begin participating when those tasks add them.
	var route TriggerRoute
	if err := json.Unmarshal([]byte(`{
		"Labels": ["ready"],
		"Agent": "autopilot",
		"Name": "ready-trigger",
		"Budget": 2.75,
		"MaxTurns": 17
	}`), &route); err != nil {
		t.Fatalf("decode trigger route: %v", err)
	}

	issue := ghpkg.ItemStatus{
		Number:   42,
		Title:    "Exercise trigger activation",
		State:    "open",
		Labels:   []string{"ready"},
		ItemType: "issue",
	}
	return store, deploy, route, issue
}

func activateTriggerForSpec(t *testing.T, store *db.Store, deploy *db.Deployment, route TriggerRoute, issue ghpkg.ItemStatus) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/widgets/issues/42":
			_, _ = w.Write([]byte(`{"number":42,"title":"Exercise trigger activation","body":"spec body","state":"open"}`))
		case "/repos/acme/widgets/issues/42/comments":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	sup := NewTestSupervisor(store, deploy, t.TempDir())
	client := ghpkg.NewClientWithBaseURL("", server.URL+"/")
	if created := sup.createJobForIssue(context.Background(), client, issue, route); created != 1 {
		t.Fatalf("trigger activation created %d jobs, want 1", created)
	}
}
