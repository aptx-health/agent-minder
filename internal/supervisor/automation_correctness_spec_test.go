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

// TestJobProvenance_Watch asserts a --watch filter discovery (no matching
// named trigger route) is recorded as source_type='watch' rather than the
// mislabeled 'trigger' from before issue #602.
func TestJobProvenance_Watch(t *testing.T) {
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)
	deploy.WatchFilter = sql.NullString{String: "label:ready", Valid: true}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/widgets/issues/99":
			_, _ = w.Write([]byte(`{"number":99,"title":"Watch discovery","body":"body","state":"open"}`))
		case "/repos/acme/widgets/issues/99/comments":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	sup := NewTestSupervisor(store, deploy, t.TempDir())
	client := ghpkg.NewClientWithBaseURL("", server.URL+"/")
	issue := ghpkg.ItemStatus{Number: 99, Title: "Watch discovery", State: "open", ItemType: "issue"}

	if created := sup.createJobForIssue(context.Background(), client, issue, TriggerRoute{Agent: "autopilot"}, "watch"); created != 1 {
		t.Fatalf("watch activation created %d jobs, want 1", created)
	}

	var sourceType, sourceName, sourceRef sql.NullString
	if err := store.DB().QueryRow(`
		SELECT source_type, source_name, source_ref
		FROM jobs
		WHERE deployment_id = ? AND name = ?`, deploy.ID, "autopilot-issue-99").
		Scan(&sourceType, &sourceName, &sourceRef); err != nil {
		t.Fatalf("query watch job provenance: %v", err)
	}
	if !sourceType.Valid || sourceType.String != "watch" {
		t.Fatalf("watch job source_type = %#v, want watch", sourceType)
	}
	if sourceName.Valid && sourceName.String != "" {
		t.Fatalf("watch job source_name = %#v, want NULL/empty (no named route)", sourceName)
	}
	if !sourceRef.Valid || sourceRef.String != "label:ready" {
		t.Fatalf("watch job source_ref = %#v, want label:ready", sourceRef)
	}
}

// TestJobProvenance_Explicit asserts a targeted deploy (minder deploy <issue>)
// records source_type='explicit'.
func TestJobProvenance_Explicit(t *testing.T) {
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)
	deploy.RepoDir = "" // skip agent-def repo lookup

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/widgets/issues/7":
			_, _ = w.Write([]byte(`{"number":7,"title":"Explicit target","body":"body","state":"open"}`))
		case "/repos/acme/widgets/issues/7/comments":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	origGHClientFactory := newGHClientForToken
	newGHClientForToken = func(token string) *ghpkg.Client {
		return ghpkg.NewClientWithBaseURL("", server.URL+"/")
	}
	t.Cleanup(func() { newGHClientForToken = origGHClientFactory })

	result, err := Prepare(context.Background(), store, nil, deploy, []int{7}, "autopilot", "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("Prepare total = %d, want 1", result.Total)
	}

	var sourceType sql.NullString
	if err := store.DB().QueryRow(`
		SELECT source_type FROM jobs
		WHERE deployment_id = ? AND name = ?`, deploy.ID, "autopilot-issue-7").
		Scan(&sourceType); err != nil {
		t.Fatalf("query explicit job provenance: %v", err)
	}
	if !sourceType.Valid || sourceType.String != "explicit" {
		t.Fatalf("explicit job source_type = %#v, want explicit", sourceType)
	}
}

func TestActivation_ContentFetchFailure_NoJobThenRetry(t *testing.T) {
	store, deploy, route, issue := setupTriggerActivationSpec(t)

	// First poll: content fetch fails (body endpoint 500). No job created.
	// Second poll: content fetch succeeds and the job is created.
	failNext := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/widgets/issues/42":
			if failNext {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
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

	if created := sup.createJobForIssue(context.Background(), client, issue, route, "trigger"); created != 0 {
		t.Fatalf("activation with failing content fetch created %d jobs, want 0", created)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM jobs WHERE deployment_id = ?`, deploy.ID).Scan(&count); err != nil {
		t.Fatalf("count jobs after failed fetch: %v", err)
	}
	if count != 0 {
		t.Fatalf("jobs created after failed fetch = %d, want 0", count)
	}

	// Next poll succeeds and the job is created.
	failNext = false
	if created := sup.createJobForIssue(context.Background(), client, issue, route, "trigger"); created != 1 {
		t.Fatalf("retry activation created %d jobs, want 1", created)
	}
}

func TestPrepare_ContentFetchFailure_Errors(t *testing.T) {
	store := testStoreForMultiAgent(t)
	deploy := testDeployForMultiAgent(t, store)

	// FetchItem (status) succeeds via the issue GET, but the second issue GET
	// (from FetchItemContent) fails, so Prepare must surface that error.
	var issueGets int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/widgets/pulls/42":
			http.NotFound(w, r) // force fallback to the issue path
		case "/repos/acme/widgets/issues/42":
			issueGets++
			if issueGets == 1 {
				_, _ = w.Write([]byte(`{"number":42,"title":"Exercise prepare","body":"spec body","state":"open"}`))
				return
			}
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	orig := newGHClientForToken
	newGHClientForToken = func(string) *ghpkg.Client {
		return ghpkg.NewClientWithBaseURL("", server.URL+"/")
	}
	t.Cleanup(func() { newGHClientForToken = orig })

	_, err := Prepare(context.Background(), store, nil, deploy, []int{42}, "autopilot", "")
	if err == nil {
		t.Fatal("Prepare with failing content fetch returned nil error, want failure")
	}

	var count int
	if qErr := store.DB().QueryRow(`SELECT COUNT(*) FROM jobs WHERE deployment_id = ?`, deploy.ID).Scan(&count); qErr != nil {
		t.Fatalf("count jobs after Prepare failure: %v", qErr)
	}
	if count != 0 {
		t.Fatalf("jobs created after Prepare failure = %d, want 0", count)
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
	if created := sup.createJobForIssue(context.Background(), client, issue, route, "trigger"); created != 1 {
		t.Fatalf("trigger activation created %d jobs, want 1", created)
	}
}
