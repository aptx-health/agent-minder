package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is an HTTP client for the deploy daemon's status API.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a new API client for the given remote address.
// The addr should be "host:port" — the client prepends "http://".
func NewClient(addr, apiKey string) *Client {
	base := addr
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	base = strings.TrimRight(base, "/")

	return &Client{
		baseURL: base,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// StatusResponse represents the /status endpoint response.
type StatusResponse struct {
	DeployID  string       `json:"deploy_id"`
	ProjectID int64        `json:"project_id"`
	PID       int          `json:"pid"`
	Alive     bool         `json:"alive"`
	UptimeSec int          `json:"uptime_sec"`
	StartedAt string       `json:"started_at"`
	Config    DeployConfig `json:"config"`
}

// DeployConfig represents the config portion of the status response.
type DeployConfig struct {
	MaxAgents  int     `json:"max_agents"`
	MaxTurns   int     `json:"max_turns"`
	MaxBudget  float64 `json:"max_budget"`
	Analyzer   string  `json:"analyzer"`
	SkipLabel  string  `json:"skip_label"`
	AutoMerge  bool    `json:"auto_merge"`
	BaseBranch string  `json:"base_branch"`
}

// TaskResponse represents a task from the /tasks endpoint.
type TaskResponse struct {
	ID            int64   `json:"id"`
	IssueNumber   int     `json:"issue_number"`
	IssueTitle    string  `json:"issue_title"`
	Owner         string  `json:"owner"`
	Repo          string  `json:"repo"`
	Status        string  `json:"status"`
	CostUSD       float64 `json:"cost_usd"`
	PRNumber      int     `json:"pr_number"`
	Branch        string  `json:"branch"`
	StartedAt     string  `json:"started_at"`
	CompletedAt   string  `json:"completed_at"`
	AgentLog      string  `json:"agent_log"`
	FailureReason string  `json:"failure_reason,omitempty"`
	FailureDetail string  `json:"failure_detail,omitempty"`
	ReviewRisk    string  `json:"review_risk,omitempty"`
}

// GetStatus calls GET /status on the remote daemon.
func (c *Client) GetStatus() (*StatusResponse, error) {
	var resp StatusResponse
	if err := c.get("/status", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetTasks calls GET /tasks on the remote daemon.
func (c *Client) GetTasks() ([]TaskResponse, error) {
	var resp []TaskResponse
	if err := c.get("/tasks", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetTask calls GET /tasks/{id} on the remote daemon.
func (c *Client) GetTask(id string) (*TaskResponse, error) {
	var resp TaskResponse
	if err := c.get("/tasks/"+id, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AnalysisResponse represents a single poll result from GET /analysis.
type AnalysisResponse struct {
	ID             int64  `json:"id"`
	NewCommits     int    `json:"new_commits"`
	NewMessages    int    `json:"new_messages"`
	ConcernsRaised int    `json:"concerns_raised"`
	Analysis       string `json:"analysis"`
	BusMessageSent bool   `json:"bus_message_sent"`
	PolledAt       string `json:"polled_at"`
}

// TriggerPoll calls POST /analysis/poll on the remote daemon to trigger an on-demand analysis.
func (c *Client) TriggerPoll() error {
	return c.post("/analysis/poll", nil)
}

// GetAnalysis calls GET /analysis on the remote daemon.
func (c *Client) GetAnalysis(limit int) ([]AnalysisResponse, error) {
	path := "/analysis"
	if limit > 0 {
		path = fmt.Sprintf("/analysis?limit=%d", limit)
	}
	var resp []AnalysisResponse
	if err := c.get(path, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Stop calls POST /stop on the remote daemon to initiate graceful shutdown.
func (c *Client) Stop() error {
	return c.post("/stop", nil)
}

// get performs an authenticated GET request and decodes the JSON response.
func (c *Client) get(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return classifyError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("authentication failed — check --api-key or MINDER_API_KEY")
	}
	if resp.StatusCode != http.StatusOK {
		var errResp errorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Message != "" {
			return fmt.Errorf("remote error (%d): %s", resp.StatusCode, errResp.Message)
		}
		return fmt.Errorf("remote returned HTTP %d", resp.StatusCode)
	}

	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// post performs an authenticated POST request.
func (c *Client) post(path string, out any) error {
	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return classifyError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("authentication failed — check --api-key or MINDER_API_KEY")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		var errResp errorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Message != "" {
			return fmt.Errorf("remote error (%d): %s", resp.StatusCode, errResp.Message)
		}
		return fmt.Errorf("remote returned HTTP %d", resp.StatusCode)
	}

	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// classifyError converts low-level network errors into user-friendly messages.
func classifyError(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "connection refused") {
		return fmt.Errorf("connection refused — is the daemon running with --serve?")
	}
	if strings.Contains(msg, "no such host") {
		return fmt.Errorf("host not found — check the --remote address")
	}
	if strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "context deadline exceeded") {
		return fmt.Errorf("connection timed out — check the --remote address and network")
	}
	return fmt.Errorf("connection error: %w", err)
}
