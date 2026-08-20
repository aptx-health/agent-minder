package daemon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/aptx-health/agent-minder/internal/coordinator"
	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/logresolve"
)

// Server is the HTTP API server embedded in the deploy daemon.
type Server struct {
	store             *db.Store
	deployID          string
	apiKey            string
	startTime         time.Time
	mux               *http.ServeMux
	srv               *http.Server
	unixSrv           *http.Server
	unixSocketMu      sync.Mutex
	unixSocketPath    string
	buildVersion      string
	v1Logger          *slog.Logger
	v1Limiter         V1RateLimiter
	v1ClientKey       func(*http.Request) string
	v1Encoder         func(io.Writer, any) error
	v1RequestID       func() string
	v1Streams         *StreamLimiter
	eventHeartbeat    time.Duration
	eventWriteTimeout time.Duration
	eventQueueSize    int
	eventBatchSize    int

	// Provider is the Coordinator-owned control surface (DM-5), wired by the
	// entry points. Handlers reach the deployment lifecycle only through it —
	// never through *supervisor.Supervisor. Nil-safe: control routes no-op and
	// status reports budget_paused=false, matching the unwired legacy behavior.
	Provider coordinator.StateProvider
}

// ServerConfig holds configuration for the API server.
type ServerConfig struct {
	Store                  *db.Store
	DeployID               string
	APIKey                 string
	BuildVersion           string
	Logger                 *slog.Logger
	V1Limiter              V1RateLimiter
	V1ClientKey            func(*http.Request) string
	V1Encoder              func(io.Writer, any) error
	V1RequestID            func() string
	V1StreamLimiter        *StreamLimiter
	EventHeartbeatInterval time.Duration
	EventWriteTimeout      time.Duration
	EventQueueSize         int
	EventBatchSize         int
	RateLimitPerMinute     int
	RateLimitBurst         int
}

