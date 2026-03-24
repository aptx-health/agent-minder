package api

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
	"strings"
	"time"

	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/deploy"
)

// Server is a lightweight HTTP API server embedded in the deploy daemon.
type Server struct {
	store     *db.Store
	projectID int64
	deployID  string
	apiKey    string
	startTime time.Time
	mux       *http.ServeMux
	srv       *http.Server

	// triggerPoll is called to trigger a manual analysis poll cycle.
	// May be nil if not wired up.
	triggerPoll func()

	// stopDaemon is called to initiate graceful daemon shutdown.
	// May be nil if not wired up.
	stopDaemon func()

	// budgetResume is called to clear budget-paused state and resume.
	// May be nil if not wired up.
	budgetResume func()

	// isBudgetPaused returns true if the supervisor is currently paused due to budget ceiling.
	// May be nil.
	isBudgetPaused func() bool
}

// Config holds configuration for the API server.
type Config struct {
	Store          *db.Store
	ProjectID      int64
	DeployID       string
	APIKey         string
	BindAddr       string
	TriggerPoll    func()
	StopDaemon     func()
	BudgetResume   func()
	IsBudgetPaused func() bool
}

// New creates a new API server with the given configuration.
func New(cfg Config) *Server {
	s := &Server{
		store:          cfg.Store,
		projectID:      cfg.ProjectID,
		deployID:       cfg.DeployID,
		apiKey:         cfg.APIKey,
		startTime:      time.Now(),
		mux:            http.NewServeMux(),
		triggerPoll:    cfg.TriggerPoll,
		stopDaemon:     cfg.StopDaemon,
		budgetResume:   cfg.BudgetResume,
		isBudgetPaused: cfg.IsBudgetPaused,
	}

	s.mux.HandleFunc("GET /status", s.handleStatus)
	s.mux.HandleFunc("GET /tasks", s.handleTasks)
	s.mux.HandleFunc("GET /tasks/{id}", s.handleTask)
	s.mux.HandleFunc("GET /tasks/{id}/log", s.handleTaskLog)
	s.mux.HandleFunc("GET /dep-graph", s.handleDepGraph)
	s.mux.HandleFunc("GET /analysis", s.handleAnalysis)
	s.mux.HandleFunc("POST /analysis/poll", s.handleTriggerPoll)
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)
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

// ListenAndServe starts the HTTP server on the configured address.
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	log.Printf("API server listening on %s", ln.Addr())
	return s.srv.Serve(ln)
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// middleware applies auth and CORS headers to all requests.
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS headers for potential web UI consumption.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "X-API-Key, Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// API key authentication.
		if s.apiKey != "" {
			key := r.Header.Get("X-API-Key")
			if key != s.apiKey {
				writeJSON(w, http.StatusUnauthorized, errorResponse{"unauthorized", "invalid or missing X-API-Key header"})
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// --- Handlers ---

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	project, err := s.store.GetProjectByID(s.projectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"db_error", err.Error()})
		return
	}

	alive, pid := deploy.IsRunning(s.deployID)

	budgetPaused := false
	if s.isBudgetPaused != nil {
		budgetPaused = s.isBudgetPaused()
	}

	configMap := map[string]any{
		"max_agents":  project.AutopilotMaxAgents,
		"max_turns":   project.AutopilotMaxTurns,
		"max_budget":  project.AutopilotMaxBudgetUSD,
		"analyzer":    project.LLMAnalyzerModel,
		"skip_label":  project.AutopilotSkipLabel,
		"auto_merge":  project.AutopilotAutoMerge,
		"base_branch": project.AutopilotBaseBranch,
	}
	if project.TotalBudgetUSD > 0 {
		configMap["total_budget"] = project.TotalBudgetUSD
		configMap["budget_pause_running"] = project.BudgetPauseRunning
	}

	resp := map[string]any{
		"deploy_id":     s.deployID,
		"project_id":    s.projectID,
		"pid":           pid,
		"alive":         alive,
		"budget_paused": budgetPaused,
		"uptime_sec":    int(time.Since(s.startTime).Seconds()),
		"started_at":    s.startTime.UTC().Format(time.RFC3339),
		"config":        configMap,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleTasks(w http.ResponseWriter, _ *http.Request) {
	tasks, err := s.store.GetAutopilotTasks(s.projectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"db_error", err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, tasksToJSON(tasks))
}

func (s *Server) handleTask(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	task, err := s.findTask(idStr)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"db_error", err.Error()})
		return
	}
	if task == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{"not_found", fmt.Sprintf("task %s not found", idStr)})
		return
	}
	writeJSON(w, http.StatusOK, taskToJSON(*task))
}

