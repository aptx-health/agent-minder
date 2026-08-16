package daemon

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aptx-health/agent-minder/internal/controlapi"
	"github.com/aptx-health/agent-minder/internal/coordinator"
	"github.com/aptx-health/agent-minder/internal/db"
)

type metaProvider struct {
	coordinator.StateProvider
	marker      coordinator.SnapshotMarker
	incarnation string
	err         error
	panicRead   bool
}

func (p *metaProvider) SnapshotMarker() (coordinator.SnapshotMarker, error) {
	if p.panicRead {
		panic("provider exploded")
	}
	return p.marker, p.err
}

func (p *metaProvider) WorkerIncarnation() string { return p.incarnation }

type readProvider struct {
	coordinator.StateProvider
	deployment  *db.Deployment
	automations []coordinator.Automation
	jobs        []*db.Job
	runs        []*db.AgentRun
	logs        []coordinator.LogRead
	marker      coordinator.SnapshotMarker
	config      coordinator.ConfigRevision
	incarnation string
}

type failingReadProvider struct {
	*readProvider
	err error
}

func (p *failingReadProvider) ReadJobsSnapshot() (coordinator.JobsRead, error) {
	return coordinator.JobsRead{}, p.err
}

func newReadProvider() *readProvider {
	at := time.Date(2026, 8, 15, 18, 34, 56, 123456789, time.UTC)
	deployment := &db.Deployment{
		ID: "deploy-1", Owner: "acme", Repo: "widgets", Mode: "issues",
		ActivationPolicy: db.ActivationExplicit, StartedAt: at, Runtime: "codex",
		MaxTurns: 50, MaxBudgetUSD: 5,
	}
	job := &db.Job{
		ID: 7, DeploymentID: deployment.ID, Name: "issue-648", Agent: "autopilot",
		IssueNumber: 648, IssueTitle: sql.NullString{String: "Read endpoints", Valid: true},
		Owner: "acme", Repo: "widgets", Status: db.StatusRunning,
		CurrentStage: sql.NullString{String: "implement", Valid: true},
		QueuedAt:     sql.NullTime{Time: at, Valid: true}, StartedAt: sql.NullTime{Time: at, Valid: true},
	}
	run := &db.AgentRun{
		ID: 9, JobID: job.ID, Stage: "implement", Attempt: 1, Agent: "autopilot",
		Runtime: sql.NullString{String: "codex", Valid: true}, Status: db.RunStatusRunning,
		StepCount: 3, StartedAt: sql.NullTime{Time: at, Valid: true},
		LastActivityAt: sql.NullTime{Time: at, Valid: true},
	}
	return &readProvider{
		deployment: deployment,
		automations: []coordinator.Automation{{
			Kind: coordinator.AutomationCron, Name: "daily", Expression: "0 9 * * *",
			Agent: "autopilot", EffectiveRuntime: "codex", EffectiveModel: "gpt-5.6",
			EffectiveMaxTurns: 50, EffectiveMaxBudget: 5, Enabled: true,
		}},
		jobs: []*db.Job{job}, runs: []*db.AgentRun{run},
		logs:        []coordinator.LogRead{{Run: run, Available: true}},
		marker:      coordinator.SnapshotMarker{Watermark: 42, LogEpoch: "epoch-1"},
		config:      coordinator.ConfigRevision{SHA256: "revision-1", LoadedAt: at, HasConfig: true},
		incarnation: "incarnation-1",
	}
}