// NewServer creates a new API server.
func NewServer(cfg ServerConfig) *Server {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	limiter := cfg.V1Limiter
	if limiter == nil {
		limiter = NewTokenBucketLimiter(cfg.RateLimitPerMinute, cfg.RateLimitBurst)
	}
	streamLimiter := cfg.V1StreamLimiter
	if streamLimiter == nil {
		streamLimiter = processV1StreamLimiter
	}
	heartbeat := cfg.EventHeartbeatInterval
	if heartbeat <= 0 {
		heartbeat = defaultEventHeartbeatInterval
	}
	writeTimeout := cfg.EventWriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = defaultEventWriteTimeout
	}
	queueSize := cfg.EventQueueSize
	if queueSize <= 0 {
		queueSize = defaultEventQueueSize
	}
	batchSize := cfg.EventBatchSize
	if batchSize <= 0 {
		batchSize = defaultEventBatchSize
	}
	s := &Server{
		store:             cfg.Store,
		deployID:          cfg.DeployID,
		apiKey:            cfg.APIKey,
		startTime:         time.Now(),
		mux:               http.NewServeMux(),
		buildVersion:      cfg.BuildVersion,
		v1Logger:          logger,
		v1Limiter:         limiter,
		v1ClientKey:       cfg.V1ClientKey,
		v1Encoder:         cfg.V1Encoder,
		v1RequestID:       cfg.V1RequestID,
		v1Streams:         streamLimiter,
		eventHeartbeat:    heartbeat,
		eventWriteTimeout: writeTimeout,
		eventQueueSize:    queueSize,
		eventBatchSize:    batchSize,
	}

	s.mux.HandleFunc("GET /status", s.handleStatus)
	s.mux.HandleFunc("GET /jobs", s.handleJobs)
	s.mux.HandleFunc("GET /jobs/{id}", s.handleJob)
	s.mux.HandleFunc("GET /jobs/{id}/log", s.handleJobLog)
	// Backward compat aliases.
	s.mux.HandleFunc("GET /tasks", s.handleJobs)
	s.mux.HandleFunc("GET /tasks/{id}", s.handleJob)
	s.mux.HandleFunc("GET /dep-graph", s.handleDepGraph)
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)
	s.mux.HandleFunc("GET /lessons", s.handleLessons)
	s.mux.HandleFunc("POST /stop", s.handleStop)
	s.mux.HandleFunc("POST /resume", s.handleResume)
	v1Mux := http.NewServeMux()
	v1Mux.HandleFunc("GET /api/v1/meta", s.handleV1Meta)
	v1Mux.HandleFunc("GET /api/v1/events", s.handleV1Events)
	v1Mux.HandleFunc("GET /api/v1/deployments", s.handleV1Deployments)
	v1Mux.HandleFunc("GET /api/v1/deployments/{deployment_id}", s.handleV1Deployment)
	v1Mux.HandleFunc("GET /api/v1/deployments/{deployment_id}/automations", s.handleV1Automations)
	v1Mux.HandleFunc("GET /api/v1/deployments/{deployment_id}/jobs", s.handleV1Jobs)
	v1Mux.HandleFunc("GET /api/v1/deployments/{deployment_id}/jobs/{job_id}", s.handleV1Job)
	v1Mux.HandleFunc("GET /api/v1/deployments/{deployment_id}/jobs/{job_id}/runs", s.handleV1Runs)
	v1Mux.HandleFunc("GET /api/v1/deployments/{deployment_id}/jobs/{job_id}/logs", s.handleV1Logs)
	s.mux.Handle("/api/v1/", s.v1Middleware(v1Mux))

	s.srv = &http.Server{
		Handler:      s.middleware(s.mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	// Unix socket server shares the same routes but skips the API-key check:
	// filesystem permissions on the socket file (0600) are the auth boundary.
	s.unixSrv = &http.Server{
		Handler:      s.unixMiddleware(s.mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return s
}

// ListenAndServe starts the TCP server. Requires an API key (opt-in surface).
func (s *Server) ListenAndServe(addr string) error {
	if s.apiKey == "" {
		return fmt.Errorf("api key required")
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	log.Printf("API server listening on %s", ln.Addr())
	return s.srv.Serve(ln)
}

// Shutdown gracefully stops the TCP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// ListenAndServeUnix starts the API server on the deploy-ID-keyed Unix
// domain socket under ~/.agent-minder/deploys, default-on for every worker.
// The socket file is created 0600; if a stale socket is left behind by a
// crashed process (heartbeat/PID show it is dead), it is removed and
// recreated — the same discipline as stale PID-file handling. Blocks until
// the server is shut down or fails; call in a goroutine.
func (s *Server) ListenAndServeUnix(deployID string) error {
	path := SocketPath(deployID)
	ln, err := bindUnixSocket(deployID, path)
	if err != nil {
		return err
	}
	s.unixSocketMu.Lock()
	s.unixSocketPath = path
	s.unixSocketMu.Unlock()

	log.Printf("API server listening on unix socket %s", path)
	err = s.unixSrv.Serve(ln)
	_ = os.Remove(path)
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// ShutdownUnix gracefully stops the Unix socket server and removes the
// socket file.
func (s *Server) ShutdownUnix(ctx context.Context) error {
	err := s.unixSrv.Shutdown(ctx)
	s.unixSocketMu.Lock()
	path := s.unixSocketPath
	s.unixSocketMu.Unlock()
	if path != "" {
		_ = os.Remove(path)
	}
	return err
}

// bindUnixSocket binds a Unix domain socket at path, recovering from a
// stale socket left by a crashed process for the given deployment. If the
// owning process is still alive (per PID/heartbeat), binding is refused
// rather than clobbering a live server.
func bindUnixSocket(deployID, path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("mkdir socket dir: %w", err)
	}

	if _, err := os.Stat(path); err == nil {
		if alive, _ := IsRunning(deployID); alive {
			return nil, fmt.Errorf("socket %s already in use by a running daemon", path)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen unix %s: %w", path, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	return ln, nil
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "X-API-Key, Content-Type, Last-Event-ID")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if s.apiKey == "" || !constantTimeEqual(r.Header.Get("X-API-Key"), s.apiKey) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// unixMiddleware wraps the Unix-socket handler chain. No API key check: the
// socket file's 0600 permissions are the auth boundary (design per #650).
// CORS headers are omitted — a browser cannot dial a Unix socket.
func (s *Server) unixMiddleware(next http.Handler) http.Handler {
	return next
}

// constantTimeEqual reports whether the two strings are equal using a
// constant-time comparison. Both sides are hashed with SHA-256 first so the
// comparison runs over fixed-size digests, avoiding both the byte-by-byte
// timing side-channel of `==` (CWE-208) and any leak of the secret's length.
func constantTimeEqual(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// --- Handlers ---

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	deploy, err := s.store.GetDeployment(s.deployID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	alive, pid := IsRunning(s.deployID)
	budgetPaused := false
	if s.Provider != nil {
		budgetPaused = s.Provider.IsBudgetPaused()
	}

	spent, _ := s.store.TotalSpend(s.deployID)

	writeJSON(w, http.StatusOK, StatusResponse{
		DeployID:     s.deployID,
		Alive:        alive,
		PID:          pid,
		BudgetPaused: budgetPaused,
		UptimeSec:    int(time.Since(s.startTime).Seconds()),
		StartedAt:    s.startTime.UTC().Format(time.RFC3339),
		TotalSpent:   spent,
		TotalBudget:  deploy.TotalBudgetUSD,
		Config: DeployConfig{
			MaxAgents:     deploy.MaxAgents,
			MaxTurns:      deploy.MaxTurns,
			MaxBudget:     deploy.MaxBudgetUSD,
			Runtime:       deploy.Runtime,
			AnalyzerModel: deploy.AnalyzerModel,
			SkipLabel:     deploy.SkipLabel,
			AutoMerge:     deploy.AutoMerge,
			BaseBranch:    deploy.BaseBranch,
		},
	})
}

func (s *Server) handleJobs(w http.ResponseWriter, _ *http.Request) {
	jobs, err := s.store.GetJobs(s.deployID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	resp := make([]JobResponse, 0, len(jobs))
	deploy, _ := s.store.GetDeployment(s.deployID)
	for _, j := range jobs {
		resp = append(resp, jobToResponse(j, deploy))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid job id"})
		return
	}

	job, err := s.store.GetJobForDeployment(s.deployID, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}

	deploy, _ := s.store.GetDeployment(s.deployID)
	writeJSON(w, http.StatusOK, jobToResponse(job, deploy))
}

func (s *Server) handleJobLog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid job id"})
		return
	}

	job, err := s.store.GetJobForDeployment(s.deployID, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job_not_found"})
		return
	}
	// Legacy route: always resolves to the job's latest log (C-3). Run-scoped
	// addressing (stage/attempt) is reserved for the future /api/v1 runs/logs
	// resources; this route keeps its current shape.
	logPath, _, err := logresolve.Resolve(s.store, job, logresolve.Selector{})
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "log_not_found"})
		return
	}

	f, err := os.Open(logPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "log_not_found"})
		return
	}
	defer func() { _ = f.Close() }()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		_, _ = w.Write(scanner.Bytes())
		_, _ = w.Write([]byte("\n"))
	}

	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *Server) handleDepGraph(w http.ResponseWriter, _ *http.Request) {
	dg, err := s.store.GetDepGraph(s.deployID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no dep graph"})
		return
	}

	writeJSON(w, http.StatusOK, DepGraphResponse{
		GraphJSON:  dg.GraphJSON,
		OptionName: dg.OptionName.String,
		Reasoning:  dg.Reasoning.String,
		CreatedAt:  dg.CreatedAt.Format(time.RFC3339),
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	jobs, _ := s.store.GetJobs(s.deployID)
	spent, _ := s.store.TotalSpend(s.deployID)

	var totalCost float64
	statusCounts := make(map[string]int)
	for _, j := range jobs {
		statusCounts[j.Status]++
		totalCost += j.CostUSD
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total_jobs":    len(jobs),
		"total_cost":    totalCost,
		"total_spend":   spent,
		"status_counts": statusCounts,
		"uptime_sec":    int(time.Since(s.startTime).Seconds()),
	})
}

func (s *Server) handleLessons(w http.ResponseWriter, r *http.Request) {
	deploy, err := s.store.GetDeployment(s.deployID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	scope := deploy.Owner + "/" + deploy.Repo
	lessons, err := s.store.GetActiveLessons(scope)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	resp := make([]LessonResponse, 0, len(lessons))
	for _, l := range lessons {
		resp = append(resp, LessonResponse{
			ID:             l.ID,
			Content:        l.Content,
			Source:         l.Source,
			RepoScope:      l.RepoScope.String,
			Active:         l.Active,
			Pinned:         l.Pinned,
			TimesInjected:  l.TimesInjected,
			TimesHelpful:   l.TimesHelpful,
			TimesUnhelpful: l.TimesUnhelpful,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleStop(w http.ResponseWriter, _ *http.Request) {
	if s.Provider != nil {
		go s.Provider.Stop()
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
}

func (s *Server) handleResume(w http.ResponseWriter, _ *http.Request) {
	if s.Provider != nil {
		s.Provider.ResumeBudget()
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

// --- Response types ---

// StatusResponse is the JSON shape for GET /status.
type StatusResponse struct {
	DeployID     string       `json:"deploy_id"`
	Alive        bool         `json:"alive"`
	PID          int          `json:"pid"`
	BudgetPaused bool         `json:"budget_paused"`
	UptimeSec    int          `json:"uptime_sec"`
	StartedAt    string       `json:"started_at"`
	TotalSpent   float64      `json:"total_spent"`
	TotalBudget  float64      `json:"total_budget"`
	Config       DeployConfig `json:"config"`
}

type DeployConfig struct {
	MaxAgents int     `json:"max_agents"`
	MaxTurns  int     `json:"max_turns"`
	MaxBudget float64 `json:"max_budget"`
	Runtime   string  `json:"runtime"`
	// AnalyzerModel is the model used for dependency-graph resolution, review
	// assessment, and other analysis calls (internal/claudecli). It is NOT the
	// model doer agents run with — see docs/research/fable-expedition/05-runtime-conformance-and-resolution.md
	// §4.3 / §7 leak L3. Doers currently have no deployment-level model
	// override (jobs.model / agent frontmatter only).
	AnalyzerModel string `json:"analyzer_model"`
	SkipLabel     string `json:"skip_label"`
	AutoMerge     bool   `json:"auto_merge"`
	BaseBranch    string `json:"base_branch"`
}

// JobResponse is the JSON shape for job endpoints.
type JobResponse struct {
	ID           int64   `json:"id"`
	Agent        string  `json:"agent"`
	Name         string  `json:"name"`
	Title        string  `json:"title"`
	IssueNumber  int     `json:"issue_number,omitempty"`
	IssueTitle   string  `json:"issue_title,omitempty"` // deprecated: use title
	Owner        string  `json:"owner"`
	Repo         string  `json:"repo"`
	Status       string  `json:"status"`
	CurrentStage string  `json:"current_stage,omitempty"`
	Runtime      string  `json:"runtime,omitempty"`
	Model        string  `json:"model,omitempty"`
	PRNumber     int     `json:"pr_number,omitempty"`
	CostUSD      float64 `json:"cost_usd"`
	Branch       string  `json:"branch,omitempty"`
	StartedAt    string  `json:"started_at,omitempty"`
	CompletedAt  string  `json:"completed_at,omitempty"`
	FailReason   string  `json:"failure_reason,omitempty"`
	FailDetail   string  `json:"failure_detail,omitempty"`
	ReviewRisk   string  `json:"review_risk,omitempty"`
}

// DepGraphResponse is the JSON shape for GET /dep-graph.
type DepGraphResponse struct {
	GraphJSON  string `json:"graph_json"`
	OptionName string `json:"option_name"`
	Reasoning  string `json:"reasoning,omitempty"`
	CreatedAt  string `json:"created_at"`
}

// LessonResponse is the JSON shape for GET /lessons.
type LessonResponse struct {
	ID             int64  `json:"id"`
	Content        string `json:"content"`
	Source         string `json:"source"`
	RepoScope      string `json:"repo_scope,omitempty"`
	Active         bool   `json:"active"`
	Pinned         bool   `json:"pinned"`
	TimesInjected  int    `json:"times_injected"`
	TimesHelpful   int    `json:"times_helpful"`
	TimesUnhelpful int    `json:"times_unhelpful"`
}

// --- Helpers ---

func jobToResponse(j *db.Job, deploy *db.Deployment) JobResponse {
	// Title: prefer issue title for reactive jobs, fall back to job name.
	title := j.IssueTitle.String
	if title == "" {
		title = j.Name
	}

	resp := JobResponse{
		ID:           j.ID,
		Agent:        j.Agent,
		Name:         j.Name,
		Title:        title,
		IssueNumber:  j.IssueNumber,
		IssueTitle:   j.IssueTitle.String, // deprecated compat
		Owner:        j.Owner,
		Repo:         j.Repo,
		Status:       j.Status,
		CurrentStage: j.CurrentStage.String,
		Runtime:      j.EffectiveRuntime(deploy),
		Model:        j.EffectiveModel(),
		CostUSD:      j.CostUSD,
		Branch:       j.Branch.String,
		FailReason:   j.FailureReason.String,
		FailDetail:   j.FailureDetail.String,
		ReviewRisk:   j.ReviewRisk.String,
	}
	if j.PRNumber.Valid {
		resp.PRNumber = int(j.PRNumber.Int64)
	}
	if j.StartedAt.Valid {
		resp.StartedAt = j.StartedAt.Time.Format(time.RFC3339)
	}
	if j.CompletedAt.Valid {
		resp.CompletedAt = j.CompletedAt.Time.Format(time.RFC3339)
	}
	return resp
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
