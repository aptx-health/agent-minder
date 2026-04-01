package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client connects to the daemon HTTP API.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a new daemon API client.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) do(method, path string) (*http.Response, error) {
	req, err := http.NewRequest(method, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	return c.httpClient.Do(req)
}

func (c *Client) getJSON(path string, v any) error {
	resp, err := c.do("GET", path)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	return json.NewDecoder(resp.Body).Decode(v)
}

func (c *Client) post(path string) error {
	resp, err := c.do("POST", path)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	return nil
}

// GetStatus fetches the daemon status.
func (c *Client) GetStatus() (*StatusResponse, error) {
	var resp StatusResponse
	if err := c.getJSON("/status", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetTasks fetches all tasks.
func (c *Client) GetTasks() ([]TaskResponse, error) {
	var resp []TaskResponse
	if err := c.getJSON("/tasks", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetTask fetches a single task.
func (c *Client) GetTask(id int64) (*TaskResponse, error) {
	var resp TaskResponse
	if err := c.getJSON(fmt.Sprintf("/tasks/%d", id), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetTaskLog fetches the log content for a task.
func (c *Client) GetTaskLog(id int64) (string, error) {
	resp, err := c.do("GET", fmt.Sprintf("/tasks/%d/log", id))
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// GetDepGraph fetches the dependency graph.
func (c *Client) GetDepGraph() (*DepGraphResponse, error) {
	var resp DepGraphResponse
	if err := c.getJSON("/dep-graph", &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetMetrics fetches cost and status metrics.
func (c *Client) GetMetrics() (map[string]any, error) {
	var resp map[string]any
	if err := c.getJSON("/metrics", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetLessons fetches active lessons.
func (c *Client) GetLessons() ([]LessonResponse, error) {
	var resp []LessonResponse
	if err := c.getJSON("/lessons", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Stop sends a stop request to the daemon.
func (c *Client) Stop() error {
	return c.post("/stop")
}

// Resume sends a budget resume request to the daemon.
func (c *Client) Resume() error {
	return c.post("/resume")
}