func (p *readProvider) DeploymentID() string                       { return p.deployment.ID }
func (p *readProvider) WorkerIncarnation() string                  { return p.incarnation }
func (p *readProvider) ConfigRevision() coordinator.ConfigRevision { return p.config }
func (p *readProvider) ActivationPolicy() db.ActivationPolicy      { return p.deployment.ActivationPolicy }
func (p *readProvider) SnapshotMarker() (coordinator.SnapshotMarker, error) {
	return p.marker, nil
}
func (p *readProvider) ReadDeploymentSnapshot() (coordinator.DeploymentRead, error) {
	return coordinator.DeploymentRead{Deployment: p.deployment, Marker: p.marker}, nil
}
func (p *readProvider) ReadAutomationsSnapshot() (coordinator.AutomationsRead, error) {
	return coordinator.AutomationsRead{Automations: p.automations, Marker: p.marker}, nil
}
func (p *readProvider) ReadJobsSnapshot() (coordinator.JobsRead, error) {
	return coordinator.JobsRead{Deployment: p.deployment, Jobs: p.jobs, Marker: p.marker}, nil
}
func (p *readProvider) ReadJobSnapshot(id int64) (coordinator.JobRead, error) {
	if len(p.jobs) == 0 || p.jobs[0].ID != id {
		return coordinator.JobRead{}, coordinator.ErrNotFound
	}
	return coordinator.JobRead{Deployment: p.deployment, Job: p.jobs[0], Marker: p.marker}, nil
}
func (p *readProvider) ReadAgentRunsSnapshot(jobID int64) (coordinator.AgentRunsRead, error) {
	if len(p.jobs) == 0 || p.jobs[0].ID != jobID {
		return coordinator.AgentRunsRead{}, coordinator.ErrNotFound
	}
	return coordinator.AgentRunsRead{Runs: p.runs, Marker: p.marker}, nil
}
func (p *readProvider) ReadLogsSnapshot(jobID int64) (coordinator.LogsRead, error) {
	if len(p.jobs) == 0 || p.jobs[0].ID != jobID {
		return coordinator.LogsRead{}, coordinator.ErrNotFound
	}
	return coordinator.LogsRead{Logs: p.logs, Marker: p.marker}, nil
}

type fixedLimiter struct {
	allowed bool
	retry   time.Duration
	key     string
}

func (l *fixedLimiter) Allow(key string, _ time.Time) (bool, time.Duration) {
	l.key = key
	return l.allowed, l.retry
}

func newV1TestServer(provider coordinator.StateProvider, overrides func(*ServerConfig)) *Server {
	cfg := ServerConfig{
		DeployID: "deploy-1", APIKey: "secret", BuildVersion: "1.2.3",
		V1RequestID: func() string { return "request-1" },
	}
	if overrides != nil {
		overrides(&cfg)
	}
	server := NewServer(cfg)
	server.Provider = provider
	return server
}

func requestV1(server *Server, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-API-Key", "secret")
	recorder := httptest.NewRecorder()
	server.middleware(server.mux).ServeHTTP(recorder, req)
	return recorder
}

func TestV1MetaGolden(t *testing.T) {
	provider := &metaProvider{
		marker:      coordinator.SnapshotMarker{Watermark: 42, LogEpoch: "epoch-1"},
		incarnation: "incarnation-1",
	}
	server := newV1TestServer(provider, nil)
	recorder := requestV1(server, "/api/v1/meta")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Request-ID"); got != "request-1" {
		t.Fatalf("X-Request-ID = %q", got)
	}
	want, err := os.ReadFile("testdata/meta.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var gotJSON, wantJSON any
	if err := json.Unmarshal(recorder.Body.Bytes(), &gotJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("meta changed\n got: %s\nwant: %s", recorder.Body.Bytes(), want)
	}
}

func TestV1ReadEndpointsGolden(t *testing.T) {
	server := newV1TestServer(newReadProvider(), nil)
	routes := map[string]string{
		"deployments": "/api/v1/deployments",
		"deployment":  "/api/v1/deployments/deploy-1",
		"automations": "/api/v1/deployments/deploy-1/automations",
		"jobs":        "/api/v1/deployments/deploy-1/jobs",
		"job":         "/api/v1/deployments/deploy-1/jobs/7",
		"runs":        "/api/v1/deployments/deploy-1/jobs/7/runs",
		"logs":        "/api/v1/deployments/deploy-1/jobs/7/logs",
	}
	got := make(map[string]any, len(routes))
	for name, route := range routes {
		recorder := requestV1(server, route)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", route, recorder.Code, recorder.Body.String())
		}
		var value any
		if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
			t.Fatalf("decode %s: %v", route, err)
		}
		got[name] = value
	}
	assertV1GoldenFile(t, "testdata/read_endpoints.golden.json", got)
}

