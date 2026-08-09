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
		Runtime:        "claude-code",
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

func TestLegacyRoutes_Shapes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv, store := testServer(t)

	logPath := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(logPath, []byte("planning\nworking\n"), 0o600); err != nil {
		t.Fatalf("write agent log: %v", err)
	}
	job := &db.Job{
		DeploymentID: "test-deploy",
		Agent:        "autopilot",
		Name:         "issue-42",
		IssueNumber:  42,
		IssueTitle:   sql.NullString{String: "Fix auth", Valid: true},
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusQueued,
	}
	if err := store.CreateJob(job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.DB().Exec("UPDATE jobs SET agent_log = ? WHERE id = ?", logPath, job.ID); err != nil {
		t.Fatalf("set agent log: %v", err)
	}

	status := decodeJSONResponse(t, doRequest(t, srv, "GET", "/status"))
	statusObject, ok := status.(map[string]any)
	if !ok {
		t.Fatalf("/status returned %T, want a JSON object", status)
	}
	if _, ok := statusObject["uptime_sec"]; !ok {
		t.Fatal("/status omitted uptime_sec")
	}
	if _, ok := statusObject["started_at"]; !ok {
		t.Fatal("/status omitted started_at")
	}
	statusObject["uptime_sec"] = float64(0)
	statusObject["started_at"] = "<timestamp>"
	assertGoldenJSON(t, "/status", statusObject, `{
  "deploy_id": "test-deploy",
  "alive": false,
  "pid": 0,
  "budget_paused": false,
  "uptime_sec": 0,
  "started_at": "<timestamp>",
  "total_spent": 0,
  "total_budget": 25,
  "config": {
    "max_agents": 3,
    "max_turns": 50,
    "max_budget": 5,
    "runtime": "claude-code",
    "model": "sonnet",
    "skip_label": "no-agent",
    "auto_merge": false,
    "base_branch": "main"
  }
}`)

	const jobGolden = `{
  "id": 1,
  "agent": "autopilot",
  "name": "issue-42",
  "title": "Fix auth",
  "issue_number": 42,
  "issue_title": "Fix auth",
  "owner": "acme",
  "repo": "widgets",
  "status": "queued",
  "runtime": "claude-code",
  "cost_usd": 0
}`
	jobs := decodeJSONResponse(t, doRequest(t, srv, "GET", "/jobs"))
	assertGoldenJSON(t, "/jobs", jobs, "["+jobGolden+"]")

	jobResponse := decodeJSONResponse(t, doRequest(t, srv, "GET", fmt.Sprintf("/jobs/%d", job.ID)))
	assertGoldenJSON(t, "/jobs/{id}", jobResponse, jobGolden)

	logResponse := doRequest(t, srv, "GET", fmt.Sprintf("/jobs/%d/log", job.ID))
	assertGoldenJSON(t, "/jobs/{id}/log", map[string]any{
		"status":       logResponse.Code,
		"content_type": logResponse.Header().Get("Content-Type"),
		"body":         logResponse.Body.String(),
	}, `{
  "status": 200,
  "content_type": "text/plain; charset=utf-8",
  "body": "planning\nworking\n"
}`)
}

func decodeJSONResponse(t *testing.T, rr *httptest.ResponseRecorder) any {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("response status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var value any
	if err := json.Unmarshal(rr.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode JSON response: %v; body: %s", err, rr.Body.String())
	}
	return value
}

func assertGoldenJSON(t *testing.T, route string, got any, golden string) {
	t.Helper()
	var want any
	if err := json.Unmarshal([]byte(golden), &want); err != nil {
		t.Fatalf("invalid %s golden JSON: %v", route, err)
	}
	gotJSON, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s response: %v", route, err)
	}
	wantJSON, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s golden response: %v", route, err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("%s response shape changed\n got: %s\nwant: %s", route, gotJSON, wantJSON)
	}
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
	if jobs[0].Runtime != "claude-code" {
		t.Errorf("runtime = %q, want claude-code", jobs[0].Runtime)
	}
}

