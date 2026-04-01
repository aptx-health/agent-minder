package daemon

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
)

func testServer(t *testing.T) (*Server, *db.Store) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	store := db.NewStore(conn)

	deploy := &db.Deployment{
		ID:             "test-deploy",
		RepoDir:        "/tmp/repo",
		Owner:          "acme",
		Repo:           "widgets",
		Mode:           "issues",
		MaxAgents:      3,
		MaxTurns:       50,
		MaxBudgetUSD:   5.0,
		AnalyzerModel:  "sonnet",
		SkipLabel:      "no-agent",
		TotalBudgetUSD: 25.0,
		BaseBranch:     "main",
	}
	if err := store.CreateDeployment(deploy); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}

	srv := NewServer(ServerConfig{
		Store:    store,
		DeployID: "test-deploy",
	})
	return srv, store
}

func doRequest(t *testing.T, srv *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	srv.middleware(srv.mux).ServeHTTP(rr, req)
	return rr
}

func TestHandleStatus(t *testing.T) {
	srv, _ := testServer(t)

	rr := doRequest(t, srv, "GET", "/status")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp StatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.DeployID != "test-deploy" {
		t.Errorf("deploy_id = %q, want %q", resp.DeployID, "test-deploy")
	}
	if resp.TotalBudget != 25.0 {
		t.Errorf("total_budget = %v, want 25", resp.TotalBudget)
	}
	if resp.Config.MaxAgents != 3 {
		t.Errorf("config.max_agents = %d, want 3", resp.Config.MaxAgents)
	}
	if resp.Config.BaseBranch != "main" {
		t.Errorf("config.base_branch = %q, want %q", resp.Config.BaseBranch, "main")
	}
}

func TestHandleTasks(t *testing.T) {
	srv, store := testServer(t)

	// Empty list initially.
	rr := doRequest(t, srv, "GET", "/tasks")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var empty []TaskResponse
	if err := json.NewDecoder(rr.Body).Decode(&empty); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Add a task.
	task := &db.Task{
		DeploymentID: "test-deploy",
		IssueNumber:  42,
		IssueTitle:   sql.NullString{String: "Fix auth", Valid: true},
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusQueued,
	}
	if err := store.CreateTask(task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	rr = doRequest(t, srv, "GET", "/tasks")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var tasks []TaskResponse
	if err := json.NewDecoder(rr.Body).Decode(&tasks); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if tasks[0].IssueNumber != 42 {
		t.Errorf("issue_number = %d, want 42", tasks[0].IssueNumber)
	}
	if tasks[0].Status != "queued" {
		t.Errorf("status = %q, want %q", tasks[0].Status, "queued")
	}
	if tasks[0].IssueTitle != "Fix auth" {
		t.Errorf("issue_title = %q, want %q", tasks[0].IssueTitle, "Fix auth")
	}
}

func TestHandleTaskByID(t *testing.T) {
	srv, store := testServer(t)

	task := &db.Task{
		DeploymentID: "test-deploy",
		IssueNumber:  99,
		IssueTitle:   sql.NullString{String: "Add feature", Valid: true},
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusQueued,
	}
	if err := store.CreateTask(task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Valid task.
	rr := doRequest(t, srv, "GET", "/tasks/1")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp TaskResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.IssueNumber != 99 {
		t.Errorf("issue_number = %d, want 99", resp.IssueNumber)
	}
	if resp.IssueTitle != "Add feature" {
		t.Errorf("issue_title = %q, want %q", resp.IssueTitle, "Add feature")
	}

	// Non-existent task → 404.
	rr = doRequest(t, srv, "GET", "/tasks/9999")
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing task, got %d", rr.Code)
	}

	// Invalid ID → 400.
	rr = doRequest(t, srv, "GET", "/tasks/abc")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid id, got %d", rr.Code)
	}
}

func TestHandleDepGraph(t *testing.T) {
	srv, store := testServer(t)

	// No dep graph → 404.
	rr := doRequest(t, srv, "GET", "/dep-graph")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}

	// Create dep graph.
	if err := store.SaveDepGraph("test-deploy", `{"1":[],"2":[1]}`, "linear"); err != nil {
		t.Fatalf("SaveDepGraph: %v", err)
	}

	rr = doRequest(t, srv, "GET", "/dep-graph")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp DepGraphResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.GraphJSON != `{"1":[],"2":[1]}` {
		t.Errorf("graph_json = %q, want %q", resp.GraphJSON, `{"1":[],"2":[1]}`)
	}
	if resp.OptionName != "linear" {
		t.Errorf("option_name = %q, want %q", resp.OptionName, "linear")
	}
}

func TestHandleLessons(t *testing.T) {
	srv, store := testServer(t)

	// Create a lesson scoped to acme/widgets.
	l := &db.Lesson{
		RepoScope: sql.NullString{String: "acme/widgets", Valid: true},
		Content:   "Always run go vet",
		Source:    "review",
		Active:    true,
	}
	if err := store.CreateLesson(l); err != nil {
		t.Fatalf("CreateLesson: %v", err)
	}

	rr := doRequest(t, srv, "GET", "/lessons")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var lessons []LessonResponse
	if err := json.NewDecoder(rr.Body).Decode(&lessons); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(lessons) != 1 {
		t.Fatalf("got %d lessons, want 1", len(lessons))
	}
	if lessons[0].Content != "Always run go vet" {
		t.Errorf("content = %q, want %q", lessons[0].Content, "Always run go vet")
	}
	if lessons[0].Source != "review" {
		t.Errorf("source = %q, want %q", lessons[0].Source, "review")
	}
}

func TestHandleStop(t *testing.T) {
	srv, _ := testServer(t)

	stopped := false
	srv.StopDaemon = func() { stopped = true }

	rr := doRequest(t, srv, "POST", "/stop")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "stopping" {
		t.Errorf("status = %q, want %q", resp["status"], "stopping")
	}
	// StopDaemon is called in a goroutine; give it a moment.
	time.Sleep(10 * time.Millisecond)
	if !stopped {
		t.Error("StopDaemon callback was not invoked")
	}
}

func TestHandleResume(t *testing.T) {
	srv, _ := testServer(t)

	resumed := false
	srv.BudgetResume = func() { resumed = true }

	rr := doRequest(t, srv, "POST", "/resume")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "resumed" {
		t.Errorf("status = %q, want %q", resp["status"], "resumed")
	}
	if !resumed {
		t.Error("BudgetResume callback was not invoked")
	}
}

func TestAPIKeyMiddleware(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	store := db.NewStore(conn)

	deploy := &db.Deployment{
		ID:      "auth-deploy",
		RepoDir: "/tmp/repo",
		Owner:   "acme",
		Repo:    "widgets",
		Mode:    "issues",
	}
	if err := store.CreateDeployment(deploy); err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}

	srv := NewServer(ServerConfig{
		Store:    store,
		DeployID: "auth-deploy",
		APIKey:   "secret-key",
	})

	// No key → 401.
	rr := doRequest(t, srv, "GET", "/status")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", rr.Code)
	}

	// Wrong key → 401.
	req := httptest.NewRequest("GET", "/status", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	rr = httptest.NewRecorder()
	srv.middleware(srv.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong key, got %d", rr.Code)
	}

	// Correct key → 200.
	req = httptest.NewRequest("GET", "/status", nil)
	req.Header.Set("X-API-Key", "secret-key")
	rr = httptest.NewRecorder()
	srv.middleware(srv.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with correct key, got %d", rr.Code)
	}
}
