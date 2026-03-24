package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dustinlange/agent-minder/internal/db"
)

func openTestDB(t *testing.T) *db.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return db.NewStore(conn)
}

func setupTestServer(t *testing.T) (*Server, *db.Store, int64) {
	t.Helper()
	store := openTestDB(t)

	project := &db.Project{
		Name:                  "test-deploy",
		IsDeploy:              true,
		GoalType:              "deploy",
		GoalDescription:       "test",
		AutopilotMaxAgents:    3,
		AutopilotMaxTurns:     50,
		AutopilotMaxBudgetUSD: 5.0,
		LLMAnalyzerModel:      "sonnet",
		AutopilotSkipLabel:    "no-agent",
		RefreshIntervalSec:    300,
		StatusIntervalSec:     300,
		AnalysisIntervalSec:   1800,
	}
	if err := store.CreateProject(project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	srv := New(Config{
		Store:     store,
		ProjectID: project.ID,
		DeployID:  "test-deploy",
		APIKey:    "test-key",
	})

	return srv, store, project.ID
}

func doRequest(t *testing.T, srv *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-API-Key", "test-key")
	rr := httptest.NewRecorder()
	srv.middleware(srv.mux).ServeHTTP(rr, req)
	return rr
}

func TestAuth_RejectsWithoutKey(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rr := httptest.NewRecorder()
	srv.middleware(srv.mux).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestAuth_AcceptsValidKey(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	rr := doRequest(t, srv, http.MethodGet, "/status")
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestAuth_NoKeyRequired(t *testing.T) {
	store := openTestDB(t)
	project := &db.Project{
		Name:                "nokey-deploy",
		IsDeploy:            true,
		GoalType:            "deploy",
		RefreshIntervalSec:  300,
		StatusIntervalSec:   300,
		AnalysisIntervalSec: 1800,
	}
	if err := store.CreateProject(project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	srv := New(Config{
		Store:     store,
		ProjectID: project.ID,
		DeployID:  "nokey-deploy",
		APIKey:    "", // no key required
	})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rr := httptest.NewRecorder()
	srv.middleware(srv.mux).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 without key, got %d", rr.Code)
	}
}

func TestStatusEndpoint(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	rr := doRequest(t, srv, http.MethodGet, "/status")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["deploy_id"] != "test-deploy" {
		t.Errorf("deploy_id = %v, want test-deploy", resp["deploy_id"])
	}
	if _, ok := resp["config"]; !ok {
		t.Error("missing config field")
	}
}

func TestTasksEndpoint_Empty(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	rr := doRequest(t, srv, http.MethodGet, "/tasks")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp []any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 0 {
		t.Errorf("expected empty task list, got %d", len(resp))
	}
}

func TestTasksEndpoint_WithTasks(t *testing.T) {
	srv, store, projectID := setupTestServer(t)

	task := &db.AutopilotTask{
		ProjectID:    projectID,
		Owner:        "test",
		Repo:         "repo",
		IssueNumber:  42,
		IssueTitle:   "Fix the thing",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	rr := doRequest(t, srv, http.MethodGet, "/tasks")

	var resp []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 1 {
		t.Fatalf("expected 1 task, got %d", len(resp))
	}
	if resp[0]["issue_number"].(float64) != 42 {
		t.Errorf("issue_number = %v, want 42", resp[0]["issue_number"])
	}
	if resp[0]["status"] != "queued" {
		t.Errorf("status = %v, want queued", resp[0]["status"])
	}
}

func TestSingleTaskEndpoint(t *testing.T) {
	srv, store, projectID := setupTestServer(t)

	task := &db.AutopilotTask{
		ProjectID:    projectID,
		Owner:        "test",
		Repo:         "repo",
		IssueNumber:  99,
		IssueTitle:   "Important task",
		Dependencies: "[42]",
		Status:       "running",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Lookup by issue number.
	rr := doRequest(t, srv, http.MethodGet, "/tasks/99")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["issue_title"] != "Important task" {
		t.Errorf("issue_title = %v, want Important task", resp["issue_title"])
	}
}

func TestSingleTask_NotFound(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	rr := doRequest(t, srv, http.MethodGet, "/tasks/999")
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestDepGraphEndpoint_Empty(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	rr := doRequest(t, srv, http.MethodGet, "/dep-graph")
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing dep graph, got %d", rr.Code)
	}
}

func TestDepGraphEndpoint_WithGraph(t *testing.T) {
	srv, store, projectID := setupTestServer(t)

	graphJSON := `{"42": [], "43": [42]}`
	if err := store.SaveDepGraph(projectID, graphJSON, "sequential"); err != nil {
		t.Fatalf("save dep graph: %v", err)
	}

	rr := doRequest(t, srv, http.MethodGet, "/dep-graph")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["strategy"] != "sequential" {
		t.Errorf("strategy = %v, want sequential", resp["strategy"])
	}
	if _, ok := resp["graph"]; !ok {
		t.Error("missing graph field")
	}
}

func TestAnalysisEndpoint_Empty(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	rr := doRequest(t, srv, http.MethodGet, "/analysis")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp []any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 0 {
		t.Errorf("expected empty analysis list, got %d", len(resp))
	}
}

func TestMetricsEndpoint(t *testing.T) {
	srv, store, projectID := setupTestServer(t)

	// Add a completed task to generate metrics.
	task := &db.AutopilotTask{
		ProjectID:    projectID,
		Owner:        "test",
		Repo:         "repo",
		IssueNumber:  10,
		IssueTitle:   "Done task",
		Dependencies: "[]",
		Status:       "done",
		CostUSD:      1.50,
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	rr := doRequest(t, srv, http.MethodGet, "/metrics")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["spend"]; !ok {
		t.Error("missing spend field")
	}
	tasks := resp["tasks"].(map[string]any)
	if tasks["total"].(float64) != 1 {
		t.Errorf("total tasks = %v, want 1", tasks["total"])
	}
	if tasks["success_rate"].(float64) != 1.0 {
		t.Errorf("success_rate = %v, want 1.0", tasks["success_rate"])
	}
}

func TestTriggerPoll_NotWired(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	rr := doRequest(t, srv, http.MethodPost, "/analysis/poll")
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", rr.Code)
	}
}

func TestTriggerPoll_Wired(t *testing.T) {
	store := openTestDB(t)
	project := &db.Project{
		Name:                "poll-deploy",
		IsDeploy:            true,
		GoalType:            "deploy",
		RefreshIntervalSec:  300,
		StatusIntervalSec:   300,
		AnalysisIntervalSec: 1800,
	}
	if err := store.CreateProject(project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	triggered := false
	srv := New(Config{
		Store:       store,
		ProjectID:   project.ID,
		DeployID:    "poll-deploy",
		APIKey:      "",
		TriggerPoll: func() { triggered = true },
	})

	req := httptest.NewRequest(http.MethodPost, "/analysis/poll", nil)
	rr := httptest.NewRecorder()
	srv.middleware(srv.mux).ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", rr.Code)
	}
	if !triggered {
		t.Error("expected triggerPoll to be called")
	}
}

func TestCORS_PreflightRequest(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodOptions, "/status", nil)
	rr := httptest.NewRecorder()
	srv.middleware(srv.mux).ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected 204 for OPTIONS, got %d", rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS header")
	}
}

func TestStopEndpoint_NotWired(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	rr := doRequest(t, srv, http.MethodPost, "/stop")
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("expected 501, got %d", rr.Code)
	}
}

func TestStopEndpoint_Wired(t *testing.T) {
	store := openTestDB(t)
	project := &db.Project{
		Name:                "stop-deploy",
		IsDeploy:            true,
		GoalType:            "deploy",
		RefreshIntervalSec:  300,
		StatusIntervalSec:   300,
		AnalysisIntervalSec: 1800,
	}
	if err := store.CreateProject(project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	stopped := make(chan struct{}, 1)
	srv := New(Config{
		Store:     store,
		ProjectID: project.ID,
		DeployID:  "stop-deploy",
		APIKey:    "",
		StopDaemon: func() {
			stopped <- struct{}{}
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/stop", nil)
	rr := httptest.NewRecorder()
	srv.middleware(srv.mux).ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", rr.Code)
	}
}

func TestTaskLogEndpoint_NoLog(t *testing.T) {
	srv, store, projectID := setupTestServer(t)

	task := &db.AutopilotTask{
		ProjectID:    projectID,
		Owner:        "test",
		Repo:         "repo",
		IssueNumber:  50,
		IssueTitle:   "No log task",
		Dependencies: "[]",
		Status:       "queued",
	}
	if err := store.CreateAutopilotTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	rr := doRequest(t, srv, http.MethodGet, "/tasks/50/log")
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing log, got %d", rr.Code)
	}
}