func TestHandleTaskByID(t *testing.T) {
	srv, store := testServer(t)

	task := &db.Job{
		DeploymentID: "test-deploy",
		IssueNumber:  99,
		IssueTitle:   sql.NullString{String: "Add feature", Valid: true},
		Runtime:      sql.NullString{String: "codex", Valid: true},
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
	if resp.Runtime != "codex" {
		t.Errorf("runtime = %q, want codex", resp.Runtime)
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

func TestHandleJob_CrossDeploymentScoping(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	store := db.NewStore(conn)

	for _, id := range []string{"deploy-a", "deploy-b"} {
		deploy := &db.Deployment{
			ID:      id,
			RepoDir: "/tmp/repo",
			Owner:   "acme",
			Repo:    "widgets",
			Mode:    "issues",
		}
		if err := store.CreateDeployment(deploy); err != nil {
			t.Fatalf("CreateDeployment(%s): %v", id, err)
		}
	}

	logFile := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(logFile, []byte("secret log\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	jobA := &db.Job{
		DeploymentID: "deploy-a",
		Agent:        "autopilot",
		Name:         "issue-1",
		IssueNumber:  1,
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusQueued,
	}
	if err := store.CreateJob(jobA); err != nil {
		t.Fatalf("CreateJob(a): %v", err)
	}
	if _, err := store.DB().Exec("UPDATE jobs SET agent_log = ? WHERE id = ?", logFile, jobA.ID); err != nil {
		t.Fatalf("set agent_log: %v", err)
	}

	jobB := &db.Job{
		DeploymentID: "deploy-b",
		Agent:        "autopilot",
		Name:         "issue-2",
		IssueNumber:  2,
		Owner:        "acme",
		Repo:         "widgets",
		Status:       db.StatusQueued,
	}
	if err := store.CreateJob(jobB); err != nil {
		t.Fatalf("CreateJob(b): %v", err)
	}

	srvA := NewServer(ServerConfig{Store: store, DeployID: "deploy-a"})
	srvB := NewServer(ServerConfig{Store: store, DeployID: "deploy-b"})

	// Each server can see its own job.
	rr := doRequest(t, srvA, "GET", fmt.Sprintf("/jobs/%d", jobA.ID))
	if rr.Code != http.StatusOK {
		t.Fatalf("srvA own job: expected 200, got %d", rr.Code)
	}
	rr = doRequest(t, srvB, "GET", fmt.Sprintf("/jobs/%d", jobB.ID))
	if rr.Code != http.StatusOK {
		t.Fatalf("srvB own job: expected 200, got %d", rr.Code)
	}

	// Neither server can see the other's job or log.
	rr = doRequest(t, srvA, "GET", fmt.Sprintf("/jobs/%d", jobB.ID))
	if rr.Code != http.StatusNotFound {
		t.Errorf("srvA cross-deployment job: expected 404, got %d", rr.Code)
	}
	rr = doRequest(t, srvB, "GET", fmt.Sprintf("/jobs/%d", jobA.ID))
	if rr.Code != http.StatusNotFound {
		t.Errorf("srvB cross-deployment job: expected 404, got %d", rr.Code)
	}
	rr = doRequest(t, srvB, "GET", fmt.Sprintf("/jobs/%d/log", jobA.ID))
	if rr.Code != http.StatusNotFound {
		t.Errorf("srvB cross-deployment log: expected 404, got %d", rr.Code)
	}
	if bodyContains := rr.Body.String(); bodyContains != "" && bodyContains == "secret log\n" {
		t.Errorf("srvB leaked deploy-a log content: %q", bodyContains)
	}

	// Legacy /tasks/{id} alias is scoped the same way.
	rr = doRequest(t, srvA, "GET", fmt.Sprintf("/tasks/%d", jobB.ID))
	if rr.Code != http.StatusNotFound {
		t.Errorf("srvA cross-deployment task alias: expected 404, got %d", rr.Code)
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

	stopped := make(chan struct{})
	srv.StopDaemon = func() { close(stopped) }

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
	select {
	case <-stopped:
	case <-time.After(time.Second):
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

func TestConstantTimeEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"equal", "secret-key", "secret-key", true},
		{"differ mid", "secret-key", "secret-kex", false},
		{"differ first byte", "secret-key", "Xecret-key", false},
		{"shorter guess", "secret-key", "secret", false},
		{"longer guess", "secret-key", "secret-key-extra", false},
		{"empty guess", "secret-key", "", false},
		{"both empty", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := constantTimeEqual(tc.a, tc.b); got != tc.want {
				t.Fatalf("constantTimeEqual(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