func assertV1GoldenFile(t *testing.T, path string, value any) {
	t.Helper()
	wantData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	gotData, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	var got, want any
	if err := json.Unmarshal(gotData, &got); err != nil {
		t.Fatalf("decode actual: %v", err)
	}
	if err := json.Unmarshal(wantData, &want); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		prettyGot, _ := json.MarshalIndent(got, "", "  ")
		prettyWant, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("contract changed\n got: %s\nwant: %s", prettyGot, prettyWant)
	}
}

func TestV1DeliverablesPendingAndRouteAbsent(t *testing.T) {
	server := newV1TestServer(newReadProvider(), nil)
	metaRecorder := requestV1(server, "/api/v1/meta")
	var meta controlapi.ResourceEnvelope[controlapi.Meta]
	if err := json.Unmarshal(metaRecorder.Body.Bytes(), &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if slices.Contains(meta.Data.Capabilities, controlapi.CapabilityDeliverables) {
		t.Fatalf("deliverables advertised as implemented: %v", meta.Data.Capabilities)
	}
	if !slices.Contains(meta.Data.PendingCapabilities, controlapi.CapabilityDeliverables) {
		t.Fatalf("deliverables not advertised as pending: %v", meta.Data.PendingCapabilities)
	}
	recorder := requestV1(server, "/api/v1/deployments/deploy-1/jobs/7/deliverables")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("deliverables status = %d, want 404", recorder.Code)
	}
}

func TestV1ReadEndpointScopingAndValidation(t *testing.T) {
	server := newV1TestServer(newReadProvider(), nil)
	for _, route := range []string{
		"/api/v1/deployments/foreign",
		"/api/v1/deployments/foreign/automations",
		"/api/v1/deployments/foreign/jobs",
		"/api/v1/deployments/foreign/jobs/7",
		"/api/v1/deployments/foreign/jobs/7/runs",
		"/api/v1/deployments/foreign/jobs/7/logs",
		"/api/v1/deployments/deploy-1/jobs/999",
		"/api/v1/deployments/deploy-1/jobs/not-a-number",
	} {
		recorder := requestV1(server, route)
		assertV1Error(t, recorder, http.StatusNotFound, controlapi.ErrorNotFound)
	}
	for _, route := range []string{
		"/api/v1/deployments?limit=0",
		"/api/v1/deployments/deploy-1/jobs?cursor=malformed",
	} {
		recorder := requestV1(server, route)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400; body: %s", route, recorder.Code, recorder.Body.String())
		}
	}
}

func TestV1ReadEndpointFailuresUseStableErrors(t *testing.T) {
	provider := &failingReadProvider{readProvider: newReadProvider(), err: errors.New("sqlite details must stay private")}
	server := newV1TestServer(provider, nil)
	recorder := requestV1(server, "/api/v1/deployments/deploy-1/jobs")
	assertV1Error(t, recorder, http.StatusServiceUnavailable, controlapi.ErrorProviderUnavailable)
	if strings.Contains(recorder.Body.String(), "sqlite") {
		t.Fatalf("response exposed provider error: %s", recorder.Body.String())
	}

	server = newV1TestServer(newReadProvider(), func(cfg *ServerConfig) {
		cfg.V1Encoder = func(io.Writer, any) error { return errors.New("encoder failure") }
	})
	recorder = requestV1(server, "/api/v1/deployments")
	assertV1Error(t, recorder, http.StatusInternalServerError, controlapi.ErrorInternal)
}