func (s *Server) handleTaskLog(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")

	// Find the task (by ID or issue number).
	task, err := s.findTask(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"bad_request", err.Error()})
		return
	}
	if task == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{"not_found", "task not found"})
		return
	}

	logPath := task.AgentLog
	if logPath == "" {
		writeJSON(w, http.StatusNotFound, errorResponse{"not_found", "no agent log for this task"})
		return
	}

	// Check for streaming request.
	stream := r.URL.Query().Get("stream") == "true"

	if stream {
		s.streamLog(r.Context(), w, logPath)
		return
	}

	// Return the full log content.
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, errorResponse{"not_found", "log file does not exist"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{"io_error", err.Error()})
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// streamLog streams a log file using Server-Sent Events.
func (s *Server) streamLog(ctx context.Context, w http.ResponseWriter, logPath string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"stream_error", "streaming not supported"})
		return
	}

	// Disable write deadline for long-lived SSE connections — the server's
	// WriteTimeout (30s) would otherwise kill the stream prematurely.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, errorResponse{"not_found", "log file does not exist"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{"io_error", err.Error()})
		return
	}
	defer func() { _ = f.Close() }()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	reader := bufio.NewReader(f)
	// Send existing content first.
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", strings.TrimRight(line, "\n"))
		}
		if err != nil {
			break
		}
	}
	flusher.Flush()

	// Tail for new content.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				line, err := reader.ReadString('\n')
				if line != "" {
					_, _ = fmt.Fprintf(w, "data: %s\n\n", strings.TrimRight(line, "\n"))
				}
				if err != nil {
					break
				}
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleDepGraph(w http.ResponseWriter, _ *http.Request) {
	dg, err := s.store.GetDepGraph(s.projectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"db_error", err.Error()})
		return
	}
	if dg == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{"not_found", "no dependency graph stored"})
		return
	}

	// Parse the graph JSON for a cleaner response.
	var graphData any
	if err := json.Unmarshal([]byte(dg.GraphJSON), &graphData); err != nil {
		graphData = dg.GraphJSON // fall back to raw string
	}

	resp := map[string]any{
		"strategy":   dg.OptionName,
		"graph":      graphData,
		"created_at": dg.CreatedAt,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAnalysis(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 5
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}

	polls, err := s.store.RecentPolls(s.projectID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"db_error", err.Error()})
		return
	}

	results := make([]map[string]any, 0, len(polls))
	for _, p := range polls {
		results = append(results, map[string]any{
			"id":               p.ID,
			"new_commits":      p.NewCommits,
			"new_messages":     p.NewMessages,
			"concerns_raised":  p.ConcernsRaised,
			"analysis":         p.LLMResponse(),
			"bus_message_sent": p.BusMessageSent,
			"polled_at":        p.PolledAt,
		})
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleTriggerPoll(w http.ResponseWriter, _ *http.Request) {
	if s.triggerPoll == nil {
		writeJSON(w, http.StatusNotImplemented, errorResponse{"not_implemented", "poll trigger not wired up"})
		return
	}
	s.triggerPoll()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "poll triggered"})
}

