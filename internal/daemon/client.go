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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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

// GetJobs fetches all jobs.
func (c *Client) GetJobs() ([]JobResponse, error) {
	var resp []JobResponse
	if err := c.getJSON("/jobs", &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetJob fetches a single job.
func (c *Client) GetJob(id int64) (*JobResponse, error) {
	var resp JobResponse
	if err := c.getJSON(fmt.Sprintf("/jobs/%d", id), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetJobLog fetches the log content for a job.
func (c *Client) GetJobLog(id int64) (string, error) {
	resp, err := c.do("GET", fmt.Sprintf("/jobs/%d/log", id))
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

// GetJobLogStream returns the raw HTTP response for streaming a job's log.
// Caller is responsible for closing the response body.
func (c *Client) GetJobLogStream(id int64) (*http.Response, error) {
	// Use a longer timeout for streaming.
	streamClient := &http.Client{Timeout: 0}
	req, err := http.NewRequest("GET", c.baseURL+fmt.Sprintf("/jobs/%d/log", id), nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
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
