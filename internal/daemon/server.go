package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
)

// Server is the HTTP API server embedded in the deploy daemon.
type Server struct {
	store     *db.Store
	deployID  string
	apiKey    string
	startTime time.Time
	mux       *http.ServeMux
	srv       *http.Server

	// Callbacks wired by the daemon.
	StopDaemon     func()
	BudgetResume   func()
	IsBudgetPaused func() bool
}

// ServerConfig holds configuration for the API server.
type ServerConfig struct {
	Store    *db.Store
	DeployID string
	APIKey   string
}

// NewServer creates a new API server.
func NewServer(cfg ServerConfig) *Server {
	s := &Server{
		store:     cfg.Store,
		deployID:  cfg.DeployID,
		apiKey:    cfg.APIKey,
		startTime: time.Now(),
		mux:       http.NewServeMux(),
	}

	s.mux.HandleFunc("GET /status", s.handleStatus)
	s.mux.HandleFunc("GET /tasks", s.handleTasks)
	s.mux.HandleFunc("GET /tasks/{id}", s.handleTask)
	s.mux.HandleFunc("GET /tasks/{id}/log", s.handleTaskLog)
	s.mux.HandleFunc("GET /dep-graph", s.handleDepGraph)
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)
	s.mux.HandleFunc("GET /lessons", s.handleLessons)
	s.mux.HandleFunc("POST /stop", s.handleStop)
	s.mux.HandleFunc("POST /resume", s.handleResume)

	s.srv = &http.Server{
		Handler:      s.middleware(s.mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return s
}

// ListenAndServe starts the server.
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	log.Printf("API server listening on %s", ln.Addr())
	return s.srv.Serve(ln)
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "X-API-Key, Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if s.apiKey != "" {
			if r.Header.Get("X-API-Key") != s.apiKey {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}

		next.ServeHTTP(w, r)
	})
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
	if s.IsBudgetPaused != nil {
		budgetPaused = s.IsBudgetPaused()
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
			MaxAgents:  deploy.MaxAgents,
			MaxTurns:   deploy.MaxTurns,
			MaxBudget:  deploy.MaxBudgetUSD,
			Model:      deploy.AnalyzerModel,
			SkipLabel:  deploy.SkipLabel,
			AutoMerge:  deploy.AutoMerge,
			BaseBranch: deploy.BaseBranch,
		},
	})
}

func (s *Server) handleTasks(w http.ResponseWriter, _ *http.Request) {
	tasks, err := s.store.GetTasks(s.deployID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	var resp []TaskResponse
	for _, t := range tasks {
		resp = append(resp, taskToResponse(t))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid task id"})
		return
	}

	task, err := s.store.GetTask(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}

	writeJSON(w, http.StatusOK, taskToResponse(task))
}

func (s *Server) handleTaskLog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid task id"})
		return
	}

	task, err := s.store.GetTask(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task_not_found"})
		return
	}
	if !task.AgentLog.Valid {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "log_not_found"})
		return
	}

	// Stream the log file.
	f, err := os.Open(task.AgentLog.String)
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

	// Flush if possible.
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
	tasks, _ := s.store.GetTasks(s.deployID)
	spent, _ := s.store.TotalSpend(s.deployID)

	var totalCost float64
	statusCounts := make(map[string]int)
	for _, t := range tasks {
		statusCounts[t.Status]++
		totalCost += t.CostUSD
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total_tasks":   len(tasks),
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

	var resp []LessonResponse
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
	if s.StopDaemon != nil {
		go s.StopDaemon()
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
}

func (s *Server) handleResume(w http.ResponseWriter, _ *http.Request) {
	if s.BudgetResume != nil {
		s.BudgetResume()
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
	MaxAgents  int     `json:"max_agents"`
	MaxTurns   int     `json:"max_turns"`
	MaxBudget  float64 `json:"max_budget"`
	Model      string  `json:"model"`
	SkipLabel  string  `json:"skip_label"`
	AutoMerge  bool    `json:"auto_merge"`
	BaseBranch string  `json:"base_branch"`
}

// TaskResponse is the JSON shape for task endpoints.
type TaskResponse struct {
	ID          int64   `json:"id"`
	IssueNumber int     `json:"issue_number"`
	IssueTitle  string  `json:"issue_title"`
	Owner       string  `json:"owner"`
	Repo        string  `json:"repo"`
	Status      string  `json:"status"`
	PRNumber    int     `json:"pr_number,omitempty"`
	CostUSD     float64 `json:"cost_usd"`
	Branch      string  `json:"branch,omitempty"`
	StartedAt   string  `json:"started_at,omitempty"`
	CompletedAt string  `json:"completed_at,omitempty"`
	FailReason  string  `json:"failure_reason,omitempty"`
	FailDetail  string  `json:"failure_detail,omitempty"`
	ReviewRisk  string  `json:"review_risk,omitempty"`
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

func taskToResponse(t *db.Task) TaskResponse {
	resp := TaskResponse{
		ID:          t.ID,
		IssueNumber: t.IssueNumber,
		IssueTitle:  t.IssueTitle.String,
		Owner:       t.Owner,
		Repo:        t.Repo,
		Status:      t.Status,
		CostUSD:     t.CostUSD,
		Branch:      t.Branch.String,
		FailReason:  t.FailureReason.String,
		FailDetail:  t.FailureDetail.String,
		ReviewRisk:  t.ReviewRisk.String,
	}
	if t.PRNumber.Valid {
		resp.PRNumber = int(t.PRNumber.Int64)
	}
	if t.StartedAt.Valid {
		resp.StartedAt = t.StartedAt.Time.Format(time.RFC3339)
	}
	if t.CompletedAt.Valid {
		resp.CompletedAt = t.CompletedAt.Time.Format(time.RFC3339)
	}
	return resp
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
