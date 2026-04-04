package daemon

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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

	var empty []JobResponse
	if err := json.NewDecoder(rr.Body).Decode(&empty); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Add a task.
	task := &db.Job{
		DeploymentID: "test-deploy",
		Agent:        "autopilot",
		Name:         "issue-42",
		IssueNumber:  42,
		IssueTitle:   sql.NullString{String: "Fix auth", Valid: true},
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusQueued,
	}
	if err := store.CreateJob(task); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	rr = doRequest(t, srv, "GET", "/tasks")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var jobs []JobResponse
	if err := json.NewDecoder(rr.Body).Decode(&jobs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	if jobs[0].IssueNumber != 42 {
		t.Errorf("issue_number = %d, want 42", jobs[0].IssueNumber)
	}
	if jobs[0].Status != "queued" {
		t.Errorf("status = %q, want %q", jobs[0].Status, "queued")
	}
	if jobs[0].IssueTitle != "Fix auth" {
		t.Errorf("issue_title = %q, want %q", jobs[0].IssueTitle, "Fix auth")
	}
	if jobs[0].Title != "Fix auth" {
		t.Errorf("title = %q, want %q", jobs[0].Title, "Fix auth")
	}
}

func TestHandleTaskByID(t *testing.T) {
	srv, store := testServer(t)

	task := &db.Job{
		DeploymentID: "test-deploy",
		IssueNumber:  99,
		IssueTitle:   sql.NullString{String: "Add feature", Valid: true},
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusQueued,
	}
	if err := store.CreateJob(task); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// Valid task.
	rr := doRequest(t, srv, "GET", "/jobs/1")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp JobResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.IssueNumber != 99 {
		t.Errorf("issue_number = %d, want 99", resp.IssueNumber)
	}
	if resp.IssueTitle != "Add feature" {
		t.Errorf("issue_title = %q, want %q", resp.IssueTitle, "Add feature")
	}
	if resp.Title != "Add feature" {
		t.Errorf("title = %q, want %q", resp.Title, "Add feature")
	}

	// Non-existent task → 404.
	rr = doRequest(t, srv, "GET", "/jobs/9999")
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing job, got %d", rr.Code)
	}

	// Invalid ID → 400.
	rr = doRequest(t, srv, "GET", "/jobs/abc")
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