func TestV1TwoDeploymentIsolation(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "control-api.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	store := db.NewStore(conn)
	deployA := &db.Deployment{
		ID: "deploy-a", RepoDir: t.TempDir(), Owner: "acme", Repo: "alpha", Mode: "issues",
		Runtime: "codex", MaxTurns: 50, MaxBudgetUSD: 5, ActivationPolicy: db.ActivationExplicit,
	}
	deployB := &db.Deployment{
		ID: "deploy-b", RepoDir: t.TempDir(), Owner: "acme", Repo: "beta", Mode: "issues",
		Runtime: "codex", MaxTurns: 50, MaxBudgetUSD: 5, ActivationPolicy: db.ActivationExplicit,
	}
	for _, deployment := range []*db.Deployment{deployA, deployB} {
		if err := store.CreateDeployment(deployment); err != nil {
			t.Fatalf("create deployment %s: %v", deployment.ID, err)
		}
	}
	jobA := &db.Job{DeploymentID: deployA.ID, Agent: "autopilot", Name: "issue-1", Owner: "acme", Repo: "alpha", Status: db.StatusQueued}
	jobB := &db.Job{DeploymentID: deployB.ID, Agent: "autopilot", Name: "issue-2", Owner: "acme", Repo: "beta", Status: db.StatusQueued}
	for _, job := range []*db.Job{jobA, jobB} {
		if err := store.CreateJob(job); err != nil {
			t.Fatalf("create job %s: %v", job.Name, err)
		}
	}
	for _, run := range []*db.AgentRun{
		{JobID: jobA.ID, Stage: "implement", Attempt: 1, Agent: "autopilot", LogPath: sql.NullString{String: "/tmp/a.log", Valid: true}},
		{JobID: jobB.ID, Stage: "implement", Attempt: 1, Agent: "autopilot", LogPath: sql.NullString{String: "/tmp/b.log", Valid: true}},
	} {
		if err := store.StartAgentRun(run); err != nil {
			t.Fatalf("create run: %v", err)
		}
	}
	coordA, err := coordinator.New(coordinator.Options{Store: store, Deploy: deployA})
	if err != nil {
		t.Fatalf("create coordinator: %v", err)
	}
	server := newV1TestServer(coordA, nil)

	var deployments controlapi.CollectionEnvelope[controlapi.Deployment]
	decodeV1Response(t, requestV1(server, "/api/v1/deployments"), &deployments)
	if len(deployments.Data) != 1 || deployments.Data[0].ID != deployA.ID {
		t.Fatalf("deployment list leaked shared state: %#v", deployments.Data)
	}
	var jobs controlapi.CollectionEnvelope[controlapi.Job]
	decodeV1Response(t, requestV1(server, "/api/v1/deployments/deploy-a/jobs"), &jobs)
	if len(jobs.Data) != 1 || jobs.Data[0].ID != jobA.ID {
		t.Fatalf("job list leaked shared state: %#v", jobs.Data)
	}

	for _, route := range []string{
		"/api/v1/deployments/deploy-b",
		"/api/v1/deployments/deploy-b/automations",
		"/api/v1/deployments/deploy-b/jobs",
		fmt.Sprintf("/api/v1/deployments/deploy-a/jobs/%d", jobB.ID),
		fmt.Sprintf("/api/v1/deployments/deploy-a/jobs/%d/runs", jobB.ID),
		fmt.Sprintf("/api/v1/deployments/deploy-a/jobs/%d/logs", jobB.ID),
	} {
		assertV1Error(t, requestV1(server, route), http.StatusNotFound, controlapi.ErrorNotFound)
	}
}

func decodeV1Response(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func TestV1MetaProviderUnavailable(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider coordinator.StateProvider
	}{
		{name: "nil"},
		{name: "failing", provider: &metaProvider{err: errors.New("sqlite details must stay private")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newV1TestServer(test.provider, nil)
			recorder := requestV1(server, "/api/v1/meta")
			assertV1Error(t, recorder, http.StatusServiceUnavailable, controlapi.ErrorProviderUnavailable)
			if strings.Contains(recorder.Body.String(), "sqlite") {
				t.Fatalf("response exposed provider error: %s", recorder.Body.String())
			}
		})
	}
}