func (s *Server) handleStop(w http.ResponseWriter, _ *http.Request) {
	if s.stopDaemon == nil {
		writeJSON(w, http.StatusNotImplemented, errorResponse{"not_implemented", "stop not wired up"})
		return
	}
	// Trigger stop in background so we can send the response first.
	go s.stopDaemon()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stop signal sent"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	today := time.Now().Format("2006-01-02")

	overall, err := s.store.OverallCost(s.projectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"db_error", err.Error()})
		return
	}

	daily, _ := s.store.DailyCost(s.projectID, today)
	weekly, _ := s.store.WeeklyCost(s.projectID, today)

	// Task status counts.
	tasks, err := s.store.GetAutopilotTasks(s.projectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{"db_error", err.Error()})
		return
	}

	statusCounts := make(map[string]int)
	var totalCost float64
	for _, t := range tasks {
		statusCounts[t.Status]++
		totalCost += t.CostUSD
	}

	doneCount := statusCounts["done"]
	bailedCount := statusCounts["bailed"]
	failedCount := statusCounts["failed"]
	completedTotal := doneCount + bailedCount + failedCount
	var successRate float64
	if completedTotal > 0 {
		successRate = float64(doneCount) / float64(completedTotal)
	}

	// Look up project for budget ceiling info.
	project, _ := s.store.GetProjectByID(s.projectID)

	spendMap := map[string]any{
		"total":   totalCost,
		"daily":   daily,
		"weekly":  weekly,
		"overall": overall,
	}
	if project != nil && project.TotalBudgetUSD > 0 {
		spendMap["ceiling"] = project.TotalBudgetUSD
		spendMap["remaining"] = project.TotalBudgetUSD - totalCost
		if totalCost > 0 {
			spendMap["utilization"] = totalCost / project.TotalBudgetUSD
		} else {
			spendMap["utilization"] = 0.0
		}
	}

	budgetPaused := false
	if s.isBudgetPaused != nil {
		budgetPaused = s.isBudgetPaused()
	}

	resp := map[string]any{
		"spend": spendMap,
		"tasks": map[string]any{
			"total":        len(tasks),
			"by_status":    statusCounts,
			"success_rate": successRate,
		},
		"budget_paused": budgetPaused,
		"uptime_sec":    int(time.Since(s.startTime).Seconds()),
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleResume(w http.ResponseWriter, _ *http.Request) {
	if s.budgetResume == nil {
		writeJSON(w, http.StatusNotImplemented, errorResponse{"not_implemented", "resume not wired up"})
		return
	}
	if s.isBudgetPaused != nil && !s.isBudgetPaused() {
		writeJSON(w, http.StatusOK, map[string]string{"status": "not_paused", "message": "supervisor is not budget-paused"})
		return
	}
	s.budgetResume()
	writeJSON(w, http.StatusOK, map[string]string{"status": "resumed", "message": "budget pause cleared — task launches will resume"})
}

// --- Helpers ---

func (s *Server) findTask(idStr string) (*db.AutopilotTask, error) {
	tasks, err := s.store.GetAutopilotTasks(s.projectID)
	if err != nil {
		return nil, err
	}

	// Try as task ID first, then issue number.
	if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
		for i := range tasks {
			if tasks[i].ID == id {
				return &tasks[i], nil
			}
		}
		// Fall through to issue number.
		for i := range tasks {
			if tasks[i].IssueNumber == int(id) {
				return &tasks[i], nil
			}
		}
	}
	return nil, nil
}

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func taskToJSON(t db.AutopilotTask) map[string]any {
	m := map[string]any{
		"id":           t.ID,
		"issue_number": t.IssueNumber,
		"issue_title":  t.IssueTitle,
		"owner":        t.Owner,
		"repo":         t.Repo,
		"status":       t.Status,
		"cost_usd":     t.CostUSD,
		"pr_number":    t.PRNumber,
		"branch":       t.Branch,
		"started_at":   t.StartedAt,
		"completed_at": t.CompletedAt,
		"agent_log":    t.AgentLog,
	}

	// Parse dependencies JSON.
	var deps any
	if err := json.Unmarshal([]byte(t.Dependencies), &deps); err == nil {
		m["dependencies"] = deps
	} else {
		m["dependencies"] = t.Dependencies
	}

	if t.FailureReason != "" {
		m["failure_reason"] = t.FailureReason
		m["failure_detail"] = t.FailureDetail
	}
	if t.ReviewRisk != nil {
		m["review_risk"] = *t.ReviewRisk
	}
	if t.MaxTurnsOverride != nil {
		m["max_turns_override"] = *t.MaxTurnsOverride
	}
	if t.MaxBudgetOverride != nil {
		m["max_budget_override"] = *t.MaxBudgetOverride
	}
	return m
}

func tasksToJSON(tasks []db.AutopilotTask) []map[string]any {
	result := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		result = append(result, taskToJSON(t))
	}
	return result
}