func TestHandleJobLog(t *testing.T) {
	srv, store := testServer(t)

	// Invalid ID → 400.
	rr := doRequest(t, srv, "GET", "/jobs/abc/log")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid id, got %d", rr.Code)
	}

	// Non-existent task → 404 with job_not_found.
	rr = doRequest(t, srv, "GET", "/jobs/9999/log")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing job, got %d", rr.Code)
	}
	var errResp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errResp["error"] != "job_not_found" {
		t.Errorf("error = %q, want %q", errResp["error"], "job_not_found")
	}

	// Job exists but has no log → 404 with log_not_found.
	task := &db.Job{
		DeploymentID: "test-deploy",
		IssueNumber:  50,
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusQueued,
	}
	if err := store.CreateJob(task); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	rr = doRequest(t, srv, "GET", fmt.Sprintf("/jobs/%d/log", task.ID))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing log, got %d", rr.Code)
	}
	errResp = nil
	if err := json.NewDecoder(rr.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errResp["error"] != "log_not_found" {
		t.Errorf("error = %q, want %q", errResp["error"], "log_not_found")
	}

	// Task with a valid log file → 200.
	logFile := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(logFile, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := store.DB().Exec("UPDATE jobs SET agent_log = ? WHERE id = ?", logFile, task.ID); err != nil {
		t.Fatalf("set agent_log: %v", err)
	}

	rr = doRequest(t, srv, "GET", fmt.Sprintf("/jobs/%d/log", task.ID))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if body != "line1\nline2\n" {
		t.Errorf("body = %q, want %q", body, "line1\nline2\n")
	}

	// Task with log path set but file missing → 404 with log_not_found.
	if _, err := store.DB().Exec("UPDATE jobs SET agent_log = ? WHERE id = ?", "/nonexistent/agent.log", task.ID); err != nil {
		t.Fatalf("set agent_log: %v", err)
	}

	rr = doRequest(t, srv, "GET", fmt.Sprintf("/jobs/%d/log", task.ID))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing log file, got %d", rr.Code)
	}
	errResp = nil
	if err := json.NewDecoder(rr.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errResp["error"] != "log_not_found" {
		t.Errorf("error = %q, want %q", errResp["error"], "log_not_found")
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

func TestStatusAfterDaemonRestart(t *testing.T) {
	// Simulate a daemon restart: first deploy creates jobs, then a second
	// deploy starts on the same DB. The plugin (hitting the new server)
	// should see the new deploy ID, an empty job list, and fresh uptime.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	store := db.NewStore(conn)

	// --- First deploy ---
	deploy1 := &db.Deployment{
		ID:             "deploy-old",
		RepoDir:        "/tmp/repo",
		Owner:          "acme",
		Repo:           "widgets",
		Mode:           "issues",
		MaxAgents:      3,
		TotalBudgetUSD: 25.0,
	}
	if err := store.CreateDeployment(deploy1); err != nil {
		t.Fatalf("CreateDeployment(old): %v", err)
	}

	// Add jobs to the old deploy.
	for i, title := range []string{"Fix auth", "Add tests"} {
		job := &db.Job{
			DeploymentID: "deploy-old",
			Agent:        "autopilot",
			Name:         fmt.Sprintf("issue-%d", i+1),
			IssueNumber:  i + 1,
			IssueTitle:   sql.NullString{String: title, Valid: true},
			Owner:        "acme",
			Repo:         "widgets",
			Status:       db.StatusRunning,
			CostUSD:      1.50,
		}
		if err := store.CreateJob(job); err != nil {
			t.Fatalf("CreateJob(%s): %v", title, err)
		}
	}

	srv1 := NewServer(ServerConfig{
		Store:    store,
		DeployID: "deploy-old",
	})

	// Verify first deploy returns its jobs.
	rr := doRequest(t, srv1, "GET", "/jobs")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /jobs (old): expected 200, got %d", rr.Code)
	}
	var oldJobs []JobResponse
	if err := json.NewDecoder(rr.Body).Decode(&oldJobs); err != nil {
		t.Fatalf("decode old jobs: %v", err)
	}
	if len(oldJobs) != 2 {
		t.Fatalf("old deploy: got %d jobs, want 2", len(oldJobs))
	}

	// Verify first deploy status.
	rr = doRequest(t, srv1, "GET", "/status")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /status (old): expected 200, got %d", rr.Code)
	}
	var oldStatus StatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&oldStatus); err != nil {
		t.Fatalf("decode old status: %v", err)
	}
	if oldStatus.DeployID != "deploy-old" {
		t.Errorf("old deploy_id = %q, want %q", oldStatus.DeployID, "deploy-old")
	}

	// --- Daemon restart: new deploy ---
	deploy2 := &db.Deployment{
		ID:             "deploy-new",
		RepoDir:        "/tmp/repo",
		Owner:          "acme",
		Repo:           "widgets",
		Mode:           "issues",
		MaxAgents:      5,
		TotalBudgetUSD: 50.0,
		BaseBranch:     "main",
	}
	if err := store.CreateDeployment(deploy2); err != nil {
		t.Fatalf("CreateDeployment(new): %v", err)
	}

	srv2 := NewServer(ServerConfig{
		Store:    store,
		DeployID: "deploy-new",
	})

	// /status should reflect the new deploy.
	rr = doRequest(t, srv2, "GET", "/status")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /status (new): expected 200, got %d", rr.Code)
	}
	var newStatus StatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&newStatus); err != nil {
		t.Fatalf("decode new status: %v", err)
	}
	if newStatus.DeployID != "deploy-new" {
		t.Errorf("new deploy_id = %q, want %q", newStatus.DeployID, "deploy-new")
	}
	if newStatus.TotalBudget != 50.0 {
		t.Errorf("new total_budget = %v, want 50", newStatus.TotalBudget)
	}
	if newStatus.Config.MaxAgents != 5 {
		t.Errorf("new config.max_agents = %d, want 5", newStatus.Config.MaxAgents)
	}
	if newStatus.TotalSpent != 0 {
		t.Errorf("new total_spent = %v, want 0 (no jobs yet)", newStatus.TotalSpent)
	}

	// /jobs should return empty list — old deploy's jobs must not leak.
	rr = doRequest(t, srv2, "GET", "/jobs")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /jobs (new): expected 200, got %d", rr.Code)
	}
	var newJobs []JobResponse
	if err := json.NewDecoder(rr.Body).Decode(&newJobs); err != nil {
		t.Fatalf("decode new jobs: %v", err)
	}
	if len(newJobs) != 0 {
		t.Errorf("new deploy: got %d jobs, want 0 (stale jobs leaking from old deploy)", len(newJobs))
	}

	// /metrics should also reflect zero jobs/cost.
	rr = doRequest(t, srv2, "GET", "/metrics")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /metrics (new): expected 200, got %d", rr.Code)
	}
	var metrics map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&metrics); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}
	if totalJobs, ok := metrics["total_jobs"].(float64); !ok || totalJobs != 0 {
		t.Errorf("new metrics.total_jobs = %v, want 0", metrics["total_jobs"])
	}
	if totalCost, ok := metrics["total_cost"].(float64); !ok || totalCost != 0 {
		t.Errorf("new metrics.total_cost = %v, want 0", metrics["total_cost"])
	}

	// /dep-graph should 404 — old deploy's graph must not carry over.
	rr = doRequest(t, srv2, "GET", "/dep-graph")
	if rr.Code != http.StatusNotFound {
		t.Errorf("GET /dep-graph (new): expected 404, got %d (stale dep graph leaking)", rr.Code)
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