func TestV1MetaEncodingFailure(t *testing.T) {
	provider := &metaProvider{marker: coordinator.SnapshotMarker{LogEpoch: "epoch"}, incarnation: "inc"}
	server := newV1TestServer(provider, func(cfg *ServerConfig) {
		cfg.V1Encoder = func(io.Writer, any) error { return errors.New("encoder failure") }
	})
	recorder := requestV1(server, "/api/v1/meta")
	assertV1Error(t, recorder, http.StatusInternalServerError, controlapi.ErrorInternal)
}

func TestV1PanicRecoveryKeepsServerAlive(t *testing.T) {
	provider := &metaProvider{panicRead: true}
	server := newV1TestServer(provider, nil)
	recorder := requestV1(server, "/api/v1/meta")
	assertV1Error(t, recorder, http.StatusInternalServerError, controlapi.ErrorInternal)

	provider.panicRead = false
	provider.marker = coordinator.SnapshotMarker{LogEpoch: "epoch"}
	provider.incarnation = "inc"
	if next := requestV1(server, "/api/v1/meta"); next.Code != http.StatusOK {
		t.Fatalf("server did not survive panic: status %d", next.Code)
	}
}

func TestV1StructuredRequestLog(t *testing.T) {
	var logOutput bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logOutput, nil))
	provider := &metaProvider{err: errors.New("internal database failure")}
	server := newV1TestServer(provider, func(cfg *ServerConfig) { cfg.Logger = logger })
	_ = requestV1(server, "/api/v1/meta")

	var record map[string]any
	lines := strings.Split(strings.TrimSpace(logOutput.String()), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &record); err != nil {
		t.Fatalf("decode log: %v; %s", err, logOutput.String())
	}
	for key, want := range map[string]any{
		"request_id": "request-1", "method": "GET", "route": "GET /api/v1/meta",
		"deployment_id": "deploy-1", "status": float64(http.StatusServiceUnavailable),
		"internal_error": "internal database failure",
	} {
		if got := record[key]; got != want {
			t.Errorf("log[%s] = %#v, want %#v", key, got, want)
		}
	}
	if _, ok := record["duration"]; !ok {
		t.Error("log omitted duration")
	}
}

func TestV1RateLimit(t *testing.T) {
	limiter := &fixedLimiter{allowed: false, retry: 1500 * time.Millisecond}
	server := newV1TestServer(&metaProvider{}, func(cfg *ServerConfig) {
		cfg.V1Limiter = limiter
		cfg.V1ClientKey = func(*http.Request) string { return "client-1" }
	})
	recorder := requestV1(server, "/api/v1/meta")
	assertV1Error(t, recorder, http.StatusTooManyRequests, controlapi.ErrorRateLimited)
	if got := recorder.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}
	if limiter.key != "client-1" {
		t.Fatalf("limiter key = %q", limiter.key)
	}
}

func TestTokenBucketDefaults(t *testing.T) {
	limiter := NewTokenBucketLimiter(0, 0)
	now := time.Unix(0, 0)
	for i := 0; i < defaultRateLimitBurst; i++ {
		if ok, _ := limiter.Allow("client", now); !ok {
			t.Fatalf("request %d unexpectedly rejected", i+1)
		}
	}
	if ok, retry := limiter.Allow("client", now); ok || retry != time.Second {
		t.Fatalf("burst+1 = allowed %v retry %s", ok, retry)
	}
}

func assertV1Error(t *testing.T, recorder *httptest.ResponseRecorder, status int, code controlapi.ErrorCode) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", recorder.Code, status, recorder.Body.String())
	}
	var envelope controlapi.ErrorEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if envelope.Error.Code != code {
		t.Fatalf("code = %q, want %q", envelope.Error.Code, code)
	}
}
